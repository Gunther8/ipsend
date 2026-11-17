//go:build windows

package sys

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

// LoadSoftwareGL 把内嵌的 Mesa llvmpipe（gzip 压缩的 DLL）释放到 baseDir\mesa-<hash>\，
// 并按完整路径加载其中的 opengl32.dll。必须在 Fyne/GLFW 初始化之前调用。
//
// 之后 GLFW 和 go-gl 按名字 LoadLibrary("opengl32.dll") 时，Windows 会复用这个已加载的同名模块，
// 于是界面改由 CPU 软件渲染，不再依赖显卡驱动——远程桌面、老显卡、虚拟机下都能正常显示。
// files 的键是文件名（不含 .gz），值是 gzip 数据。返回释放目录。
func LoadSoftwareGL(baseDir string, files map[string][]byte) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("没有内嵌的软件渲染库")
	}
	if _, ok := files["opengl32.dll"]; !ok {
		return "", fmt.Errorf("内嵌文件中缺少 opengl32.dll")
	}
	names := make([]string, 0, len(files))
	h := sha256.New()
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h.Write([]byte(n))
		h.Write(files[n])
	}
	dir := filepath.Join(baseDir, "mesa-"+hex.EncodeToString(h.Sum(nil))[:12])

	if _, err := os.Stat(filepath.Join(dir, ".ok")); err != nil {
		if err := extract(dir, names, files); err != nil {
			return "", err
		}
	}

	// 线程数：界面很简单，限制 llvmpipe 的渲染线程，避免占满 CPU
	if os.Getenv("LP_NUM_THREADS") == "" {
		os.Setenv("LP_NUM_THREADS", "2")
	}
	if os.Getenv("GALLIUM_DRIVER") == "" {
		os.Setenv("GALLIUM_DRIVER", "llvmpipe")
	}
	const loadWithAlteredSearchPath = 0x00000008 // 依赖 DLL 从同一目录查找
	if _, err := windows.LoadLibraryEx(filepath.Join(dir, "opengl32.dll"), 0, loadWithAlteredSearchPath); err != nil {
		return dir, fmt.Errorf("加载软件渲染库失败: %w", err)
	}
	return dir, nil
}

// extract 解压到临时目录后整体改名，避免两个进程同时释放时读到半截文件。
func extract(dir string, names []string, files map[string][]byte) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), filepath.Base(dir)+".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, n := range names {
		if strings.ContainsAny(n, `/\:`) {
			return fmt.Errorf("非法文件名 %q", n)
		}
		zr, err := gzip.NewReader(bytes.NewReader(files[n]))
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		f, err := os.Create(filepath.Join(tmp, n))
		if err != nil {
			return err
		}
		_, err = io.Copy(f, zr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("释放 %s 失败: %w", n, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, ".ok"), nil, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(filepath.Join(dir, ".ok")); statErr == nil {
			return nil // 另一个进程刚释放完
		}
		_ = os.RemoveAll(dir) // 残缺目录：清掉重来
		return os.Rename(tmp, dir)
	}
	cleanupOldMesa(filepath.Dir(dir), filepath.Base(dir))
	return nil
}

// cleanupOldMesa 删除旧版本释放的目录（正在被其他进程使用的会删除失败，忽略即可）。
func cleanupOldMesa(base, keep string) {
	ents, _ := os.ReadDir(base)
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), "mesa-") && e.Name() != keep {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}
