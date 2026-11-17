package server_test

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ipsend/internal/client"
	"ipsend/internal/server"
)

func startPIN(t *testing.T, pin string, blocked []string, onBlocked func([]string)) (*server.Server, string) {
	t.Helper()
	srv, err := server.New(server.Config{RecvDir: t.TempDir(), Approver: funcApprover(func(r server.Request) server.Decision {
		if r.SenderName == "block-me" {
			return server.RejectAndBlock
		}
		return server.Accept
	}), PIN: pin, Blocked: blocked, OnBlockedChange: onBlocked, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	return srv, ln.Addr().String()
}

func TestPIN(t *testing.T) {
	srv, addr := startPIN(t, "4321", nil, nil)
	p := filepath.Join(t.TempDir(), "a.txt")
	writeRand(t, p, 100)
	send := func(pin string) error {
		_, err := client.New(addr, client.Options{PIN: pin}).Send(context.Background(), []string{p})
		return err
	}
	if err := send(""); !errors.Is(err, client.ErrPINRequired) {
		t.Fatalf("no pin: %v", err)
	}
	if err := send("0000"); !errors.Is(err, client.ErrBadPIN) {
		t.Fatalf("bad pin: %v", err)
	}
	if err := send("4321"); err != nil {
		t.Fatalf("good pin: %v", err)
	}
	// curl：不带 PIN → 401，带请求头 → 200
	req, _ := http.NewRequest("PUT", "http://"+addr+"/upload/c.txt", strings.NewReader("hi"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("curl no pin: %v %v", err, resp.StatusCode)
	}
	resp.Body.Close()
	req, _ = http.NewRequest("PUT", "http://"+addr+"/upload/c.txt", strings.NewReader("hi"))
	req.Header.Set(server.PINHeader, "4321")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("curl pin: %v %v", err, resp.StatusCode)
	}
	resp.Body.Close()

	// 连续输错 5 次锁定
	for i := 0; i < 4; i++ {
		send("1111")
	}
	if err := send("1111"); !errors.Is(err, client.ErrPINLocked) {
		t.Fatalf("want locked, got %v", err)
	}
	if err := send("4321"); !errors.Is(err, client.ErrPINLocked) {
		t.Fatalf("locked ip should stay locked, got %v", err)
	}
	// 修改 PIN 会清除锁定；清空 PIN 后无需 PIN
	srv.SetPIN("")
	if err := send(""); err != nil {
		t.Fatalf("pin cleared: %v", err)
	}
}

func TestBlockedPersist(t *testing.T) {
	var mu sync.Mutex
	var saved []string
	srv, addr := startPIN(t, "", []string{"10.0.0.9"}, func(l []string) { mu.Lock(); saved = l; mu.Unlock() })
	p := filepath.Join(t.TempDir(), "a.txt")
	writeRand(t, p, 10)
	_, err := client.New(addr, client.Options{SenderName: "block-me"}).Send(context.Background(), []string{p})
	if !errors.Is(err, client.ErrRejected) {
		t.Fatal(err)
	}
	mu.Lock()
	got := strings.Join(saved, ",")
	mu.Unlock()
	if got != "10.0.0.9,127.0.0.1" {
		t.Fatalf("saved %q", got)
	}
	// 被屏蔽后直接 403
	if _, err := client.New(addr, client.Options{}).Send(context.Background(), []string{p}); !errors.Is(err, client.ErrRejected) {
		t.Fatal(err)
	}
	srv.Unblock("127.0.0.1")
	if _, err := client.New(addr, client.Options{}).Send(context.Background(), []string{p}); err != nil {
		t.Fatalf("after unblock: %v", err)
	}
	if b := srv.Blocked(); len(b) != 1 || b[0] != "10.0.0.9" {
		t.Fatalf("blocked %v", b)
	}
}
