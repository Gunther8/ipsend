package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zeebo/xxh3"

	"ipsend/internal/proto"
)

// sessionMeta 是会话的静态信息，接受时写入 .ipsend/<tid>/session.json，之后不再改写。
// 会话进行中的变化（顶层目录映射、已完成文件）追加写入 journal.log，
// 避免每提交一个文件都重写整个清单（上万文件时是 O(n²)）。
type sessionMeta struct {
	TransferID string            `json:"transfer_id"`
	SenderIP   string            `json:"sender_ip"`
	SenderName string            `json:"sender_name"`
	Files      []proto.FileEntry `json:"files"` // 原始清单（用于续传比对）
	Clean      [][]string        `json:"clean"` // 清洗后的路径段
	Dirs       [][]string        `json:"dirs"`  // 清洗后的空目录
	Created    time.Time         `json:"created"`
}

type journalRec struct {
	T   string `json:"t"` // "top" | "done"
	K   string `json:"k,omitempty"`
	V   string `json:"v,omitempty"`
	FID int    `json:"fid"`
}

type fileState struct {
	entry proto.FileEntry
	segs  []string
	part  string

	mu       sync.Mutex // 串行化同一文件的 data/commit
	received atomic.Int64
	hasher   *xxh3.Hasher
	hashedAt int64 // hasher 已覆盖的字节数；-1 表示无效
	done     bool
}

type session struct {
	sid    string
	ip     string
	sender string
	dir    string
	curl   bool
	meta   sessionMeta
	files  []*fileState
	total  int64

	mu      sync.Mutex // 保护 top / done / journal
	top     map[string]string
	done    map[int]string
	journal *os.File

	bytes      atomic.Int64 // 已收字节（含续传前已有部分）
	filesDone  atomic.Int32
	cancelled  atomic.Bool
	lastActive atomic.Int64 // unix 秒，用于回收被对方放弃的会话
	stalled    atomic.Bool  // 已发过 interrupted 事件
	lastEmit   atomic.Int64 // 上次进度事件时间（unix 纳秒）
}

// shouldEmit 按会话节流进度事件：第一次立即返回 true，之后每 emitEvery 最多一次（并发请求共享）。
func (s *session) shouldEmit() bool {
	now := time.Now().UnixNano()
	last := s.lastEmit.Load()
	if now-last < int64(emitEvery) {
		return false
	}
	return s.lastEmit.CompareAndSwap(last, now)
}

func (s *session) touch() {
	s.lastActive.Store(time.Now().Unix())
	s.stalled.Store(false)
}

func partPath(dir string, fid int) string {
	return filepath.Join(dir, strconv.Itoa(fid)+".part")
}

// writeMeta 写 session.json 并新建（清空）journal。
func (s *session) writeMeta(fresh bool) error {
	if fresh {
		s.meta.Created = time.Now()
		b, err := json.Marshal(&s.meta)
		if err != nil {
			return err
		}
		tmp := filepath.Join(s.dir, "session.json.tmp")
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(s.dir, "session.json")); err != nil {
			return err
		}
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fresh {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "journal.log"), flag, 0o644)
	if err != nil {
		return err
	}
	s.journal = f
	return nil
}

// appendJournal 追加一条记录；调用方需持有 s.mu。
func (s *session) appendJournal(r journalRec) {
	if s.journal == nil {
		return
	}
	b, _ := json.Marshal(r)
	b = append(b, '\n')
	_, _ = s.journal.Write(b)
}

func (s *session) closeJournal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journal != nil {
		s.journal.Close()
		s.journal = nil
	}
}

// loadSession 读取 session.json 并重放 journal.log。
func loadSession(dir string) (*sessionMeta, map[string]string, map[int]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return nil, nil, nil, err
	}
	var m sessionMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, nil, err
	}
	top := map[string]string{}
	done := map[int]string{}
	if f, err := os.Open(filepath.Join(dir, "journal.log")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var r journalRec
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue // 最后一行可能写了一半
			}
			switch r.T {
			case "top":
				top[r.K] = r.V
			case "done":
				done[r.FID] = r.V
			}
		}
	}
	return &m, top, done, nil
}

func sameFiles(a, b []proto.FileEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// rehash 把 part 文件 [0,n) 重新读入新的 hasher（续传、重启后使用）。onProgress 可为空。
func (f *fileState) rehash(n int64, onProgress func(done int64)) error {
	h := xxh3.New()
	if n > 0 {
		fh, err := os.Open(f.part)
		if err != nil {
			return err
		}
		defer fh.Close()
		buf := make([]byte, copyBufSize)
		var got int64
		last := time.Now()
		r := io.LimitReader(fh, n)
		for {
			k, err := r.Read(buf)
			if k > 0 {
				h.Write(buf[:k])
				got += int64(k)
				if onProgress != nil && time.Since(last) > 200*time.Millisecond {
					last = time.Now()
					onProgress(got)
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
		if got != n {
			return fmt.Errorf("part file shorter than expected: %d < %d", got, n)
		}
	}
	f.hasher = h
	f.hashedAt = n
	return nil
}

// partSize 返回磁盘上 part 文件的实际大小（不存在则 0）。
func partSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}
