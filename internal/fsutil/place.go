package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MkdirUnique 在 parent 下创建名为 name 的新目录；已存在则依次尝试 "name (1)"、"name (2)"…
// 返回实际创建的目录名。使用 os.Mkdir 的原子性保证并发安全。
func MkdirUnique(parent, name string) (string, error) {
	for n := 0; n < 10000; n++ {
		cand := NumberedName(name, n, true)
		err := os.Mkdir(filepath.Join(parent, cand), 0o755)
		if err == nil {
			return cand, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("too many name conflicts for %q", name)
}

// PlaceFile 把临时文件 src 放到 dir/name，不覆盖已有文件；冲突时改名为 "name (n).ext"。
// 返回实际文件名。
func PlaceFile(src, dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for n := 0; n < 10000; n++ {
		cand := NumberedName(name, n, false)
		err := RenameNoReplace(src, filepath.Join(dir, cand))
		if err == nil {
			return cand, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("too many name conflicts for %q", name)
}

// CheckSpace 判断 dir 所在卷是否放得下 need 字节，以及（FAT32 时）单文件是否超限。
// 返回值 nil 表示可以接收；否则返回面向用户的中文错误。
func CheckSpace(dir string, need int64, maxFile int64) error {
	const margin = 64 << 20
	free, err := FreeBytes(dir)
	if err != nil {
		return fmt.Errorf("无法获取磁盘剩余空间: %v", err)
	}
	if need < 0 {
		need = 0
	}
	if uint64(need)+margin > free {
		return fmt.Errorf("磁盘空间不足：需要 %s，剩余 %s", HumanBytes(need), HumanBytes(int64(free)))
	}
	if fs, err := FSType(dir); err == nil && strings.EqualFold(fs, "FAT32") && maxFile >= 4<<30 {
		return fmt.Errorf("接收目录位于 FAT32 分区，无法保存大于等于 4GB 的文件（最大文件 %s）", HumanBytes(maxFile))
	}
	return nil
}

// HumanBytes 把字节数格式化为 1.23 GB 形式。
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
