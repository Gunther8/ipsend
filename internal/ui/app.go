// Package ui 是 ipsend 的 Fyne 图形界面：主窗口、托盘、接收确认弹窗、设置。
package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ipsend/assets"
	"ipsend/internal/config"
	"ipsend/internal/proto"
	"ipsend/internal/server"
	"ipsend/internal/sys"
)

const appID = "local.ipsend"

type Options struct {
	StartHidden bool // 开机自启时只显示托盘
	Logger      *log.Logger
}

type UI struct {
	opt   Options
	conf  config.Config
	hist  *config.History
	exe   string
	app   fyne.App
	win   fyne.Window
	hwnd  uintptr
	srv   *server.Server
	ready atomic.Bool

	status    *pill // 顶部状态标签
	fwOK      bool  // 防火墙已放行（或无法检查）
	listenErr string
	serving   bool
	ipRow     *fyne.Container
	showVirt  bool

	send sendPanel
	recv recvPanel
}

// Run 启动图形界面，阻塞直到用户从托盘退出。
func Run(opt Options) error {
	u := &UI{opt: opt}
	inst, err := sys.AcquireSingleInstance(func() {
		if u.ready.Load() {
			fyne.Do(u.showMain)
		}
	})
	if errors.Is(err, sys.ErrAlreadyRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	defer inst.Release()

	u.conf, err = config.Load()
	if err != nil {
		u.logf("config load failed, using defaults: %v", err)
	}
	u.hist = config.LoadHistory()
	if exe, err := os.Executable(); err == nil {
		u.exe, _ = filepath.Abs(exe)
	}

	u.app = app.NewWithID(appID)
	u.app.Settings().SetTheme(newTheme())
	u.app.SetIcon(iconRes)
	u.win = u.app.NewWindow("ipsend " + proto.Version)
	u.win.SetIcon(iconRes)
	u.win.SetMaster()
	u.win.SetCloseIntercept(func() { u.win.Hide() })
	u.win.SetOnDropped(u.onDropped)
	u.win.SetContent(u.build())
	u.win.Resize(fyne.NewSize(620, 780))
	u.win.CenterOnScreen()

	if desk, ok := u.app.(desktop.App); ok {
		open := fyne.NewMenuItem("打开", u.showMain)
		quit := fyne.NewMenuItem("退出", u.quit)
		quit.IsQuit = true
		desk.SetSystemTrayMenu(fyne.NewMenu("ipsend", open, quit))
		desk.SetSystemTrayIcon(iconRes)
		desk.SetSystemTrayWindow(u.win)
	}

	u.app.Lifecycle().SetOnStarted(func() {
		u.ready.Store(true)
		go u.startup()
	})
	u.app.Lifecycle().SetOnStopped(u.stopServer)

	if !opt.StartHidden {
		u.win.Show()
		u.hookMain()
	}
	u.app.Run()
	return nil
}

func (u *UI) logf(format string, args ...any) {
	if u.opt.Logger != nil {
		u.opt.Logger.Printf(format, args...)
	}
}

// startup 在后台完成：注册通知名、同步开机自启、检查防火墙，然后启动接收服务。
func (u *UI) startup() {
	iconPath := filepath.Join(config.Dir(), "icon.png")
	if err := os.WriteFile(iconPath, assets.Icon, 0o644); err != nil {
		iconPath = ""
	}
	if err := sys.RegisterAppID(appID, "ipsend", iconPath); err != nil {
		u.logf("register app id: %v", err)
	}
	if u.exe != "" && u.conf.AutoStart && !sys.AutoStartEnabled(u.exe) { // exe 被移动过
		if err := sys.SetAutoStart(u.exe, true); err != nil {
			u.logf("autostart: %v", err)
		}
	}

	ok := u.refreshFirewall()
	// 窗口可见时，先引导添加防火墙规则再监听端口，避免同时弹出 Windows 自带的防火墙对话框。
	if !ok && !u.conf.FirewallDeclined && !u.opt.StartHidden && u.exe != "" {
		u.askFirewall(false)
	}
	fyne.Do(func() { u.startServer() })
}

// refreshFirewall 检查防火墙并更新主界面提示，返回是否已放行。
func (u *UI) refreshFirewall() bool {
	if u.exe == "" {
		return true
	}
	st, err := sys.CheckFirewall(u.exe)
	ok := err != nil || st.OK() // 查不到就不打扰用户
	if err != nil {
		u.logf("firewall check: %v", err)
	}
	fyne.Do(func() {
		u.fwOK = ok
		u.updateStatus()
	})
	return ok
}

// updateStatus 根据服务状态、防火墙刷新顶部的状态标签。
func (u *UI) updateStatus() {
	if u.status == nil {
		return
	}
	port := strconv.Itoa(u.conf.Port)
	switch {
	case u.listenErr != "":
		u.status.set("● 端口 "+port+" 无法监听 · 点此修改", pillDanger)
		u.status.onTap = u.showSettings
	case !u.serving:
		u.status.set("● 正在启动…", pillNeutral)
		u.status.onTap = nil
	case !u.fwOK:
		u.status.set("● 接收中 · "+port+" · 防火墙未放行，点此处理", pillWarning)
		u.status.onTap = func() { go u.askFirewall(true) }
	default:
		t := "● 接收中 · " + port
		if u.conf.PIN != "" {
			t += " · PIN"
		}
		u.status.set(t, pillSuccess)
		u.status.onTap = u.showSettings
	}
}

// askFirewall 询问并（经 UAC）添加防火墙规则。manual 表示用户主动点击，不受“不再提示”影响。阻塞直到完成。
func (u *UI) askFirewall(manual bool) {
	choice := make(chan int, 1) // 0 以后再说，1 添加，2 不再提示
	fyne.DoAndWait(func() {
		u.showFirewallDialog(manual, func(c int) { choice <- c })
	})
	c := <-choice
	switch c {
	case 1:
		err := sys.AddFirewallRule(u.exe)
		switch {
		case errors.Is(err, sys.ErrUACCancelled):
			u.conf.FirewallDeclined = true
			_ = u.conf.Save()
			fyne.Do(func() {
				u.showInfo("未添加防火墙规则", "已取消。之后可以在“设置”里重新添加。\n未放行时，其他电脑发来的连接可能被 Windows 拦截。")
			})
		case err != nil:
			fyne.Do(func() { u.showError(fmt.Errorf("添加防火墙规则失败：%w", err)) })
		default:
			u.conf.FirewallDeclined = false
			_ = u.conf.Save()
		}
		u.refreshFirewall()
	case 2:
		u.conf.FirewallDeclined = true
		_ = u.conf.Save()
	}
}

func nativeHWND(w fyne.Window) uintptr {
	var h uintptr
	if nw, ok := w.(driver.NativeWindow); ok {
		nw.RunNative(func(ctx any) {
			if wc, ok := ctx.(driver.WindowsWindowContext); ok {
				h = wc.HWND
			}
		})
	}
	return h
}

// hookMain 拦截主窗口最小化 → 隐藏到托盘。窗口首次显示后调用。
func (u *UI) hookMain() {
	if u.hwnd != 0 {
		return
	}
	u.hwnd = nativeHWND(u.win)
	sys.HookMinimize(u.hwnd, func() { fyne.Do(u.win.Hide) })
}

func (u *UI) showMain() {
	u.win.Show()
	u.hookMain()
	u.win.RequestFocus()
	sys.BringToFront(u.hwnd)
}

func (u *UI) quit() {
	u.app.Quit()
}

func (u *UI) startServer() {
	srv, err := server.New(server.Config{
		Port: u.conf.Port, RecvDir: u.conf.RecvDir, Name: hostname(),
		Approver: u, OnEvent: u.onEvent, Logger: u.opt.Logger,
		PIN: u.conf.PIN, Blocked: u.conf.Blocked,
		OnBlockedChange: func(l []string) {
			fyne.Do(func() {
				u.conf.Blocked = l
				if err := u.conf.Save(); err != nil {
					u.logf("save config: %v", err)
				}
			})
		},
	})
	if err != nil {
		u.listenErr = err.Error()
		u.recv.setServerErr("接收服务启动失败：" + err.Error())
		u.updateStatus()
		return
	}
	// 先同步监听，端口被占用时立即在状态栏提示
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(u.conf.Port))
	if err != nil {
		u.listenErr = err.Error()
		u.serving = false
		u.recv.setServerErr(fmt.Sprintf("端口 %d 监听失败（可能已被占用，可在“设置”里换一个端口）：%v", u.conf.Port, err))
		u.updateStatus()
		return
	}
	u.listenErr = ""
	u.serving = true
	u.recv.errLabel.Hide()
	u.srv = srv
	u.updateStatus()
	go func() {
		if err := srv.Serve(ln); err != nil {
			fyne.Do(func() {
				u.serving = false
				u.listenErr = err.Error()
				u.recv.setServerErr("接收服务异常停止：" + err.Error())
				u.updateStatus()
			})
		}
	}()
}

