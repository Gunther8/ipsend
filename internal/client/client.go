// Package client 实现 ipsend 发送端（见 SPEC.md §3/§4）。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zeebo/xxh3"

	"ipsend/internal/proto"
)

// Progress 是发送进度快照。
type Progress struct {
	Phase     string // "waiting" "hashing" "sending" "done"
	File      string // 当前（最近）处理的文件
	FileIndex int
	FileCount int
	FilesDone int   // 已完成的文件数
	Sent      int64 // 已发出的总字节（含续传跳过部分）
	Total     int64
	Hashed    int64 // Phase=="hashing" 时：当前文件已校验字节
	HashTotal int64 // Phase=="hashing" 时：需要校验的字节（续传点）
	Resumed   bool  // 本次是续传
}

type Options struct {
	SenderName string
	PIN        string         // 对方要求 PIN 时填写
	OnProgress func(Progress) // 可为空；调用频率约 5 次/秒
}

const pinHeader = "X-Ipsend-Pin"

// PIN 相关错误。
var (
	ErrPINRequired = errors.New("对方设置了 PIN，请输入 PIN")
	ErrBadPIN      = errors.New("PIN 不正确")
	ErrPINLocked   = errors.New("PIN 输错次数过多，请 10 分钟后再试")
)

// Result 是一次发送的结果。
type Result struct {
	Saved    []string
	Resumed  bool
	PeerName string // 对方机器名（来自 ping）
}

// ErrRejected 表示对方拒绝或超时未确认。
var ErrRejected = errors.New("对方拒绝了接收（或 60 秒内未确认）")

// ErrPeerCancelled 表示接收方取消了本次接收。
var ErrPeerCancelled = errors.New("对方取消了接收（已传部分会保留，重新发送可续传）")

// smallFileMax 以下的文件整个读入内存，先算哈希，一次请求完成数据+校验，并发发送。
const (
	smallFileMax  = 4 << 20
	smallWorkers  = 4
	cancelTimeout = 2 * time.Second
)

type localFile struct {
	abs string
	proto.FileEntry
}

// NormalizeAddr 把 "1.2.3.4" / "1.2.3.4:5" / "::1" / "[::1]:5" 统一为 host:port。
func NormalizeAddr(addr string, defPort int) string {
	addr = strings.TrimSpace(addr)
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	addr = strings.Trim(addr, "[]")
	return net.JoinHostPort(addr, strconv.Itoa(defPort))
}

// Collect 遍历用户选择的文件/文件夹，生成清单。文件夹以其名字为根，保留相对路径；跳过符号链接等特殊文件。
func Collect(paths []string) ([]localFile, []string, error) {
	var files []localFile
	var dirs []string
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, nil, err
		}
		st, err := os.Lstat(abs)
		if err != nil {
			return nil, nil, err
		}
		base := filepath.Base(abs)
		switch {
		case st.Mode().IsRegular():
			files = append(files, localFile{abs: abs, FileEntry: proto.FileEntry{Path: base, Size: st.Size(), Mtime: st.ModTime().Unix()}})
		case st.IsDir():
			err := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(abs, path)
				relSlash := filepath.ToSlash(filepath.Join(base, rel))
				if d.IsDir() {
					ents, err := os.ReadDir(path)
					if err == nil && len(ents) == 0 {
						dirs = append(dirs, relSlash)
					}
					return nil
				}
				if !d.Type().IsRegular() {
					return nil // 符号链接、junction 等
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				files = append(files, localFile{abs: path, FileEntry: proto.FileEntry{Path: relSlash, Size: info.Size(), Mtime: info.ModTime().Unix()}})
				return nil
			})
			if err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("不支持的文件类型: %s", p)
		}
	}
	for i := range files {
		files[i].FID = i
	}
	return files, dirs, nil
}

// TransferID：同一批文件（路径+大小+修改时间不变）得到同一 ID，接收方据此续传。
func TransferID(files []localFile, dirs []string) string {
	keys := make([]string, 0, len(files)+len(dirs))
	for _, f := range files {
		keys = append(keys, fmt.Sprintf("f\x00%s\x00%d\x00%d", f.abs, f.Size, f.Mtime))
	}
	for _, d := range dirs {
		keys = append(keys, "d\x00"+d)
	}
	sort.Strings(keys)
	return proto.HashHex(xxh3.Hash128([]byte(strings.Join(keys, "\n"))))
}

type Client struct {
	base string
	http *http.Client
	opt  Options
}

func New(addr string, opt Options) *Client {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 4,
		WriteBufferSize:     256 << 10,
		ReadBufferSize:      64 << 10,
		DisableCompression:  true,
	}
	return &Client{
		base: "http://" + NormalizeAddr(addr, proto.DefaultPort),
		http: &http.Client{Transport: tr},
		opt:  opt,
	}
}

// Ping 检查对方是否在线，并把常见网络错误翻译成中文。
func (c *Client) Ping(ctx context.Context) (*proto.Ping, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var p proto.Ping
	if err := c.doJSON(ctx, "GET", "/api/ping", nil, &p); err != nil {
		return nil, explainNetErr(err)
	}
	if p.App != proto.AppName {
		return nil, errors.New("对方端口上运行的不是 ipsend")
	}
	if p.Proto != proto.ProtoVer {
		return nil, fmt.Errorf("协议版本不兼容：对方 %d，本机 %d，请升级到相同版本", p.Proto, proto.ProtoVer)
	}
	return &p, nil
}

func explainNetErr(err error) error {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "actively refused"):
		return fmt.Errorf("连接被拒绝：对方未运行 ipsend 或端口不对 (%w)", err)
	case errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("连接超时：IP 不可达，或被对方防火墙拦截 (%w)", err)
	}
	return err
}

type httpError struct {
	Code int
	Body proto.Error
}

func (e *httpError) Error() string {
	if e.Body.Message != "" {
		return fmt.Sprintf("HTTP %d %s: %s", e.Code, e.Body.Error, e.Body.Message)
	}
	return fmt.Sprintf("HTTP %d %s", e.Code, e.Body.Error)
}

func (c *Client) setPIN(req *http.Request) {
	if c.opt.PIN != "" {
		req.Header.Set(pinHeader, c.opt.PIN)
	}
}

// pinError 把 401/429 的 PIN 错误码映射为对应错误；不是 PIN 错误时返回 nil。
func pinError(err error) error {
	var he *httpError
	if !errors.As(err, &he) {
		return nil
	}
	switch he.Body.Error {
	case "pin_required":
		return ErrPINRequired
	case "bad_pin":
		return ErrBadPIN
	case "pin_locked":
		return ErrPINLocked
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setPIN(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeResp(resp, out)
}

func decodeResp(resp *http.Response, out any) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		he := &httpError{Code: resp.StatusCode}
		_ = json.Unmarshal(b, &he.Body)
		return he
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
