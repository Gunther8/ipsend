package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/zeebo/xxh3"

	"ipsend/internal/proto"
)

// Send 发送 paths（文件或文件夹）。ctx 取消时会通知对方并返回 context.Canceled。
func (c *Client) Send(ctx context.Context, paths []string) (*Result, error) {
	ping, err := c.Ping(ctx)
	if err != nil {
		return nil, err
	}
	if ping.PIN && c.opt.PIN == "" {
		return nil, ErrPINRequired
	}
	files, dirs, err := Collect(paths)
	if err != nil {
		return nil, err
	}
	if len(files)+len(dirs) == 0 {
		return nil, errors.New("没有可发送的文件")
	}
	var total int64
	entries := make([]proto.FileEntry, len(files))
	for i, f := range files {
		entries[i] = f.FileEntry
		total += f.Size
	}
	offer := proto.Offer{TransferID: TransferID(files, dirs), SenderName: c.opt.SenderName, Files: entries, Dirs: dirs}

	prog := &progress{cb: c.opt.OnProgress, p: Progress{Phase: "waiting", FileCount: len(files), Total: total}}
	prog.emit(true)

	var or proto.OfferResp
	if err := c.doJSON(ctx, "POST", "/api/offer", offer, &or); err != nil {
		if pe := pinError(err); pe != nil {
			return nil, pe
		}
		var he *httpError
		if errors.As(err, &he) {
			switch he.Code {
			case http.StatusForbidden:
				return nil, ErrRejected
			case http.StatusInsufficientStorage:
				return nil, fmt.Errorf("对方磁盘空间不足：%s", he.Body.Message)
			case http.StatusServiceUnavailable, http.StatusTooManyRequests:
				return nil, fmt.Errorf("对方正忙（待确认的请求太多），请稍后再试")
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, explainNetErr(err)
	}

	offsets := make([]int64, len(files))
	done := make([]bool, len(files))
	for _, fo := range or.Files {
		if fo.FID >= 0 && fo.FID < len(files) {
			offsets[fo.FID], done[fo.FID] = fo.Offset, fo.Done
		}
	}
	prog.mu.Lock()
	prog.p.Resumed = or.Resume
	prog.p.Phase = "sending"
	for i, f := range files { // 进度从已收部分开始计
		if done[i] {
			prog.p.Sent += f.Size
			prog.p.FilesDone++
		} else {
			prog.p.Sent += offsets[i]
		}
	}
	prog.mu.Unlock()

	res := &Result{Resumed: or.Resume, PeerName: ping.Name}
	err = c.sendAll(ctx, or.SID, files, offsets, done, prog)
	if err != nil {
		if ctx.Err() != nil {
			c.notifyCancel(or.SID)
			return res, ctx.Err()
		}
		return res, err
	}
	var fr proto.FinishResp
	if err := c.doJSON(ctx, "POST", "/api/finish/"+or.SID, nil, &fr); err != nil {
		return res, mapErr(err)
	}
	res.Saved = fr.Saved
	prog.mu.Lock()
	prog.p.Phase = "done"
	prog.mu.Unlock()
	prog.emit(true)
	return res, nil
}

// notifyCancel 尽力通知对方本次发送已取消（不影响返回值）。
func (c *Client) notifyCancel(sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	_ = c.doJSON(ctx, "POST", "/api/cancel/"+sid, nil, nil)
}

// mapErr 把对方的 410 映射为 ErrPeerCancelled。
func mapErr(err error) error {
	var he *httpError
	if errors.As(err, &he) && he.Code == http.StatusGone {
		return ErrPeerCancelled
	}
	return err
}

// sendAll：小文件并发、一次请求完成；大文件逐个流式发送。
func (c *Client) sendAll(ctx context.Context, sid string, files []localFile, offsets []int64, done []bool, prog *progress) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var small, large []int
	for i, f := range files {
		switch {
		case done[i]:
		case f.Size <= smallFileMax && offsets[i] == 0:
			small = append(small, i)
		default:
			large = append(large, i)
		}
	}

	if len(small) > 0 {
		jobs := make(chan int)
		var wg sync.WaitGroup
		var firstErr error
		var errOnce sync.Once
		fail := func(err error) {
			errOnce.Do(func() { firstErr = err; cancel() })
		}
		for w := 0; w < min(smallWorkers, len(small)); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					if err := c.sendSmall(ctx, sid, files[i], prog); err != nil {
						fail(fmt.Errorf("%s: %w", files[i].Path, err))
					}
				}
			}()
		}
	feed:
		for _, i := range small {
			select {
			case jobs <- i:
			case <-ctx.Done():
				break feed
			}
		}
		close(jobs)
		wg.Wait()
		if firstErr != nil {
			return unwrapCancel(firstErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	for _, i := range large {
		if err := c.sendFile(ctx, sid, files[i], offsets[i], prog); err != nil {
			return unwrapCancel(fmt.Errorf("%s: %w", files[i].Path, err))
		}
	}
	return nil
}

func unwrapCancel(err error) error {
	if errors.Is(err, ErrPeerCancelled) {
		return ErrPeerCancelled
	}
	return err
}

// sendSmall 读入整个小文件，先算哈希，PUT 时带 commit 参数一次完成。
func (c *Client) sendSmall(ctx context.Context, sid string, f localFile, prog *progress) error {
	data, err := os.ReadFile(f.abs)
	if err != nil {
		return err
	}
	if int64(len(data)) != f.Size {
		return fmt.Errorf("文件大小在发送前后不一致（%d → %d），请重新发送", f.Size, len(data))
	}
	sum := proto.HashHex(xxh3.Hash128(data))
	prog.start(f)
	url := c.base + "/api/data/" + sid + "/" + strconv.Itoa(f.FID) + "?offset=0&commit=" + sum
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.ContentLength = int64(len(data))
		c.setPIN(req)
		resp, err := c.http.Do(req)
		if err == nil {
			var cr proto.CommitResp
			err = decodeResp(resp, &cr)
			resp.Body.Close()
			if err == nil {
				prog.fileDone(f.Size)
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var he *httpError
		if errors.As(err, &he) {
			switch {
			case he.Code == http.StatusGone:
				return ErrPeerCancelled
			case he.Code == http.StatusConflict && he.Body.Error == "done":
				prog.fileDone(f.Size)
				return nil
			case he.Code == http.StatusConflict && he.Body.Error == "offset_mismatch" && he.Body.Received != nil:
				// 上次请求其实写进去了一部分：退回流式发送，从对方的进度继续
				r := *he.Body.Received
				prog.addSent(r)
				return c.sendFile(ctx, sid, f, r, prog)
			case he.Code != http.StatusConflict && he.Code != http.StatusUnprocessableEntity:
				return err
			}
		}
		if attempt >= maxRetries {
			return fmt.Errorf("重试次数过多，放弃: %w", err)
		}
		debugf("retry small %s: %v", f.Path, err)
		if err := sleepCtx(ctx, backoff(attempt)); err != nil {
			return err
		}
	}
}

const maxRetries = 8

// debugf 在设置了环境变量 IPSEND_DEBUG 时输出重试原因。
func debugf(format string, args ...any) {
	if os.Getenv("IPSEND_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[client] "+format+"\n", args...)
	}
}

func backoff(attempt int) time.Duration {
	return time.Duration(min((attempt+1)*(attempt+1), 10)) * time.Second
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// sendFile 流式发送单个文件，网络中断时查询 status 后自动续传。
func (c *Client) sendFile(ctx context.Context, sid string, f localFile, offset int64, prog *progress) error {
	fid := strconv.Itoa(f.FID)
	prog.start(f)
	acked := offset // 已计入进度的字节（相对本文件）
	hashRestarts := 0
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if attempt > maxRetries {
				return errors.New("重试次数过多，放弃")
			}
			if err := sleepCtx(ctx, backoff(attempt-1)); err != nil {
				return err
			}
			var st proto.Status
			if err := c.doJSON(ctx, "GET", "/api/status/"+sid+"/"+fid, nil, &st); err != nil {
				if errors.Is(mapErr(err), ErrPeerCancelled) {
					return ErrPeerCancelled
				}
				continue
			}
			if st.Done {
				prog.addSent(f.Size - acked)
				prog.fileDone(0)
				return nil
			}
			prog.addSent(st.Received - acked)
			acked, offset = st.Received, st.Received
		}

		sum, sent, err := c.pushFrom(ctx, sid, f, offset, prog)
		acked += sent
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(mapErr(err), ErrPeerCancelled) {
				return ErrPeerCancelled
			}
			var he *httpError
			if errors.As(err, &he) && he.Code != http.StatusConflict {
				return err
			}
			debugf("retry %s at %d: %v", f.Path, offset, err)
			continue // 网络错误 / 409：查询状态后重试
		}

		var cr proto.CommitResp
		err = c.doJSON(ctx, "POST", "/api/commit/"+sid+"/"+fid, proto.CommitReq{XXH3: sum}, &cr)
		if err == nil {
			prog.fileDone(0)
			return nil
		}
		if errors.Is(mapErr(err), ErrPeerCancelled) {
			return ErrPeerCancelled
		}
		var he *httpError
		if errors.As(err, &he) && he.Code == http.StatusUnprocessableEntity {
			// 哈希不符：对方已删除 part，从 0 重传一次
			if hashRestarts++; hashRestarts > 1 {
				return errors.New("哈希校验多次失败（文件在发送过程中被修改？）")
			}
			prog.addSent(-acked)
			acked, offset = 0, 0
			attempt = -1 // 下一轮不等待、不查状态，直接从 0 发
			continue
		}
		if errors.As(err, &he) && he.Code != http.StatusConflict {
			return err
		}
	}
}

