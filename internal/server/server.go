// Package server 实现 ipsend 接收端 HTTP 服务（见 SPEC.md §3）。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ipsend/internal/fsutil"
	"ipsend/internal/proto"
)

const (
	stateDirName = ".ipsend"
	staleAfter   = 7 * 24 * time.Hour
	copyBufSize  = 1 << 20
	emitEvery    = 200 * time.Millisecond
	idleSession  = 6 * time.Hour
	stallCheck   = 10 * time.Second
	stallAfter   = 30 * time.Second
)

// Event 是给界面层的通知。所有事件都带 SID（curl 上传为 "curl-..."）。
//
//	accepted    用户接受，开始接收（FileCount、SessTotal、SessBytes=续传前已有、Resume）
//	progress    数据进度（Name=当前文件，SessBytes/SessTotal、FilesDone/FileCount）
//	rehash      续传前重新哈希已收部分（Name、Bytes/Total）
//	saved       一个文件已校验并保存（Name=实际相对路径、Bytes）
//	finished    整批完成（FilesDone、SessTotal）
//	interrupted 连接中断（对方可重发续传）
//	cancelled   被取消（Name="sender" 或 "receiver"）
//	rejected    用户拒绝或超时
//	error       出错（Err）
type Event struct {
	Kind      string
	SID       string
	RemoteIP  string
	Sender    string
	Name      string
	Bytes     int64
	Total     int64
	SessBytes int64
	SessTotal int64
	FilesDone int
	FileCount int
	Resume    bool
	Err       error
}

type Config struct {
	Port     int
	RecvDir  string
	Name     string // 本机名，ping 返回
	Approver Approver
	OnEvent  func(Event) // 可为空；会在多个 goroutine 中调用
	Logger   *log.Logger // 可为空

	PIN             string         // 非空时要求对方提供 PIN
	Blocked         []string       // 初始屏蔽列表
	OnBlockedChange func([]string) // 屏蔽列表变化时回调（用于保存），可为空
}

type Server struct {
	cfg   Config
	queue *approvalQueue
	pin   *pinGuard
	log   *log.Logger
	srv   *http.Server
	stop  chan struct{}

	mu       sync.Mutex
	sessions map[string]*session // sid -> session（含 curl）
	byTID    map[string]*session // transfer_id -> session
	placeMu  sync.Mutex          // 顶层目录映射
}

func New(cfg Config) (*Server, error) {
	if cfg.Port == 0 {
		cfg.Port = proto.DefaultPort
	}
	if cfg.Approver == nil {
		return nil, errors.New("approver is required")
	}
	abs, err := filepath.Abs(cfg.RecvDir)
	if err != nil {
		return nil, err
	}
	cfg.RecvDir = abs
	if err := os.MkdirAll(filepath.Join(abs, stateDirName), 0o755); err != nil {
		return nil, err
	}
	hideDir(filepath.Join(abs, stateDirName))
	lg := cfg.Logger
	if lg == nil {
		lg = log.New(os.Stderr, "[server] ", log.LstdFlags)
	}
	s := &Server{
		cfg:      cfg,
		queue:    newApprovalQueue(cfg.Approver, cfg.Blocked, cfg.OnBlockedChange),
		pin:      newPinGuard(cfg.PIN),
		log:      lg,
		stop:     make(chan struct{}),
		sessions: map[string]*session{},
		byTID:    map[string]*session{},
	}
	s.cleanupStale()
	return s, nil
}

func (s *Server) stateDir() string { return filepath.Join(s.cfg.RecvDir, stateDirName) }

// RecvDir 返回接收目录的绝对路径。
func (s *Server) RecvDir() string { return s.cfg.RecvDir }

// SetPIN 运行中修改 PIN（空串表示不需要）。
func (s *Server) SetPIN(pin string) { s.pin.set(pin) }

// Blocked 返回当前屏蔽的 IP。
func (s *Server) Blocked() []string { return s.queue.Blocked() }

