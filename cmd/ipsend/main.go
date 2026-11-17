package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"ipsend/assets"
	"ipsend/internal/client"
	"ipsend/internal/config"
	"ipsend/internal/fsutil"
	"ipsend/internal/proto"
	"ipsend/internal/server"
	"ipsend/internal/sys"
	"ipsend/internal/ui"
)

func usage() {
	fmt.Fprintf(os.Stderr, `ipsend %s — 知道 IP 就能直传文件

用法:
  ipsend                    启动图形界面（--hidden 启动时只显示托盘）
  ipsend recv [-port 52100] [-dir 接收目录] [-pin PIN] [-yes]
  ipsend send [-port 52100] [-pin PIN] <IP[:端口]> <文件或文件夹>...

Linux 发送:
  curl -T file --expect100-timeout 120 http://IP:52100/upload/file
  （对方设置了 PIN 时加上 -H "X-Ipsend-Pin: PIN"）
`, proto.Version)
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "--hidden" {
		if err := runGUI(len(os.Args) >= 2); err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
		return
	}
	sys.AttachParentConsole()
	var err error
	switch os.Args[1] {
	case "recv":
		err = cmdRecv(os.Args[2:])
	case "send":
		err = cmdSend(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

// runGUI 启动图形界面；日志写到 %APPDATA%\ipsend\ipsend.log（超过 5MB 时清空）。
func runGUI(hidden bool) error {
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if dir, err := os.UserConfigDir(); err == nil {
		dir = filepath.Join(dir, "ipsend")
		_ = os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, "ipsend.log")
		if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 {
			_ = os.Remove(p)
		}
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			defer f.Close()
			logger = log.New(f, "", log.LstdFlags)
		}
	}
	// 默认用内置的 Mesa 软件渲染画界面，不依赖显卡驱动（远程桌面、老显卡下系统 OpenGL 不可用）。
	// 设置环境变量 IPSEND_GL=native 可改回系统 OpenGL。
	if os.Getenv("IPSEND_GL") != "native" {
		base := filepath.Join(os.TempDir(), "ipsend")
		if d, err := os.UserCacheDir(); err == nil {
			base = filepath.Join(d, "ipsend")
		}
		if dir, err := sys.LoadSoftwareGL(base, assets.MesaFiles()); err != nil {
			logger.Printf("software OpenGL unavailable, falling back to system OpenGL: %v", err)
		} else {
			logger.Printf("software OpenGL loaded from %s", dir)
		}
	}
	return ui.Run(ui.Options{StartHidden: hidden, Logger: logger})
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func defaultRecvDir() string { return config.DefaultRecvDir() }

func cmdRecv(args []string) error {
	fs := flag.NewFlagSet("recv", flag.ExitOnError)
	port := fs.Int("port", proto.DefaultPort, "监听端口")
	dir := fs.String("dir", defaultRecvDir(), "接收目录")
	yes := fs.Bool("yes", false, "自动接受所有请求（测试用）")
	pin := fs.String("pin", "", "要求对方提供的 PIN（留空表示不需要）")
	_ = fs.Parse(args)

	var ap server.Approver = server.AutoApprover{}
	if !*yes {
		ap = newCLIApprover()
	}
	srv, err := server.New(server.Config{Port: *port, RecvDir: *dir, Name: hostname(), Approver: ap, PIN: *pin})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	return srv.ListenAndServe()
}

// cliApprover 在终端里询问 y/n；60 秒超时自动拒绝。
type cliApprover struct {
	lines chan string
}

func newCLIApprover() *cliApprover {
	a := &cliApprover{lines: make(chan string, 16)}
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			a.lines <- strings.TrimSpace(sc.Text())
		}
	}()
	return a
}

func (a *cliApprover) Approve(ctx context.Context, r server.Request) server.Decision {
	for { // 丢弃提示出现前多敲的输入
		select {
		case <-a.lines:
			continue
		default:
		}
		break
	}
	fmt.Printf("\n===== 收到文件请求 =====\n来自: %s (%s)\n", r.RemoteIP, r.SenderName)
	fmt.Printf("文件: %d 个，共 %s\n", r.FileCount, fsutil.HumanBytes(r.TotalSize))
	for _, n := range r.Names {
		fmt.Printf("  - %s\n", n)
	}
	if r.FileCount > len(r.Names) {
		fmt.Printf("  ... 等 %d 个\n", r.FileCount)
	}
	if r.Resume && r.TotalSize > 0 {
		fmt.Printf("续传：已收 %.1f%%\n", float64(r.ResumedBytes)*100/float64(r.TotalSize))
	}
	fmt.Printf("接受? [y=接受 / n=拒绝 / b=拒绝并屏蔽]（%d 秒后自动拒绝）: ", int(time.Until(r.Deadline).Seconds()))
	select {
	case l := <-a.lines:
		switch strings.ToLower(l) {
		case "y", "yes":
			return server.Accept
		case "b":
			return server.RejectAndBlock
		}
		return server.Reject
	case <-ctx.Done():
		fmt.Println("\n超时或对方已断开，自动拒绝")
		return server.Reject
	}
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	port := fs.Int("port", proto.DefaultPort, "对方端口（地址里未写端口时使用）")
	pin := fs.String("pin", "", "对方要求的 PIN")
	_ = fs.Parse(args)
	if fs.NArg() < 2 {
		usage()
		os.Exit(2)
	}
	addr := client.NormalizeAddr(fs.Arg(0), *port)
	paths := fs.Args()[1:]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	start := time.Now()
	var lastSent int64
	lastT := start
	c := client.New(addr, client.Options{
		SenderName: hostname(),
		PIN:        *pin,
		OnProgress: func(p client.Progress) {
			switch p.Phase {
			case "waiting":
				fmt.Printf("等待对方确认（%d 个文件，%s）...\n", p.FileCount, fsutil.HumanBytes(p.Total))
			case "hashing":
				fmt.Printf("\r校验已传部分: %s                    ", p.File)
			case "sending":
				now := time.Now()
				speed := 0.0
				if dt := now.Sub(lastT).Seconds(); dt > 0 {
					speed = float64(p.Sent-lastSent) / dt
				}
				lastSent, lastT = p.Sent, now
				pct := 100.0
				if p.Total > 0 {
					pct = float64(p.Sent) * 100 / float64(p.Total)
				}
				fmt.Printf("\r[%d/%d] %5.1f%%  %s/%s  %s/s    ", p.FileIndex+1, p.FileCount, pct,
					fsutil.HumanBytes(p.Sent), fsutil.HumanBytes(p.Total), fsutil.HumanBytes(int64(speed)))
			}
		},
	})
	res, err := c.Send(ctx, paths)
	fmt.Println()
	if err != nil {
		if errors.Is(err, client.ErrRejected) {
			return err
		}
		return err
	}
	if res.Resumed {
		fmt.Println("（断点续传）")
	}
	fmt.Printf("完成，用时 %s，对方保存为:\n", time.Since(start).Round(time.Millisecond))
	for _, s := range res.Saved {
		fmt.Println("  " + s)
	}
	return nil
}
