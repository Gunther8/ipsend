//go:build windows

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// LongPath 给绝对路径加 \\?\ 前缀，绕过 MAX_PATH（仅用于直接调用 Win32 API 的地方，os 包会自动处理）。
func LongPath(p string) string {
	p = filepath.Clean(p)
	if strings.HasPrefix(p, `\\?\`) || !filepath.IsAbs(p) {
		return p
	}
	if strings.HasPrefix(p, `\\`) { // UNC
		return `\\?\UNC\` + p[2:]
	}
	return `\\?\` + p
}

// FreeBytes 返回 dir 所在卷对当前用户可用的剩余字节数。
func FreeBytes(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return free, nil
}

// FSType 返回 dir 所在卷的文件系统名（NTFS / FAT32 / exFAT ...）。
func FSType(dir string) (string, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	vol := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(p, &vol[0], uint32(len(vol))); err != nil {
		return "", err
	}
	fs := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumeInformation(&vol[0], nil, 0, nil, nil, nil, &fs[0], uint32(len(fs))); err != nil {
		return "", err
	}
	return windows.UTF16ToString(fs), nil
}

// RenameNoReplace 把 src 改名为 dst；dst 已存在时返回 os.ErrExist，绝不覆盖。
func RenameNoReplace(src, dst string) error {
	s, err := windows.UTF16PtrFromString(LongPath(src))
	if err != nil {
		return err
	}
	d, err := windows.UTF16PtrFromString(LongPath(dst))
	if err != nil {
		return err
	}
	err = windows.MoveFileEx(s, d, 0) // 不带 REPLACE_EXISTING：目标存在则失败
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
		return os.ErrExist
	}
	return err
}