// Unblock 解除屏蔽。
func (s *Server) Unblock(ip string) { s.queue.Unblock(ip) }

// Active 返回进行中的接收会话数。
func (s *Server) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sess := range s.sessions {
		if !sess.cancelled.Load() {
			n++
		}
	}
	return n
}

func (s *Server) emit(e Event) {
	if s.cfg.OnEvent != nil {
		s.cfg.OnEvent(e)
	}
}

func (s *Server) sessEvent(kind string, sess *session) Event {
	return Event{Kind: kind, SID: sess.sid, RemoteIP: sess.ip, Sender: sess.sender,
		SessBytes: sess.bytes.Load(), SessTotal: sess.total,
		FilesDone: int(sess.filesDone.Load()), FileCount: len(sess.files)}
}

// Handler 返回路由（测试时可直接用 httptest）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ping", s.handlePing)
	mux.HandleFunc("POST /api/offer", s.handleOffer)
	mux.HandleFunc("GET /api/status/{sid}/{fid}", s.handleStatus)
	mux.HandleFunc("PUT /api/data/{sid}/{fid}", s.handleData)
	mux.HandleFunc("POST /api/commit/{sid}/{fid}", s.handleCommit)
	mux.HandleFunc("POST /api/finish/{sid}", s.handleFinish)
	mux.HandleFunc("POST /api/cancel/{sid}", s.handleCancel)
	mux.HandleFunc("PUT /upload/{name...}", s.handleUpload)
	return mux
}

// ListenAndServe 监听 :port（IPv4/IPv6 双栈），阻塞直到 Shutdown。
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(s.cfg.Port))
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

func (s *Server) Serve(ln net.Listener) error {
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	go func() { // 每 10 秒检查停滞的会话；每小时清理过期会话
		t := time.NewTicker(stallCheck)
		defer t.Stop()
		lastClean := time.Now()
		for {
			select {
			case <-t.C:
				s.checkStalled(stallAfter)
				if time.Since(lastClean) > time.Hour {
					lastClean = time.Now()
					s.dropIdleSessions(idleSession)
					s.cleanupStale()
				}
			case <-s.stop:
				return
			}
		}
	}()
	s.log.Printf("listening on %s, saving to %s", ln.Addr(), s.cfg.RecvDir)
	err := s.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	var err error
	if s.srv != nil {
		err = s.srv.Shutdown(ctx)
	}
	s.dropIdleSessions(0)
	return err
}