func (u *UI) stopServer() {
	if u.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = u.srv.Shutdown(ctx)
	u.srv = nil
}

// restartServer 应用新的端口 / 接收目录。
func (u *UI) restartServer() {
	srv := u.srv
	u.srv = nil
	u.serving = false
	u.updateStatus()
	go func() {
		if srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = srv.Shutdown(ctx)
			cancel()
		}
		fyne.Do(u.startServer)
	}()
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// ---------- 界面布局 ----------

func (u *UI) build() fyne.CanvasObject {
	header := u.buildHeader()
	top := container.New(layout.NewCustomPaddedVBoxLayout(10), u.buildLocal(), u.buildSend())
	recv := container.New(layout.NewCustomPaddedLayout(10, 0, 0, 0), u.buildRecv())
	body := container.New(layout.NewCustomPaddedLayout(4, 12, 12, 12), container.NewBorder(top, nil, nil, nil, recv))
	return container.NewBorder(header, nil, nil, nil, body)
}

// buildHeader：图标 + 名称 + 状态标签 + 设置按钮。
func (u *UI) buildHeader() fyne.CanvasObject {
	logo := canvas.NewImageFromResource(iconRes)
	logo.FillMode = canvas.ImageFillContain
	title := widget.NewLabelWithStyle("ipsend", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	u.status = newPill("● 正在启动…", pillNeutral, nil, nil)
	settings := widget.NewButtonWithIcon("", theme.SettingsIcon(), u.showSettings)
	settings.Importance = widget.LowImportance
	left := container.New(layout.NewCustomPaddedHBoxLayout(4),
		container.NewCenter(sized(logo, 22)), title, container.NewCenter(u.status))
	bar := container.New(layout.NewCustomPaddedLayout(6, 4, 14, 8), container.NewBorder(nil, nil, left, settings))
	return container.NewVBox(bar, widget.NewSeparator())
}

// buildLocal：本机 IP 小卡片，点击复制；虚拟网卡默认收起。
func (u *UI) buildLocal() fyne.CanvasObject {
	u.ipRow = container.New(layout.NewCustomPaddedHBoxLayout(6))
	u.refreshIPs()
	return card(caption(theme.ComputerIcon(), "本机地址 · 点击复制，告诉对方"), container.NewHScroll(u.ipRow))
}

func (u *UI) refreshIPs() {
	u.ipRow.RemoveAll()
	list := sys.LocalIPv4s()
	if len(list) == 0 {
		u.ipRow.Add(mutedLabel("未检测到可用网卡"))
		return
	}
	virtual := 0
	first := true
	for _, ip := range list {
		if ip.Virtual && !u.showVirt {
			virtual++
			continue
		}
		kind := pillNeutral
		if first && !ip.Virtual {
			kind = pillAccent // 最可能是局域网地址
		}
		first = false
		addr := ip.IP
		var p *pill
		p = newPill(addr, kind, theme.ContentCopyIcon(), func() {
			u.app.Clipboard().SetContent(addr)
			p.set("已复制", kind)
			time.AfterFunc(1200*time.Millisecond, func() { fyne.Do(func() { p.set(addr, kind) }) })
		})
		p.big = true
		u.ipRow.Add(p)
	}
	if virtual > 0 || u.showVirt {
		text := fmt.Sprintf("+%d 虚拟网卡", virtual)
		if u.showVirt {
			text = "收起虚拟网卡"
		}
		more := newPill(text, pillGhost, nil, func() { u.showVirt = !u.showVirt; u.refreshIPs() })
		u.ipRow.Add(container.NewCenter(more))
	}
	u.ipRow.Refresh()
}

// openRecvDir 在资源管理器中打开接收目录，失败时提示。
func (u *UI) openRecvDir() {
	if err := sys.OpenFolder(u.conf.RecvDir); err != nil {
		u.showError(fmt.Errorf("无法打开接收目录 %s：%w", u.conf.RecvDir, err))
	}
}

// refreshLocal 设置变更后刷新状态栏。
func (u *UI) refreshLocal() { u.updateStatus() }

// ---------- 拖放 ----------

func (u *UI) onDropped(_ fyne.Position, uris []fyne.URI) {
	for _, uri := range uris {
		if uri.Scheme() != "file" {
			continue
		}
		u.addItem(filepath.FromSlash(uri.Path()))
	}
	u.refreshItems()
}
