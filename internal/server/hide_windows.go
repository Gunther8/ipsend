//go:build windows

package server

import "golang.org/x/sys/windows"

func hideDir(p string) {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return
	}
	_ = windows.SetFileAttributes(ptr, attrs|windows.FILE_ATTRIBUTE_HIDDEN)
}