// checkStalled 对超过 after 没有任何活动的会话发一次 interrupted 事件（对方崩溃、断网、或在拿到 sid 前取消）。
// 会话恢复活动后会重新计时。
func (s *Server) checkStalled(after time.Duration) {
	cutoff := time.Now().Add(-after).Unix()
	var stalled []*session
	s.mu.Lock()
	for _, sess := range s.sessions {
		if !sess.curl && !sess.cancelled.Load() && sess.lastActive.Load() < cutoff && sess.stalled.CompareAndSwap(false, true) {
			stalled = append(stalled, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range stalled {
		e := s.sessEvent("interrupted", sess)
		e.Err = errors.New("对方长时间无响应")
		s.emit(e)
	}
}

// dropIdleSessions 从内存中移除闲置超过 idle 的会话并关闭其 journal（磁盘上的断点数据保留，可续传）。
// idle 为 0 时移除全部。
func (s *Server) dropIdleSessions(idle time.Duration) {
	cutoff := time.Now().Add(-idle).Unix()
	var drop []*session
	s.mu.Lock()
	for sid, sess := range s.sessions {
		if idle == 0 || sess.lastActive.Load() < cutoff {
			drop = append(drop, sess)
			delete(s.sessions, sid)
			if !sess.curl && s.byTID[sess.meta.TransferID] == sess {
				delete(s.byTID, sess.meta.TransferID)
			}
		}
	}
	s.mu.Unlock()
	for _, sess := range drop {
		sess.cancelled.Store(true)
		sess.closeJournal()
	}
}

// CancelSession 由接收方取消一个正在进行的接收。已收数据保留，对方重发时可续传。
func (s *Server) CancelSession(sid string) bool {
	s.mu.Lock()
	sess := s.sessions[sid]
	s.mu.Unlock()
	if sess == nil || !sess.cancelled.CompareAndSwap(false, true) {
		return false
	}
	s.log.Printf("receiver cancelled session %s from %s", sid, sess.ip)
	e := s.sessEvent("cancelled", sess)
	e.Name = "receiver"
	s.emit(e)
	return true
}

// ---------- helpers ----------

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		return ip.String()
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, proto.Error{Error: errCode, Message: msg})
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (s *Server) cleanupStale() {
	ents, err := os.ReadDir(s.stateDir())
	if err != nil {
		return
	}
	s.mu.Lock()
	active := map[string]bool{}
	for tid := range s.byTID {
		active[tid] = true
	}
	s.mu.Unlock()
	for _, e := range ents {
		if active[e.Name()] {
			continue
		}
		p := filepath.Join(s.stateDir(), e.Name())
		newest := time.Time{}
		if info, err := e.Info(); err == nil {
			newest = info.ModTime()
		}
		if e.IsDir() { // 目录的修改时间不随文件内容变化，取其中最新的文件
			if sub, err := os.ReadDir(p); err == nil {
				for _, f := range sub {
					if fi, err := f.Info(); err == nil && fi.ModTime().After(newest) {
						newest = fi.ModTime()
					}
				}
			}
		}
		if time.Since(newest) < staleAfter {
			continue
		}
		s.log.Printf("cleanup stale %s", e.Name())
		_ = os.RemoveAll(p)
	}
}

// approve 统一处理排队/超时/屏蔽，返回 true 表示已接受；否则已写好错误响应。
func (s *Server) approve(w http.ResponseWriter, r *http.Request, req Request, plainText bool) bool {
	fail := func(code int, errCode, msg string) bool {
		if plainText {
			http.Error(w, msg, code)
		} else {
			writeErr(w, code, errCode, msg)
		}
		return false
	}
	if s.queue.IsBlocked(req.RemoteIP) {
		return fail(http.StatusForbidden, "rejected", "rejected")
	}
	s.log.Printf("request from %s (%s): %d files, %s", req.RemoteIP, req.SenderName, req.FileCount, fsutil.HumanBytes(req.TotalSize))
	d, err := s.queue.Ask(r.Context(), req)
	switch {
	case errors.Is(err, errQueueFull):
		return fail(http.StatusServiceUnavailable, "busy", "receiver queue is full, try later")
	case errors.Is(err, errIPQueueFull):
		return fail(http.StatusTooManyRequests, "too_many", "too many pending requests from your ip")
	case d != Accept:
		s.log.Printf("rejected %s", req.RemoteIP)
		s.emit(Event{Kind: "rejected", RemoteIP: req.RemoteIP, Sender: req.SenderName})
		return fail(http.StatusForbidden, "rejected", "rejected by receiver")
	}
	s.log.Printf("accepted %s", req.RemoteIP)
	return true
}

// place 把临时文件移动到接收目录下 segs 对应位置，返回实际相对路径。调用方需持有 sess.mu（sess 可为 nil）。
func (s *Server) place(src string, segs []string, sess *session, mtime int64) (string, error) {
	dir := s.cfg.RecvDir
	rel := []string{}
	if len(segs) > 1 {
		mapped, err := s.mapTop(segs[0], sess)
		if err != nil {
			return "", err
		}
		rel = append(rel, mapped)
		rel = append(rel, segs[1:len(segs)-1]...)
		dir = filepath.Join(append([]string{dir}, rel...)...)
	}
	name, err := fsutil.PlaceFile(src, dir, segs[len(segs)-1])
	if err != nil {
		return "", err
	}
	rel = append(rel, name)
	if mtime > 0 {
		t := time.Unix(mtime, 0)
		_ = os.Chtimes(filepath.Join(dir, name), t, t)
	}
	return strings.Join(rel, "/"), nil
}

// mapTop 返回顶层目录的实际名字；首次出现时原子地创建新目录（重名则 "dir (1)"）。调用方需持有 sess.mu。
func (s *Server) mapTop(name string, sess *session) (string, error) {
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	if sess != nil {
		if m, ok := sess.top[name]; ok {
			if err := os.MkdirAll(filepath.Join(s.cfg.RecvDir, m), 0o755); err != nil {
				return "", err
			}
			return m, nil
		}
	}
	m, err := fsutil.MkdirUnique(s.cfg.RecvDir, name)
	if err != nil {
		return "", err
	}
	if sess != nil {
		sess.top[name] = m
		sess.appendJournal(journalRec{T: "top", K: name, V: m})
	}
	return m, nil
}

// copyBody 从 r 读取最多 limit 字节写入 w（同时写 hash，可为 nil），每块调用 onChunk。
// 返回实际写入字节数；overflow 表示 r 中数据超过 limit；cancelled 表示被取消。
func copyBody(w io.Writer, hash io.Writer, r io.Reader, limit int64, cancelled func() bool, onChunk func(n int64)) (written int64, overflow, wasCancelled bool, werr, rerr error) {
	buf := make([]byte, copyBufSize)
	body := io.LimitReader(r, limit+1) // 多读 1 字节以发现超长
	remain := limit
	for {
		if cancelled() {
			return written, false, true, nil, nil
		}
		n, err := body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if int64(n) > remain {
				chunk = buf[:remain]
				overflow = true
			}
			if len(chunk) > 0 {
				if _, werr = w.Write(chunk); werr != nil {
					return
				}
				if hash != nil {
					hash.Write(chunk)
				}
				remain -= int64(len(chunk))
				written += int64(len(chunk))
				onChunk(int64(len(chunk)))
			}
			if overflow {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				rerr = err
			}
			return
		}
	}
}

// ---------- ping ----------

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, proto.Ping{App: proto.AppName, Version: proto.Version, Proto: proto.ProtoVer, Name: s.cfg.Name, PIN: s.pin.required()})
}

