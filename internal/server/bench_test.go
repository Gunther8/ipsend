package server_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"ipsend/internal/client"
	"ipsend/internal/server"
)

// TestManySmallFiles：上万个小文件的吞吐（IPSEND_SMALL=N 可改数量）。
func TestManySmallFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	n := 10000
	if v, err := strconv.Atoi(os.Getenv("IPSEND_SMALL")); err == nil {
		n = v
	}
	addr, recv := start(t, server.AutoApprover{})
	src := filepath.Join(t.TempDir(), "many")
	for i := 0; i < n; i++ {
		dir := filepath.Join(src, fmt.Sprintf("d%03d", i/100))
		if i%100 == 0 {
			os.MkdirAll(dir, 0o755)
		}
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d.txt", i)), []byte(fmt.Sprintf("file %d\n", i)), 0o644)
	}
	start := time.Now()
	res, err := client.New(addr, client.Options{}).Send(context.Background(), []string{src})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	t.Logf("%d files in %s (%.0f files/s)", len(res.Saved), el.Round(time.Millisecond), float64(n)/el.Seconds())
	if len(res.Saved) != n {
		t.Fatalf("saved %d", len(res.Saved))
	}
	b, err := os.ReadFile(filepath.Join(recv, "many", "d099", "f09999.txt"))
	if n == 10000 && (err != nil || string(b) != "file 9999\n") {
		t.Fatalf("content: %q %v", b, err)
	}
}
