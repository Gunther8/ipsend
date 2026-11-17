package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zeebo/xxh3"

	"ipsend/internal/client"
	"ipsend/internal/proto"
	"ipsend/internal/server"
)

type funcApprover func(server.Request) server.Decision

func (f funcApprover) Approve(_ context.Context, r server.Request) server.Decision { return f(r) }

func start(t *testing.T, ap server.Approver) (addr, recv string) {
	t.Helper()
	recv = t.TempDir()
	var lw io.Writer = io.Discard
	if os.Getenv("IPSEND_LOG") != "" {
		lw = os.Stderr
	}
	srv, err := server.New(server.Config{RecvDir: recv, Name: "test", Approver: ap,
		Logger: log.New(lw, "[srv] ", log.Lmicroseconds)})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	return ln.Addr().String(), recv
}

func writeRand(t *testing.T, p string, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func mustEqualFile(t *testing.T, p string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s content mismatch (%d vs %d bytes)", p, len(got), len(want))
	}
}

func TestFolderAndRename(t *testing.T) {
	addr, recv := start(t, server.AutoApprover{})
	src := t.TempDir()
	a := writeRand(t, filepath.Join(src, "照片", "2024", "a 1.bin"), 3<<20+7)
	b := writeRand(t, filepath.Join(src, "照片", "b.txt"), 0)
	os.MkdirAll(filepath.Join(src, "照片", "空文件夹"), 0o755)
	single := writeRand(t, filepath.Join(src, "single.dat"), 1234)

	c := client.New(addr, client.Options{SenderName: "t"})
	for round := 0; round < 2; round++ {
		res, err := c.Send(context.Background(), []string{filepath.Join(src, "照片"), filepath.Join(src, "single.dat")})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("round %d saved %v", round, res.Saved)
	}
	mustEqualFile(t, filepath.Join(recv, "照片", "2024", "a 1.bin"), a)
	mustEqualFile(t, filepath.Join(recv, "照片", "b.txt"), b)
	mustEqualFile(t, filepath.Join(recv, "single.dat"), single)
	if st, err := os.Stat(filepath.Join(recv, "照片", "空文件夹")); err != nil || !st.IsDir() {
		t.Fatal("empty dir missing")
	}
	// 第二轮：顶层目录改名、单文件改名，不覆盖
	mustEqualFile(t, filepath.Join(recv, "照片 (1)", "2024", "a 1.bin"), a)
	mustEqualFile(t, filepath.Join(recv, "single (1).dat"), single)
	if _, err := os.Stat(filepath.Join(recv, "照片 (1)", "空文件夹")); err != nil {
		t.Fatal("empty dir missing in round 2")
	}
	// mtime 保留
	si, _ := os.Stat(filepath.Join(src, "single.dat"))
	ri, _ := os.Stat(filepath.Join(recv, "single.dat"))
	if si.ModTime().Unix() != ri.ModTime().Unix() {
		t.Fatal("mtime not preserved")
	}
	// 会话目录已清理
	ents, _ := os.ReadDir(filepath.Join(recv, ".ipsend"))
	if len(ents) != 0 {
		t.Fatalf("state dir not cleaned: %d entries", len(ents))
	}
}

func TestReject(t *testing.T) {
	addr, _ := start(t, funcApprover(func(server.Request) server.Decision { return server.Reject }))
	src := t.TempDir()
	writeRand(t, filepath.Join(src, "x"), 10)
	_, err := client.New(addr, client.Options{}).Send(context.Background(), []string{filepath.Join(src, "x")})
	if !errors.Is(err, client.ErrRejected) {
		t.Fatalf("want ErrRejected, got %v", err)
	}
}

func TestNoSpace(t *testing.T) {
	addr, _ := start(t, server.AutoApprover{})
	off := proto.Offer{TransferID: strings.Repeat("a", 32), Files: []proto.FileEntry{{FID: 0, Path: "huge", Size: 1 << 60}}}
	code, _ := postJSON(t, "http://"+addr+"/api/offer", off)
	if code != http.StatusInsufficientStorage {
		t.Fatalf("want 507, got %d", code)
	}
}