// ---------- curl upload ----------

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if !s.checkPIN(w, r, ip, true) {
		return
	}
	if r.ContentLength < 0 {
		http.Error(w, "Content-Length required (use curl -T file)", http.StatusLengthRequired)
		return
	}
	segs, err := fsutil.SanitizeRelPath(r.PathValue("name"))
	if err != nil {
		segs = []string{"upload.bin"}
	}
	size := r.ContentLength
	if err := fsutil.CheckSpace(s.cfg.RecvDir, size, size); err != nil {
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
		return
	}
	display := strings.Join(segs, "/")
	req := Request{Kind: "curl", RemoteIP: ip, SenderName: "curl", FileCount: 1, TotalSize: size, Names: []string{display}, Sizes: []int64{size}}
	if !s.approve(w, r, req, true) {
		return
	}

	sess := &session{sid: "curl-" + randHex(8), ip: ip, sender: "curl", curl: true, total: size,
		files: []*fileState{{entry: proto.FileEntry{Path: display, Size: size}}}}
	sess.touch()
	s.mu.Lock()
	s.sessions[sess.sid] = sess
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, sess.sid)
		s.mu.Unlock()
	}()
	ev := s.sessEvent("accepted", sess)
	s.emit(ev)

	// 接受后第一次读 body，Go 此时自动回 100 Continue
	part := filepath.Join(s.stateDir(), sess.sid+".part")
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		http.Error(w, "cannot create file", http.StatusInternalServerError)
		return
	}
	n, overflow, cancelled, werr, rerr := copyBody(f, nil, r.Body, size, sess.cancelled.Load, func(k int64) {
		sess.bytes.Add(k)
		if sess.shouldEmit() {
			e := s.sessEvent("progress", sess)
			e.Name = display
			s.emit(e)
		}
	})
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil || rerr != nil || overflow || cancelled || n != size {
		_ = os.Remove(part)
		switch {
		case cancelled:
			http.Error(w, "cancelled by receiver", http.StatusGone)
		case werr != nil:
			http.Error(w, "write failed: "+werr.Error(), http.StatusInternalServerError)
			s.emit(Event{Kind: "error", SID: sess.sid, RemoteIP: ip, Err: werr})
		default:
			s.log.Printf("curl upload from %s incomplete: %d/%d (%v)", ip, n, size, rerr)
			s.emit(s.sessEvent("interrupted", sess))
			http.Error(w, fmt.Sprintf("incomplete: got %d of %d bytes", n, size), http.StatusBadRequest)
		}
		return
	}
	rel, err := s.place(part, segs, nil, 0)
	if err != nil {
		_ = os.Remove(part)
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sess.filesDone.Store(1)
	s.log.Printf("saved %s (%s) from %s", rel, fsutil.HumanBytes(size), ip)
	e := s.sessEvent("saved", sess)
	e.Name, e.Bytes = rel, size
	s.emit(e)
	s.emit(s.sessEvent("finished", sess))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "saved: %s\n", rel)
}

