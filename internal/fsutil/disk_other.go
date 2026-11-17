//go:build !windows

package fsutil

import (
	"os"

	"golang.org/x/sys/unix"
)

func LongPath(p string) string { return p }

func FreeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func FSType(dir string) (string, error) { return "", nil }

// RenameNoReplace：非 Windows 平台用 link+unlink 实现不覆盖改名。
func RenameNoReplace(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		if os.IsExist(err) {
			return os.ErrExist
		}
		return err
	}
	return os.Remove(src)
}
