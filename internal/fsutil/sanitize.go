// Package fsutil 提供文件名清洗、防覆盖改名、磁盘检查等落盘工具（见 SPEC.md §5）。
package fsutil

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxSegBytes = 200

var reserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"CONIN$": true, "CONOUT$": true, "CLOCK$": true,
}

func init() {
	for i := 1; i <= 9; i++ {
		reserved["COM"+strconv.Itoa(i)] = true
		reserved["LPT"+strconv.Itoa(i)] = true
	}
	// 上标数字形式 COM¹ COM² COM³ 在 Windows 上同样保留
	for _, s := range []string{"¹", "²", "³"} {
		reserved["COM"+s] = true
		reserved["LPT"+s] = true
	}
}

// SanitizeSegment 清洗单个路径段；返回空串表示该段应被丢弃（"."、".."、空）。
func SanitizeSegment(s string) string {
	if s == "" || s == "." || s == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune('_')
		case strings.ContainsRune(`<>:"|?*/\`, r):
			b.WriteRune('_')
		case r == utf8.RuneError:
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(b.String(), ". ")
	out = strings.TrimLeft(out, " ")
	if out == "" {
		return "_"
	}
	stem := out
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if reserved[strings.ToUpper(strings.TrimRight(stem, " "))] {
		out = "_" + out
	}
	return truncateSeg(out)
}

// truncateSeg 把段截到 maxSegBytes 字节以内，尽量保留扩展名，不切断 UTF-8 字符。
func truncateSeg(s string) string {
	if len(s) <= maxSegBytes {
		return s
	}
	ext := filepath.Ext(s)
	if len(ext) > 20 || ext == s {
		ext = ""
	}
	limit := maxSegBytes - len(ext)
	stem := s[:len(s)-len(ext)]
	cut := 0
	for i := range stem {
		if i > limit {
			break
		}
		cut = i
	}
	if len(stem) <= limit {
		cut = len(stem)
	}
	return strings.TrimRight(stem[:cut], ". ") + ext
}

var ErrEmptyPath = errors.New("path is empty after sanitizing")

// SanitizeRelPath 把任意外部传来的路径清洗为安全的相对路径段列表。
// 去掉盘符、UNC/设备前缀、绝对路径根、"."/".."，并清洗每一段。
func SanitizeRelPath(p string) ([]string, error) {
	p = strings.ReplaceAll(p, `\`, "/")
	for _, pre := range []string{"//?/", "//./", "/??/"} {
		if strings.HasPrefix(p, pre) {
			p = p[len(pre):]
		}
	}
	// 盘符 "C:" / "C:/foo"（"a:b.txt" 这种不当盘符，冒号会被替换为 _）
	if len(p) >= 2 && p[1] == ':' && isLetter(p[0]) && (len(p) == 2 || p[2] == '/') {
		p = p[2:]
	}
	var segs []string
	for _, raw := range strings.Split(p, "/") {
		if s := SanitizeSegment(raw); s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return nil, ErrEmptyPath
	}
	return segs, nil
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// NumberedName 返回 "xxx (n).ext"（isDir 时不拆扩展名）。
func NumberedName(name string, n int, isDir bool) string {
	if n <= 0 {
		return name
	}
	ext := ""
	if !isDir {
		ext = filepath.Ext(name)
		if ext == name || len(ext) > 20 { // ".bashrc" 之类
			ext = ""
		}
	}
	stem := name[:len(name)-len(ext)]
	return stem + " (" + strconv.Itoa(n) + ")" + ext
}

// DedupeCaseInsensitive 在同一批路径里消除仅大小写不同的冲突，以及“文件与目录同名”的冲突。
// 输入、输出均为段列表；冲突的条目会把最后一段改为 "xxx (n).ext"。
func DedupeCaseInsensitive(paths [][]string) [][]string {
	files := map[string]bool{}
	dirs := map[string]bool{}
	for _, segs := range paths {
		for i := 1; i < len(segs); i++ {
			dirs[strings.ToLower(strings.Join(segs[:i], "/"))] = true
		}
	}
	out := make([][]string, len(paths))
	for idx, segs := range paths {
		segs = append([]string(nil), segs...)
		parent := strings.Join(segs[:len(segs)-1], "/")
		base := segs[len(segs)-1]
		for n := 0; ; n++ {
			cand := NumberedName(base, n, false)
			key := strings.ToLower(joinNonEmpty(parent, cand))
			if !files[key] && !dirs[key] {
				files[key] = true
				segs[len(segs)-1] = cand
				break
			}
		}
		out[idx] = segs
	}
	return out
}

func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	return a + "/" + b
}