// ---------- offer ----------

func (s *Server) handleOffer(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if !s.checkPIN(w, r, ip, false) {
		return
	}
	var off proto.Offer
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, proto.MaxOfferBytes))
	if err := dec.Decode(&off); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_offer", err.Error())
		return
	}
	if !hex32.MatchString(off.TransferID) || len(off.Files)+len(off.Dirs) == 0 ||
		len(off.Files) > proto.MaxOfferFiles || len(off.Dirs) > proto.MaxOfferFiles {
		writeErr(w, http.StatusBadRequest, "bad_offer", "invalid transfer_id or file list")
		return
	}
	var total, maxFile int64
	paths := make([][]string, 0, len(off.Files))
	for i, fe := range off.Files {
		if fe.FID != i || fe.Size < 0 {
			writeErr(w, http.StatusBadRequest, "bad_offer", "fid must equal index and size >= 0")
			return
		}
		segs, err := fsutil.SanitizeRelPath(fe.Path)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_offer", "empty path")
			return
		}
		paths = append(paths, segs)
		total += fe.Size
		maxFile = max(maxFile, fe.Size)
	}
	clean := fsutil.DedupeCaseInsensitive(paths)
	var dirs [][]string
	for _, d := range off.Dirs {
		if segs, err := fsutil.SanitizeRelPath(d); err == nil {
			dirs = append(dirs, segs)
		}
	}

	// 续传判断
	tdir := filepath.Join(s.stateDir(), off.TransferID)
	resume := false
	top, done := map[string]string{}, map[int]string{}
	if m, t, d, err := loadSession(tdir); err == nil && sameFiles(m.Files, off.Files) {
		resume, top, done = true, t, d
	}

	var already int64
	if resume {
		for i, fe := range off.Files {
			if _, ok := done[i]; ok {
				already += fe.Size
			} else {
				already += min(partSize(partPath(tdir, i)), fe.Size)
			}
		}
	}
	if err := fsutil.CheckSpace(s.cfg.RecvDir, total-already, maxFile); err != nil {
		writeErr(w, http.StatusInsufficientStorage, "no_space", err.Error())
		return
	}

	names := make([]string, 0, 5)
	sizes := make([]int64, 0, 5)
	for i := 0; i < len(clean) && i < 5; i++ {
		names = append(names, strings.Join(clean[i], "/"))
		sizes = append(sizes, off.Files[i].Size)
	}
	if len(clean) == 0 {
		for i := 0; i < len(dirs) && i < 5; i++ {
			names = append(names, strings.Join(dirs[i], "/")+"/")
			sizes = append(sizes, -1)
		}
	}
	req := Request{Kind: "offer", RemoteIP: ip, SenderName: off.SenderName, FileCount: len(off.Files),
		TotalSize: total, Names: names, Sizes: sizes, Resume: resume, ResumedBytes: already}
	if !s.approve(w, r, req, false) {
		return
	}

	// 同一 transfer 若有旧会话（对方断线重发），先让旧会话停下
	s.mu.Lock()
	old := s.byTID[off.TransferID]
	s.mu.Unlock()
	if old != nil {
		old.cancelled.Store(true)
		for _, f := range old.files { // 等旧连接释放文件锁
			f.mu.Lock()
			f.mu.Unlock() //nolint:staticcheck
		}
		old.closeJournal()
		s.mu.Lock()
		delete(s.sessions, old.sid)
		s.mu.Unlock()
	}

	if !resume {
		_ = os.RemoveAll(tdir)
	}
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "io", err.Error())
		return
	}

	sess := &session{
		sid: randHex(16), ip: ip, sender: off.SenderName, dir: tdir, total: total,
		meta: sessionMeta{TransferID: off.TransferID, SenderIP: ip, SenderName: off.SenderName,
			Files: off.Files, Clean: clean, Dirs: dirs},
		top: top, done: done,
	}
	sess.touch()
	resp := proto.OfferResp{SID: sess.sid, Resume: resume}
	for i, fe := range off.Files {
		var reuse *fileState
		if old != nil && resume && i < len(old.files) {
			reuse = old.files[i]
		}
		fs := &fileState{entry: fe, segs: clean[i], part: partPath(tdir, i), hashedAt: -1}
		if _, ok := done[i]; ok {
			fs.done = true
			fs.received.Store(fe.Size)
			sess.filesDone.Add(1)
		} else {
			n := partSize(fs.part)
			if n > fe.Size { // 异常：比声明还大，丢弃
				_ = os.Remove(fs.part)
				n = 0
			}
			fs.received.Store(n)
			if reuse != nil && reuse.hashedAt == n { // 复用内存中的哈希状态，免去重新哈希
				fs.hasher, fs.hashedAt = reuse.hasher, reuse.hashedAt
			}
		}
		sess.bytes.Add(fs.received.Load())
		sess.files = append(sess.files, fs)
		resp.Files = append(resp.Files, proto.FileOffset{FID: i, Offset: fs.received.Load(), Done: fs.done})
	}
	if err := sess.writeMeta(!resume); err != nil {
		writeErr(w, http.StatusInternalServerError, "io", err.Error())
		return
	}
	s.mu.Lock()
	s.sessions[sess.sid] = sess
	s.byTID[off.TransferID] = sess
	s.mu.Unlock()

	ev := s.sessEvent("accepted", sess)
	ev.Resume = resume
	s.emit(ev)
	writeJSON(w, http.StatusOK, resp)
}