func postJSON(t *testing.T, url string, v any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(v)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func put(t *testing.T, url string, data []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(data))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// 模拟“发了一半断开”，再由客户端重新发送同一文件 → 续传。
func TestResume(t *testing.T) {
	var lastReq server.Request
	addr, recv := start(t, funcApprover(func(r server.Request) server.Decision { lastReq = r; return server.Accept }))
	src := t.TempDir()
	p := filepath.Join(src, "big.bin")
	data := writeRand(t, p, 8<<20+123)

	files, dirs, err := client.Collect([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	off := proto.Offer{TransferID: client.TransferID(files, dirs), SenderName: "t",
		Files: []proto.FileEntry{{FID: 0, Path: "big.bin", Size: files[0].Size, Mtime: files[0].Mtime}}}
	code, body := postJSON(t, "http://"+addr+"/api/offer", off)
	if code != 200 {
		t.Fatalf("offer %d %s", code, body)
	}
	var or proto.OfferResp
	json.Unmarshal(body, &or)
	half := 5<<20 + 11
	if code, body := put(t, fmt.Sprintf("http://%s/api/data/%s/0?offset=0", addr, or.SID), data[:half]); code != 200 {
		t.Fatalf("put %d %s", code, body)
	}
	// 错误 offset → 409
	if code, _ := put(t, fmt.Sprintf("http://%s/api/data/%s/0?offset=1", addr, or.SID), data[1:10]); code != http.StatusConflict {
		t.Fatalf("want 409, got %d", code)
	}

	res, err := client.New(addr, client.Options{}).Send(context.Background(), []string{p})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Resumed || !lastReq.Resume || lastReq.ResumedBytes != int64(half) {
		t.Fatalf("not resumed: %+v %+v", res, lastReq)
	}
	mustEqualFile(t, filepath.Join(recv, "big.bin"), data)
}

// 模拟服务端重启：新的 Server 实例从磁盘 session.json/.part 续传（需重新哈希已收部分）。
func TestResumeAfterRestart(t *testing.T) {
	recv := t.TempDir()
	mk := func() (string, func()) {
		srv, _ := server.New(server.Config{RecvDir: recv, Approver: server.AutoApprover{}, Logger: log.New(io.Discard, "", 0)})
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		go srv.Serve(ln)
		return ln.Addr().String(), func() { srv.Shutdown(context.Background()) }
	}
	addr, stop := mk()
	src := t.TempDir()
	p := filepath.Join(src, "f.bin")
	data := writeRand(t, p, 4<<20)
	files, dirs, _ := client.Collect([]string{p})
	off := proto.Offer{TransferID: client.TransferID(files, dirs),
		Files: []proto.FileEntry{{FID: 0, Path: "f.bin", Size: files[0].Size, Mtime: files[0].Mtime}}}
	_, body := postJSON(t, "http://"+addr+"/api/offer", off)
	var or proto.OfferResp
	json.Unmarshal(body, &or)
	put(t, fmt.Sprintf("http://%s/api/data/%s/0?offset=0", addr, or.SID), data[:1<<20])
	stop()

	addr2, stop2 := mk()
	defer stop2()
	res, err := client.New(addr2, client.Options{}).Send(context.Background(), []string{p})
	if err != nil || !res.Resumed {
		t.Fatalf("%v %+v", err, res)
	}
	mustEqualFile(t, filepath.Join(recv, "f.bin"), data)
}

func TestHashMismatch(t *testing.T) {
	addr, _ := start(t, server.AutoApprover{})
	off := proto.Offer{TransferID: strings.Repeat("b", 32), Files: []proto.FileEntry{{FID: 0, Path: "h", Size: 4}}}
	_, body := postJSON(t, "http://"+addr+"/api/offer", off)
	var or proto.OfferResp
	json.Unmarshal(body, &or)
	put(t, fmt.Sprintf("http://%s/api/data/%s/0?offset=0", addr, or.SID), []byte("abcd"))
	good := proto.HashHex(xxh3.Hash128([]byte("abce")))
	code, _ := postJSON(t, fmt.Sprintf("http://%s/api/commit/%s/0", addr, or.SID), proto.CommitReq{XXH3: good})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", code)
	}
	// 超长 body → 400
	code, _ = put(t, fmt.Sprintf("http://%s/api/data/%s/0?offset=0", addr, or.SID), []byte("abcdef"))
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", code)
	}
}

// curl 场景：确认前不能回 100 Continue；拒绝返回 403；接受后回 100 再收 body。
func TestCurlExpectContinue(t *testing.T) {
	decide := make(chan server.Decision, 1)
	asked := make(chan server.Request, 1)
	addr, recv := start(t, funcApprover(func(r server.Request) server.Decision { asked <- r; return <-decide }))

	send := func(name string, body []byte, d server.Decision) (string, string) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintf(conn, "PUT /upload/%s HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n", name, len(body))
		r := <-asked
		// 确认前不应收到任何字节
		conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		one := make([]byte, 1)
		if n, _ := conn.Read(one); n != 0 {
			t.Fatal("server responded before approval")
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		decide <- d
		br := bufio.NewReader(conn)
		status, _ := br.ReadString('\n')
		if strings.Contains(status, "100") {
			br.ReadString('\n') // 空行
			conn.Write(body)
			status, _ = br.ReadString('\n')
		}
		resp, _ := io.ReadAll(br)
		_ = r
		return strings.TrimSpace(status), string(resp)
	}

	st, _ := send("x.txt", []byte("hello"), server.Reject)
	if !strings.Contains(st, "403") {
		t.Fatalf("want 403, got %q", st)
	}
	st, resp := send("..%2F..%2FCON.txt", []byte("hello"), server.Accept)
	if !strings.Contains(st, "200") || !strings.Contains(resp, "_CON.txt") {
		t.Fatalf("got %q %q", st, resp)
	}
	mustEqualFile(t, filepath.Join(recv, "_CON.txt"), []byte("hello"))
}
