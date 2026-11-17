package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/ncruces/zenity"

	"ipsend/internal/sys"
)

func (u *UI) showInfo(title, msg string) { dialog.ShowInformation(title, msg, u.win) }
func (u *UI) showError(err error)        { dialog.ShowError(err, u.win) }

// showFirewallDialog 询问是否添加防火墙规则。cb: 0 以后再说，1 添加，2 不再提示。
func (u *UI) showFirewallDialog(manual bool, cb func(int)) {
	msg := widget.NewLabel("Windows 防火墙还没有放行 ipsend，其他电脑发来的文件可能会被拦截。\n\n" +
		"点“添加规则”后会弹出管理员权限确认（UAC），只为 ipsend 这个程序放行入站连接。")
	msg.Wrapping = fyne.TextWrapWord
	var d dialog.Dialog
	done := false
	answer := func(c int) {
		if done {
			return
		}
		done = true
		d.Hide()
		cb(c)
	}
	add := widget.NewButton("添加规则", func() { answer(1) })
	add.Importance = widget.HighImportance
	later := widget.NewButton("以后再说", func() { answer(0) })
	buttons := container.NewHBox(layout.NewSpacer(), later, add)
	if !manual {
		never := widget.NewButton("不再提示", func() { answer(2) })
		never.Importance = widget.LowImportance
		buttons = container.NewHBox(never, layout.NewSpacer(), later, add)
	}
	d = dialog.NewCustomWithoutButtons("防火墙", container.NewVBox(msg, buttons), u.win)
	d.SetOnClosed(func() { answer(0) })
	d.Resize(fyne.NewSize(460, 0))
	u.showMain()
	d.Show()
}

// askPIN 弹出 PIN 输入框；cb 收到空串表示取消。
func (u *UI) askPIN(addr string, wrong bool, cb func(string)) {
	e := widget.NewPasswordEntry()
	e.SetPlaceHolder("对方告诉你的 PIN")
	hint := fmt.Sprintf("%s 设置了 PIN，请输入：", addr)
	if wrong {
		hint = "PIN 不正确，请重新输入："
	}
	items := []*widget.FormItem{widget.NewFormItem("PIN", e)}
	d := dialog.NewForm(hint, "发送", "取消", items, func(ok bool) {
		if ok {
			cb(strings.TrimSpace(e.Text))
		} else {
			cb("")
		}
	}, u.win)
	e.OnSubmitted = func(string) { d.Submit() }
	d.Resize(fyne.NewSize(380, 0))
	d.Show()
	u.win.Canvas().Focus(e)
}