// lookup 校验 sid（含来源 IP 绑定）和 fid。被取消的会话返回 410。
func (s *Server) lookup(w http.ResponseWriter, r *http.Request, needFID bool) (*session, *fileState, bool) {
	s.mu.Lock()
	sess := s.sessions[r.PathValue("sid")]
	s.mu.Unlock()
	if sess == nil || sess.curl || sess.ip != remoteIP(r) {
		writeErr(w, http.StatusNotFound, "no_session", "unknown session")
		return nil, nil, false
	}
	if sess.cancelled.Load() {
		writeErr(w, http.StatusGone, "cancelled", "transfer cancelled")
		return nil, nil, false
	}
	sess.touch()
	if !needFID {
		return sess, nil, true
	}
	fid, err := strconv.Atoi(r.PathValue("fid"))
	if err != nil || fid < 0 || fid >= len(sess.files) {
		writeErr(w, http.StatusNotFound, "no_file", "unknown fid")
		return nil, nil, false
	}
	return sess, sess.files[fid], true
}

// ---------- status ----------

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	_, f, ok := s.lookup(w, r, true)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, proto.Status{Received: f.received.Load(), Size: f.entry.Size, Done: f.done})
}

// ---------- data ----------

func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	sess, f, ok := s.lookup(w, r, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeErr(w, http.StatusBadRequest, "bad_offset", "offset required")
		return
	}
	commitHash := q.Get("commit") // 可选：小文件一次请求完成数据+校验
	if commitHash != "" && !hex32.MatchString(commitHash) {
		writeErr(w, http.StatusBadRequest, "bad_commit", "invalid xxh3")
		return
	}
	if !f.mu.TryLock() {
		writeErr(w, http.StatusConflict, "busy", "file is being written by another request")
		return
	}
	defer f.mu.Unlock()
	if f.done {
		writeErr(w, http.StatusConflict, "done", "file already committed")
		return
	}
	cur := partSize(f.part)
	if delta := cur - f.received.Load(); delta != 0 {
		sess.bytes.Add(delta)
		f.received.Store(cur)
	}
	if offset != cur {
		writeJSON(w, http.StatusConflict, proto.Error{Error: "offset_mismatch", Received: &cur})
		return
	}
	name := strings.Join(f.segs, "/")
	if f.hasher == nil || f.hashedAt != cur {
		err := f.rehash(cur, func(done int64) {
			e := s.sessEvent("rehash", sess)
			e.Name, e.Bytes, e.Total = name, done, cur
			s.emit(e)
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "io", err.Error())
			return
		}
	}
	fh, err := os.OpenFile(f.part, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "io", err.Error())
		return
	}
	if _, err := fh.Seek(cur, io.SeekStart); err != nil {
		fh.Close()
		writeErr(w, http.StatusInternalServerError, "io", err.Error())
		return
	}

	_, overflow, cancelled, werr, rerr := copyBody(fh, f.hasher, r.Body, f.entry.Size-cur, sess.cancelled.Load, func(k int64) {
		f.hashedAt += k
		f.received.Add(k)
		sess.bytes.Add(k)
		sess.touch()
		if sess.shouldEmit() {
			e := s.sessEvent("progress", sess)
			e.Name, e.Bytes, e.Total = name, f.received.Load(), f.entry.Size
			s.emit(e)
		}
	})
	if cerr := fh.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		// 写失败时磁盘内容与 hasher 可能不一致，作废 hasher，下次重新哈希
		f.hasher, f.hashedAt = nil, -1
		writeErr(w, http.StatusInternalServerError, "io", werr.Error())
		return
	}
	got := f.received.Load()
	switch {
	case cancelled:
		writeErr(w, http.StatusGone, "cancelled", "transfer cancelled")
		return
	case rerr != nil:
		s.log.Printf("data from %s interrupted at %d/%d: %v", sess.ip, got, f.entry.Size, rerr)
		e := s.sessEvent("interrupted", sess)
		e.Err = rerr
		s.emit(e)
		return // 连接已断，无需响应
	case overflow:
		writeErr(w, http.StatusBadRequest, "too_long", "body exceeds declared size")
		return
	}
	if commitHash != "" && got == f.entry.Size {
		s.commitLocked(w, sess, f, commitHash)
		return
	}
	writeJSON(w, http.StatusOK, proto.DataResp{Received: got})
}

