package server_test

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ipsend/internal/client"
	"ipsend/internal/server"
)

type eventLog struct {
	mu sync.Mutex
	ev []server.Event
	ch chan server.Event
}

func (l *eventLog) on(e server.Event) {
	l.mu.Lock()
	l.ev = append(l.ev, e)
	l.mu.Unlock()
	select {
	case l.ch <- e:
	default:
	}
}

func (l *eventLog) kinds() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := map[string]int{}
	for _, e := range l.ev {
		m[e.Kind]++
	}
	return m
}

func startWithEvents(t *testing.T, hook func(*server.Server, server.Event)) (*server.Server, string, *eventLog) {
	t.Helper()
	el := &eventLog{ch: make(chan server.Event, 1024)}
	var srv *server.Server
	on := func(e server.Event) {
		el.on(e)
		if hook != nil {
			hook(srv, e)
		}
	}
	srv, err := server.New(server.Config{RecvDir: t.TempDir(), Approver: server.AutoApprover{},
		OnEvent: on, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	return srv, ln.Addr().String(), el
}

// throttled 让发送端慢下来，便于在传输中途取消。
func throttledFile(t *testing.T, size int) string {
	p := filepath.Join(t.TempDir(), "big.bin")
	writeRand(t, p, size)
	return p
}

func TestReceiverCancel(t *testing.T) {
	var once sync.Once
	// 第一次传输：一开始收数据就由接收方取消（在服务端事件回调里同步取消，结果确定）
	_, addr, el := startWithEvents(t, func(s *server.Server, e server.Event) {
		if e.Kind == "progress" {
			once.Do(func() { s.CancelSession(e.SID) })
		}
	})
	p := throttledFile(t, 64<<20)

	errc := make(chan error, 1)
	go func() {
		_, err := client.New(addr, client.Options{}).Send(context.Background(), []string{p})
		errc <- err
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, client.ErrPeerCancelled) {
			t.Fatalf("want ErrPeerCancelled, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("client did not stop")
	}
	if el.kinds()["cancelled"] != 1 {
		t.Fatalf("events: %v", el.kinds())
	}
	// 取消后重发应能续传
	res, err := client.New(addr, client.Options{}).Send(context.Background(), []string{p})
	if err != nil || !res.Resumed {
		t.Fatalf("resend: %v %+v", err, res)
	}
}

func TestSenderCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// 接收方一开始收数据就取消发送端的 ctx：发送端在传输中途停下，并通知接收方
	_, addr, el := startWithEvents(t, func(_ *server.Server, e server.Event) {
		if e.Kind == "progress" {
			cancel()
		}
	})
	p := throttledFile(t, 64<<20)
	_, err := client.New(addr, client.Options{}).Send(ctx, []string{p})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want Canceled, got %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for el.kinds()["cancelled"] == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if k := el.kinds(); k["cancelled"] != 1 {
		t.Fatalf("receiver not notified: %v", k)
	}
	el.mu.Lock()
	defer el.mu.Unlock()
	for _, e := range el.ev {
		if e.Kind == "cancelled" && e.Name != "sender" {
			t.Fatalf("cancelled by %q, want sender", e.Name)
		}
	}
}

// 发送方在“对方刚接受、还没拿到 sid”时取消：无法通知对方，接收方靠停滞检查发现。
func TestStalledAfterAccept(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv, addr, el := startWithEvents(t, func(_ *server.Server, e server.Event) {
		if e.Kind == "accepted" {
			cancel()
		}
	})
	p := throttledFile(t, 1<<20)
	if _, err := client.New(addr, client.Options{}).Send(ctx, []string{p}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want Canceled, got %v", err)
	}
	srv.CheckStalledNow(-time.Second) // 立即视为停滞
	srv.CheckStalledNow(-time.Second) // 只报一次
	if k := el.kinds(); k["interrupted"] != 1 {
		t.Fatalf("events: %v", k)
	}
}

func TestSessionEvents(t *testing.T) {
	_, addr, el := startWithEvents(t, nil)
	src := t.TempDir()
	for i, n := range []int{10, 20, 5 << 20} {
		writeRand(t, filepath.Join(src, "d", string(rune('a'+i))+".bin"), n)
	}
	if _, err := client.New(addr, client.Options{}).Send(context.Background(), []string{filepath.Join(src, "d")}); err != nil {
		t.Fatal(err)
	}
	k := el.kinds()
	if k["accepted"] != 1 || k["saved"] != 3 || k["finished"] != 1 {
		t.Fatalf("events: %v", k)
	}
	el.mu.Lock()
	last := el.ev[len(el.ev)-1]
	el.mu.Unlock()
	if last.Kind != "finished" || last.FilesDone != 3 || last.SessBytes != last.SessTotal {
		t.Fatalf("finished event: %+v", last)
	}
}
