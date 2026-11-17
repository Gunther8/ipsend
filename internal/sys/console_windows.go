//go:build windows

package sys

import (
	"os"

	"golang.org/x/sys/windows"
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole    = kernel32.NewProc("AttachConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
)

// AttachParentConsole 让以 -H windowsgui 编译的程序在命令行模式下把输出写回启动它的终端。
// 已有控制台（未用 windowsgui 编译）时什么也不做。
func AttachParentConsole() {
	if h, _, _ := procGetConsoleWindow.Call(); h != 0 {
		return
	}
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	// 已被重定向到文件/管道的句柄保持不变，只替换原本无效的句柄
	if !validStd(windows.STD_OUTPUT_HANDLE) {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stdout = f
		}
	}
	if !validStd(windows.STD_ERROR_HANDLE) {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stderr = f
		}
	}
	if !validStd(windows.STD_INPUT_HANDLE) {
		if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
			os.Stdin = f
		}
	}
}

// validStd 判断启动时继承的标准句柄是否指向文件或管道（被重定向）。
// windowsgui 程序从终端直接启动时这些句柄为空；AttachConsole 后会被设成控制台，所以只认文件和管道。
func validStd(which uint32) bool {
	h, err := windows.GetStdHandle(which)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return false
	}
	t, err := windows.GetFileType(h)
	return err == nil && (t == windows.FILE_TYPE_DISK || t == windows.FILE_TYPE_PIPE)
}
