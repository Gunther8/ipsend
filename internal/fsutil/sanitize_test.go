package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeRelPath(t *testing.T) {
	cases := map[string]string{
		"a.txt":                "a.txt",
		"../../etc/passwd":     "etc/passwd",
		`..\..\Windows\x.dll`:  "Windows/x.dll",
		"/abs/path.txt":        "abs/path.txt",
		`C:\Users\a.txt`:       "Users/a.txt",
		"C:a.txt":              "C_a.txt",
		`\\server\share\f.txt`: "server/share/f.txt",
		`\\?\C:\x\y.txt`:       "x/y.txt",
		"CON":                  "_CON",
		"con.txt":              "_con.txt",
		"LPT9.log":             "_LPT9.log",
		"COM10.txt":            "COM10.txt",
		"a:b.txt":              "a_b.txt",
		"what?.txt":            "what_.txt",
		"trail. . ":            "trail",
		"dir/./x":              "dir/x",
		"中文 文件.txt":            "中文 文件.txt",
		"a\x01b":               "a_b",
		"...":                  "_",
	}
	for in, want := range cases {
		segs, err := SanitizeRelPath(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := strings.Join(segs, "/"); got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "..", "/", `\\`, "./..", "C:", `C:\`} {
		if _, err := SanitizeRelPath(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("文", 100) + ".txt" // 300+ 字节
	s := SanitizeSegment(long)
	if len(s) > maxSegBytes || !strings.HasSuffix(s, ".txt") {
		t.Fatalf("bad truncate: %d %q", len(s), s)
	}
}

func TestNumberedName(t *testing.T) {
	if g := NumberedName("a.txt", 1, false); g != "a (1).txt" {
		t.Fatal(g)
	}
	if g := NumberedName("dir.v2", 2, true); g != "dir.v2 (2)" {
		t.Fatal(g)
	}
	if g := NumberedName(".bashrc", 1, false); g != ".bashrc (1)" {
		t.Fatal(g)
	}
}

func TestDedupe(t *testing.T) {
	in := [][]string{{"A.txt"}, {"a.TXT"}, {"x"}, {"X", "y"}}
	out := DedupeCaseInsensitive(in)
	got := []string{}
	for _, s := range out {
		got = append(got, strings.Join(s, "/"))
	}
	want := []string{"A.txt", "a (1).TXT", "x (1)", "X/y"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestPlaceNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old"), 0o644)
	src := filepath.Join(dir, "tmp")
	os.WriteFile(src, []byte("new"), 0o644)
	name, err := PlaceFile(src, dir, "a.txt")
	if err != nil || name != "a (1).txt" {
		t.Fatal(name, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "old" {
		t.Fatal("overwritten")
	}
	d, err := MkdirUnique(dir, "a.txt")
	if err != nil || d != "a.txt (1)" {
		t.Fatal(d, err)
	}
}