func validPIN(p string) bool {
	if len(p) < 4 || len(p) > 32 {
		return false
	}
	for _, r := range p {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// showSettings 打开设置窗口。
func (u *UI) showSettings() {
	w := u.app.NewWindow("ipsend 设置")
	w.SetIcon(iconRes)

	port := widget.NewEntry()
	port.SetText(strconv.Itoa(u.conf.Port))
	dir := widget.NewEntry()
	dir.SetText(u.conf.RecvDir)
	browse := widget.NewButtonWithIcon("浏览", theme.FolderOpenIcon(), func() {
		go func() {
			p, err := zenity.SelectFile(zenity.Title("选择接收目录"), zenity.Directory(), zenity.Filename(dir.Text+string(filepath.Separator)), zenity.Attach(nativeHWND(w)))
			if err == nil {
				fyne.Do(func() { dir.SetText(p) })
			}
		}()
	})
	autoStart := widget.NewCheck("开机自动启动（只显示托盘图标）", nil)
	autoStart.SetChecked(u.exe != "" && sys.AutoStartEnabled(u.exe))
	pin := widget.NewEntry()
	pin.SetText(u.conf.PIN)
	pin.SetPlaceHolder("留空表示不需要 PIN")
	pinHint := widget.NewLabel("设置后，对方发送时需要输入此 PIN（4-32 位字母或数字）。\ncurl 用法：curl -T 文件 -H \"X-Ipsend-Pin: PIN\" http://本机IP:端口/upload/文件名")
	pinHint.Wrapping = fyne.TextWrapWord
	pinHint.Importance = widget.LowImportance

	// 屏蔽列表
	blockedBox := container.NewVBox()
	var refreshBlocked func()
	refreshBlocked = func() {
		blockedBox.RemoveAll()
		list := u.conf.Blocked
		if u.srv != nil {
			list = u.srv.Blocked()
		}
		if len(list) == 0 {
			blockedBox.Add(widget.NewLabel("（无）"))
		}
		for _, ip := range list {
			ip := ip
			btn := widget.NewButton("解除", func() {
				if u.srv != nil {
					u.srv.Unblock(ip) // 回调中保存配置
				} else {
					kept := u.conf.Blocked[:0:0]
					for _, b := range u.conf.Blocked {
						if b != ip {
							kept = append(kept, b)
						}
					}
					u.conf.Blocked = kept
					_ = u.conf.Save()
				}
				refreshBlocked()
			})
			btn.Importance = widget.LowImportance
			blockedBox.Add(container.NewBorder(nil, nil, nil, btn, widget.NewLabel(ip)))
		}
	}
	refreshBlocked()

	// 防火墙
	fwStatus := widget.NewLabel("检查中…")
	var checkFW func()
	checkFW = func() {
		go func() {
			text := "无法检查"
			if u.exe != "" {
				if st, err := sys.CheckFirewall(u.exe); err == nil {
					switch {
					case st.OK():
						text = "✓ 已放行"
					case st.Blocked:
						text = "✗ 被阻止"
					default:
						text = "✗ 未放行"
					}
				}
			}
			fyne.Do(func() { fwStatus.SetText(text) })
			u.refreshFirewall()
		}()
	}
	checkFW()
	fwBtn := widget.NewButton("添加 / 修复规则", func() {
		go func() {
			u.askFirewall(true)
			checkFW()
		}()
	})

	form := widget.NewForm(
		widget.NewFormItem("端口", port),
		widget.NewFormItem("接收目录", container.NewBorder(nil, nil, nil, browse, dir)),
		widget.NewFormItem("", autoStart),
		widget.NewFormItem("PIN", pin),
		widget.NewFormItem("", pinHint),
		widget.NewFormItem("防火墙", container.NewBorder(nil, nil, nil, fwBtn, fwStatus)),
		widget.NewFormItem("屏蔽的 IP", blockedBox),
	)

	save := widget.NewButton("保存", nil)
	save.Importance = widget.HighImportance
	cancel := widget.NewButton("取消", func() { w.Close() })
	save.OnTapped = func() {
		p, err := strconv.Atoi(strings.TrimSpace(port.Text))
		if err != nil || p < 1 || p > 65535 {
			dialog.ShowError(errors.New("端口必须是 1-65535 之间的数字"), w)
			return
		}
		d := strings.TrimSpace(dir.Text)
		if d == "" {
			dialog.ShowError(errors.New("请填写接收目录"), w)
			return
		}
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			dialog.ShowError(fmt.Errorf("无法创建接收目录：%w", err), w)
			return
		}
		newPIN := strings.TrimSpace(pin.Text)
		if newPIN != "" && !validPIN(newPIN) {
			dialog.ShowError(errors.New("PIN 需要 4-32 位字母或数字"), w)
			return
		}
		restart := p != u.conf.Port || !strings.EqualFold(d, u.conf.RecvDir)
		apply := func() {
			if u.exe != "" && autoStart.Checked != sys.AutoStartEnabled(u.exe) {
				if err := sys.SetAutoStart(u.exe, autoStart.Checked); err != nil {
					dialog.ShowError(fmt.Errorf("设置开机自启失败：%w", err), w)
					return
				}
			}
			u.conf.Port, u.conf.RecvDir, u.conf.PIN, u.conf.AutoStart = p, d, newPIN, autoStart.Checked
			if err := u.conf.Save(); err != nil {
				dialog.ShowError(fmt.Errorf("保存设置失败：%w", err), w)
				return
			}
			if u.srv != nil {
				u.srv.SetPIN(newPIN)
			}
			u.refreshLocal()
			if restart {
				u.restartServer()
			}
			w.Close()
		}
		if restart && u.srv != nil && u.srv.Active() > 0 {
			dialog.ShowConfirm("正在接收文件", "修改端口或接收目录会中断正在进行的接收（已收部分保留，对方重发可续传）。继续吗？",
				func(ok bool) {
					if ok {
						apply()
					}
				}, w)
			return
		}
		apply()
	}

	w.SetContent(container.NewPadded(container.NewBorder(nil,
		container.NewHBox(layout.NewSpacer(), cancel, save), nil, nil, container.NewVScroll(form))))
	w.Resize(fyne.NewSize(560, 560))
	w.CenterOnScreen()
	w.Show()
}