// pushFrom 先对 [0,offset) 计算哈希，再 PUT 剩余部分，返回整个文件的 xxh3-128 和本次被对方确认的字节数。
func (c *Client) pushFrom(ctx context.Context, sid string, f localFile, offset int64, prog *progress) (string, int64, error) {
	fh, err := os.Open(f.abs)
	if err != nil {
		return "", 0, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return "", 0, err
	}
	if st.Size() != f.Size {
		return "", 0, fmt.Errorf("文件大小在发送前后不一致（%d → %d），请重新发送", f.Size, st.Size())
	}

	h := xxh3.New()
	if offset > 0 {
		hr := &countReader{r: &ctxReader{ctx: ctx, r: io.NewSectionReader(fh, 0, offset)}, onRead: func(n int64) { prog.hashing(n, offset) }}
		prog.hashing(0, offset)
		if _, err := io.CopyBuffer(h, hr, make([]byte, 1<<20)); err != nil {
			return "", 0, err
		}
		prog.hashDone()
	}

	if offset >= f.Size {
		return proto.HashHex(h.Sum128()), 0, nil
	}
	remain := f.Size - offset
	body := io.TeeReader(io.NewSectionReader(fh, offset, remain), h)
	pr := &countReader{r: throttle(body), onRead: prog.addSent}
	req, err := http.NewRequestWithContext(ctx, "PUT",
		c.base+"/api/data/"+sid+"/"+strconv.Itoa(f.FID)+"?offset="+strconv.FormatInt(offset, 10), pr)
	if err != nil {
		return "", 0, err
	}
	req.ContentLength = remain
	req.Header.Set("Content-Type", "application/octet-stream")
	c.setPIN(req)
	resp, err := c.http.Do(req)
	if err != nil {
		prog.addSent(-pr.n) // 未确认的部分回滚，重试时以 status 为准
		return "", 0, err
	}
	var dr proto.DataResp
	err = decodeResp(resp, &dr)
	resp.Body.Close()
	if err != nil {
		prog.addSent(-pr.n)
		return "", 0, err
	}
	if dr.Received != f.Size {
		prog.addSent(-pr.n)
		return "", 0, &httpError{Code: http.StatusConflict, Body: proto.Error{Error: "short"}}
	}
	return proto.HashHex(h.Sum128()), pr.n, nil
}

