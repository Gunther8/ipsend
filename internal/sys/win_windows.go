//go:build windows

package sys

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW        = user32.NewProc("GetWindowLongPtrW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procFlashWindowEx            = user32.NewProc("FlashWindowEx")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procIsIconic                 = user32.NewProc("IsIconic")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
)

const (
	gwlpWndProc     = ^uintptr(3) // -4
	wmSysCommand    = 0x0112
	wmSize          = 0x0005
	scMinimize      = 0xF020
	sizeMinimized   = 1
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpShowWindow   = 0x0040
	swRestore       = 9
	flashwAll       = 3
	flashwTimerNoFG = 12
)

var (
	hookMu   sync.Mutex
	hooks    = map[uintptr]hookInfo{}
	hookProc = syscall.NewCallback(wndProc)
)

type hookInfo struct {
	orig  uintptr
	onMin func()
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	hookMu.Lock()
	h, ok := hooks[hwnd]
	hookMu.Unlock()
	if !ok || h.orig == 0 {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	switch {
	case msg == wmSysCommand && wp&0xFFF0 == scMinimize:
		go h.onMin()
		return 0 // 吞掉最小化
	case msg == wmSize && wp == sizeMinimized:
		// 其他途径（如 Win+D 之外的程序调用）导致最小化：恢复后隐藏
		procShowWindow.Call(hwnd, swRestore)
		go h.onMin()
	}
	r, _, _ := procCallWindowProcW.Call(h.orig, hwnd, msg, wp, lp)
	return r
}

// HookMinimize 拦截窗口最小化，改为调用 onMin（通常是隐藏到托盘）。同一窗口只钩一次。
func HookMinimize(hwnd uintptr, onMin func()) {
	if hwnd == 0 {
		return
	}
	hookMu.Lock()
	if _, ok := hooks[hwnd]; ok {
		hookMu.Unlock()
		return
	}
	// 先取得原窗口过程并登记，再替换（替换时不持锁，避免消息重入死锁）
	orig, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlpWndProc)
	hooks[hwnd] = hookInfo{orig: orig, onMin: onMin}
	hookMu.Unlock()
	procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, hookProc)
}

// SetTopmost 把窗口置顶并闪烁任务栏按钮以引起注意。
func SetTopmost(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	hwndTopmost := ^uintptr(0) // -1
	procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
	procSetForegroundWindow.Call(hwnd)
	type flashInfo struct {
		size    uint32
		hwnd    uintptr
		flags   uint32
		count   uint32
		timeout uint32
	}
	fi := flashInfo{hwnd: hwnd, flags: flashwAll | flashwTimerNoFG, count: 0}
	fi.size = uint32(unsafe.Sizeof(fi))
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}

// DisableMinMax 去掉窗口的最小化/最大化按钮（确认弹窗用，避免最小化后失去置顶）。
func DisableMinMax(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	const (
		gwlStyle        = ^uintptr(15) // -16
		wsMinimizeBox   = 0x00020000
		wsMaximizeBox   = 0x00010000
		swpFrameChanged = 0x0020
		swpNoZOrder     = 0x0004
	)
	style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
	procSetWindowLongPtrW.Call(hwnd, gwlStyle, style&^(wsMinimizeBox|wsMaximizeBox))
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpFrameChanged)
}

// BringToFront 恢复并激活窗口。
func BringToFront(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procSetForegroundWindow.Call(hwnd)
}

// OpenFolder 用资源管理器打开目录（不存在时先创建）。
// 用 ShellExecute 而不是启动 explorer.exe 进程：之前带了隐藏窗口标志启动，资源管理器窗口也被一起隐藏了。
func OpenFolder(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("open")
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, p, nil, nil, windows.SW_SHOWNORMAL)
}

// RevealFile 在资源管理器中定位文件。
func RevealFile(path string) error {
	return exec.Command("explorer.exe", "/select,", path).Start()
}
