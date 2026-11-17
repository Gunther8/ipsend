//go:build windows

package sys

import (
	"bufio"
	"errors"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const mutexName = `Local\ipsend-singleton`

func pipeName() string {
	u := os.Getenv("USERNAME")
	if u == "" {
		u = "default"
	}
	return `\\.\pipe\ipsend-` + strings.Map(func(r rune) rune {
		if r == '\\' || r == '/' || r == ' ' {
			return '_'
		}
		return r
	}, u)
}

// Instance 表示已获得的单实例锁。
type Instance struct {
	mutex windows.Handle
	ln    net.Listener
}

// ErrAlreadyRunning 表示已有实例在运行（并已通知它显示窗口）。
var ErrAlreadyRunning = errors.New("ipsend is already running")

// AcquireSingleInstance 获取命名互斥锁。已有实例时通过命名管道通知其显示窗口，返回 ErrAlreadyRunning。
// onShow 在已有实例收到“显示”请求时被调用（在后台 goroutine 中）。
func AcquireSingleInstance(onShow func()) (*Instance, error) {
	name, _ := windows.UTF16PtrFromString(mutexName)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			windows.CloseHandle(h)
		}
		notifyExisting()
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, err
	}
	inst := &Instance{mutex: h}
	ln, err := winio.ListenPipe(pipeName(), &winio.PipeConfig{})
	if err == nil {
		inst.ln = ln
		go inst.serve(onShow)
	}
	return inst, nil
}

func (i *Instance) serve(onShow func()) {
	for {
		c, err := i.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			c.SetReadDeadline(time.Now().Add(2 * time.Second))
			line, _ := bufio.NewReader(c).ReadString('\n')
			if strings.TrimSpace(line) == "show" && onShow != nil {
				onShow()
			}
		}(c)
	}
}

func notifyExisting() {
	// 允许已有实例把窗口切到前台
	procAllowSetForegroundWindow.Call(uintptr(0xFFFFFFFF)) // ASFW_ANY
	timeout := 2 * time.Second
	for start := time.Now(); time.Since(start) < 3*time.Second; time.Sleep(200 * time.Millisecond) {
		c, err := winio.DialPipe(pipeName(), &timeout)
		if err != nil {
			continue
		}
		c.Write([]byte("show\n"))
		c.Close()
		return
	}
}

func (i *Instance) Release() {
	if i == nil {
		return
	}
	if i.ln != nil {
		i.ln.Close()
	}
	if i.mutex != 0 {
		windows.CloseHandle(i.mutex)
	}
}