// throttle：设置环境变量 IPSEND_RATE_MBPS 时限速（调试界面进度用）。
func throttle(r io.Reader) io.Reader {
	mbps, err := strconv.ParseFloat(os.Getenv("IPSEND_RATE_MBPS"), 64)
	if err != nil || mbps <= 0 {
		return r
	}
	return &rateReader{r: r, bps: mbps * (1 << 20), start: time.Now()}
}

type rateReader struct {
	r     io.Reader
	bps   float64
	n     int64
	start time.Time
}

func (t *rateReader) Read(p []byte) (int, error) {
	if len(p) > 256<<10 {
		p = p[:256<<10]
	}
	n, err := t.r.Read(p)
	t.n += int64(n)
	if want := time.Duration(float64(t.n) / t.bps * float64(time.Second)); want > time.Since(t.start) {
		time.Sleep(want - time.Since(t.start))
	}
	return n, err
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

type countReader struct {
	r      io.Reader
	n      int64
	onRead func(int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		c.onRead(int64(n))
	}
	return n, err
}

// progress 汇总多个 goroutine 的进度，节流后回调。
type progress struct {
	mu   sync.Mutex
	cb   func(Progress)
	p    Progress
	last time.Time
}

func (p *progress) start(f localFile) {
	p.mu.Lock()
	p.p.File, p.p.FileIndex = f.Path, f.FID
	p.mu.Unlock()
	p.emit(false)
}

func (p *progress) addSent(n int64) {
	p.mu.Lock()
	p.p.Sent += n
	p.mu.Unlock()
	p.emit(false)
}

func (p *progress) fileDone(n int64) {
	p.mu.Lock()
	p.p.Sent += n
	p.p.FilesDone++
	p.mu.Unlock()
	p.emit(false)
}

func (p *progress) hashing(n, total int64) {
	p.mu.Lock()
	p.p.Phase = "hashing"
	p.p.Hashed += n
	if n == 0 {
		p.p.Hashed = 0
	}
	p.p.HashTotal = total
	p.mu.Unlock()
	p.emit(n == 0)
}

func (p *progress) hashDone() {
	p.mu.Lock()
	p.p.Phase = "sending"
	p.p.Hashed, p.p.HashTotal = 0, 0
	p.mu.Unlock()
	p.emit(true)
}

func (p *progress) emit(force bool) {
	if p.cb == nil {
		return
	}
	p.mu.Lock()
	if !force && time.Since(p.last) < 200*time.Millisecond {
		p.mu.Unlock()
		return
	}
	p.last = time.Now()
	snap := p.p
	p.mu.Unlock()
	p.cb(snap)
}