// ---------- commit ----------

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	sess, f, ok := s.lookup(w, r, true)
	if !ok {
		return
	}
	var req proto.CommitReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || !hex32.MatchString(req.XXH3) {
		writeErr(w, http.StatusBadRequest, "bad_commit", "xxh3 required")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s.commitLocked(w, sess, f, req.XXH3)
}

// commitLocked 校验哈希并把 part 改名为正式文件。调用方需持有 f.mu。
func (s *Server) commitLocked(w http.ResponseWriter, sess *session, f *fileState, want string) {
	if f.done {
		sess.mu.Lock()
		name := sess.done[f.entry.FID]
		sess.mu.Unlock()
		writeJSON(w, http.StatusOK, proto.CommitResp{Name: name, Received: f.entry.Size})
		return
	}
	size := f.entry.Size
	if size == 0 {
		if fh, err := os.OpenFile(f.part, os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
			fh.Close()
		}
	}
	cur := partSize(f.part)
	if cur != size {
		writeJSON(w, http.StatusConflict, proto.Error{Error: "incomplete", Received: &cur})
		return
	}
	if f.hasher == nil || f.hashedAt != size {
		if err := f.rehash(size, nil); err != nil {
			writeErr(w, http.StatusInternalServerError, "io", err.Error())
			return
		}
	}
	if got := proto.HashHex(f.hasher.Sum128()); got != want {
		s.log.Printf("hash mismatch for %s: got %s want %s", strings.Join(f.segs, "/"), got, want)
		_ = os.Remove(f.part)
		f.hasher, f.hashedAt = nil, -1
		sess.bytes.Add(-f.received.Load())
		f.received.Store(0)
		writeErr(w, http.StatusUnprocessableEntity, "hash_mismatch", "xxh3 mismatch, resend from 0")
		return
	}
	sess.mu.Lock()
	rel, err := s.place(f.part, f.segs, sess, f.entry.Mtime)
	if err == nil {
		sess.done[f.entry.FID] = rel
		sess.appendJournal(journalRec{T: "done", FID: f.entry.FID, V: rel})
	}
	sess.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "io", err.Error())
		return
	}
	f.done = true
	f.hasher = nil
	sess.filesDone.Add(1)
	e := s.sessEvent("saved", sess)
	e.Name, e.Bytes = rel, size
	s.emit(e)
	writeJSON(w, http.StatusOK, proto.CommitResp{Name: rel, Received: size})
}

