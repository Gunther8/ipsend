package server

import (
	"crypto/subtle"
	"net/http"
	"sync"
	"time"
)

// PIN 校验（SPEC §3：可选）。客户端通过请求头 X-Ipsend-Pin 或查询参数 pin 提供。
// 同一 IP 连续输错 pinMaxFails 次后锁定 pinLockout，防止暴力猜测。

const (
	PINHeader   = "X-Ipsend-Pin"
	pinMaxFails = 5
	pinLockout  = 10 * time.Minute
)

type pinGuard struct {
	mu    sync.Mutex
	pin   string
	fails map[string]int
	until map[string]time.Time
}

func newPinGuard(pin string) *pinGuard {
	return &pinGuard{pin: pin, fails: map[string]int{}, until: map[string]time.Time{}}
}

func (g *pinGuard) set(pin string) {
	g.mu.Lock()
	g.pin = pin
	g.fails = map[string]int{}
	g.until = map[string]time.Time{}
	g.mu.Unlock()
}

func (g *pinGuard) required() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pin != ""
}

// check 返回 ""（通过）或错误码 "pin_required" / "bad_pin" / "pin_locked"。
func (g *pinGuard) check(ip string, r *http.Request) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pin == "" {
		return ""
	}
	if t, ok := g.until[ip]; ok {
		if time.Now().Before(t) {
			return "pin_locked"
		}
		delete(g.until, ip)
		delete(g.fails, ip)
	}
	got := r.Header.Get(PINHeader)
	if got == "" {
		got = r.URL.Query().Get("pin")
	}
	if got == "" {
		return "pin_required"
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(g.pin)) == 1 {
		delete(g.fails, ip)
		return ""
	}
	g.fails[ip]++
	if g.fails[ip] >= pinMaxFails {
		g.until[ip] = time.Now().Add(pinLockout)
		return "pin_locked"
	}
	return "bad_pin"
}

var pinMessages = map[string]string{
	"pin_required": "receiver requires a PIN (curl: -H \"X-Ipsend-Pin: <PIN>\")",
	"bad_pin":      "wrong PIN",
	"pin_locked":   "too many wrong PINs, try again in 10 minutes",
}

// checkPIN 校验失败时写好响应并返回 false。
func (s *Server) checkPIN(w http.ResponseWriter, r *http.Request, ip string, plainText bool) bool {
	code := s.pin.check(ip, r)
	if code == "" {
		return true
	}
	status := http.StatusUnauthorized
	if code == "pin_locked" {
		status = http.StatusTooManyRequests
	}
	s.log.Printf("pin check failed for %s: %s", ip, code)
	if plainText {
		http.Error(w, pinMessages[code], status)
	} else {
		writeErr(w, status, code, pinMessages[code])
	}
	return false
}