// ---------- finish / cancel ----------

func (s *Server) handleFinish(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := s.lookup(w, r, false)
	if !ok {
		return
	}
	sess.mu.Lock()
	for _, d := range sess.meta.Dirs {
		mapped, err := s.mapTop(d[0], sess)
		if err != nil {
			continue
		}
		_ = os.MkdirAll(filepath.Join(append([]string{s.cfg.RecvDir, mapped}, d[1:]...)...), 0o755)
	}
	allDone := true
	saved := make([]string, 0, len(sess.files))
	for i := range sess.files {
		if name, ok := sess.done[i]; ok {
			saved = append(saved, name)
		} else {
			allDone = false
		}
	}
	sess.mu.Unlock()
	if allDone {
		s.mu.Lock()
		delete(s.sessions, sess.sid)
		if s.byTID[sess.meta.TransferID] == sess {
			delete(s.byTID, sess.meta.TransferID)
		}
		s.mu.Unlock()
		sess.closeJournal()
		_ = os.RemoveAll(sess.dir)
		s.log.Printf("finished %d files (%s) from %s", len(saved), fsutil.HumanBytes(sess.total), sess.ip)
		s.emit(s.sessEvent("finished", sess))
	}
	writeJSON(w, http.StatusOK, proto.FinishResp{Saved: saved})
}

// handleCancel：发送方主动取消。已收数据保留，可续传。
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := s.lookup(w, r, false)
	if !ok {
		return
	}
	if sess.cancelled.CompareAndSwap(false, true) {
		s.log.Printf("sender cancelled session %s from %s", sess.sid, sess.ip)
		e := s.sessEvent("cancelled", sess)
		e.Name = "sender"
		s.emit(e)
	}
	writeJSON(w, http.StatusOK, struct{}{})
}
