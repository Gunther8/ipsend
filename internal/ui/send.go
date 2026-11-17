package ui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/ncruces/zenity"

	"ipsend/internal/client"
	"ipsend/internal/fsutil"
)

// sendItem 是待发送列表中的一项；文件夹的大小在后台统计。
type sendItem struct {
	path  string
	dir   bool
	size  int64 // -1 表示统计中
	count int   // 文件夹内文件数
}

type sendPanel struct {
	ip      *widget.SelectEntry
	items   []*sendItem
	list    *widget.List
	empty   fyne.CanvasObject
	footer  fyne.CanvasObject
	summary *widget.Label
	sendBtn *widget.Button
	stopBtn *widget.Button
	tools   []*widget.Button // 添加/清空，发送中禁用

	progBox *fyne.Container
	percent *widget.Label
	detail  *widget.Label
	bar     *thinBar
	status  *widget.Label
	cancel  context.CancelFunc
}

const histSep = " · "

// addrOf 从下拉项 “192.168.1.20 · OFFICE-PC” 中取出地址。
func addrOf(text string) string {
	a, _, _ := strings.Cut(strings.TrimSpace(text), histSep)
	return strings.TrimSpace(a)
}

func (u *UI) historyOptions() []string {
	var out []string
	for _, e := range u.hist.List() {
		if e.Name != "" {
			out = append(out, e.Addr+histSep+e.Name)
		} else {
			out = append(out, e.Addr)
		}
	}
	return out
}

func (u *UI) addItem(p string) {
	s := &u.send
	for _, it := range s.items {
		if strings.EqualFold(it.path, p) {
			return
		}
	}
	st, err := os.Stat(p)
	if err != nil {
		return
	}
	it := &sendItem{path: p, dir: st.IsDir(), size: st.Size(), count: 1}
	if it.dir {
		it.size, it.count = -1, 0
		go func() {
			var size int64
			var n int
			_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					if info, err := d.Info(); err == nil {
						size += info.Size()
						n++
					}
				}
				return nil
			})
			fyne.Do(func() {
				it.size, it.count = size, n
				u.refreshItems()
			})
		}()
	}
	s.items = append(s.items, it)
}

func (u *UI) refreshItems() {
	s := &u.send
	if len(s.items) == 0 {
		s.empty.Show()
		s.footer.Hide()
	} else {
		s.empty.Hide()
		s.footer.Show()
		var total int64
		files, pending := 0, false
		for _, it := range s.items {
			if it.size < 0 {
				pending = true
				continue
			}
			total += it.size
			files += it.count
		}
		t := fmt.Sprintf("%d 项 · %d 个文件 · %s", len(s.items), files, fsutil.HumanBytes(total))
		if pending {
			t = fmt.Sprintf("%d 项 · 统计中…", len(s.items))
		}
		s.summary.SetText(t)
	}
	s.list.Refresh()
}

func (u *UI) buildSend() fyne.CanvasObject {
	s := &u.send
	s.ip = widget.NewSelectEntry(u.historyOptions())
	s.ip.SetPlaceHolder("对方 IP，例如 192.168.1.20")
	s.ip.OnSubmitted = func(string) { u.doSend() }
	if l := u.hist.List(); len(l) > 0 {
		s.ip.SetText(u.historyOptions()[0])
	}
	s.sendBtn = widget.NewButtonWithIcon("发送", theme.MailSendIcon(), u.doSend)
	s.sendBtn.Importance = widget.HighImportance
	ipRow := container.NewBorder(nil, nil, nil, s.sendBtn, s.ip)

	// 文件列表
	s.list = widget.NewList(
		func() int { return len(s.items) },
		func() fyne.CanvasObject {
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			size := mutedLabel("")
			rm := widget.NewButtonWithIcon("", theme.ContentClearIcon(), nil)
			rm.Importance = widget.LowImportance
			return container.NewBorder(nil, nil, widget.NewIcon(theme.FileIcon()), container.NewHBox(size, rm), name)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= len(s.items) {
				return
			}
			it := s.items[i]
			c := o.(*fyne.Container)
			name := c.Objects[0].(*widget.Label)
			icon := c.Objects[1].(*widget.Icon)
			right := c.Objects[2].(*fyne.Container)
			size := right.Objects[0].(*widget.Label)
			rm := right.Objects[1].(*widget.Button)
			name.SetText(filepath.Base(it.path))
			switch {
			case it.dir && it.size < 0:
				icon.SetResource(theme.NewPrimaryThemedResource(theme.FolderIcon()))
				size.SetText("统计中…")
			case it.dir:
				icon.SetResource(theme.NewPrimaryThemedResource(theme.FolderIcon()))
				size.SetText(fmt.Sprintf("%d 个 · %s", it.count, fsutil.HumanBytes(it.size)))
			default:
				icon.SetResource(theme.FileIcon())
				size.SetText(fsutil.HumanBytes(it.size))
			}
			rm.OnTapped = func() {
				if i < len(s.items) {
					s.items = append(s.items[:i], s.items[i+1:]...)
					u.refreshItems()
				}
			}
		},
	)

	addFile := widget.NewButtonWithIcon("添加文件", theme.FileIcon(), u.pickFiles)
	addDir := widget.NewButtonWithIcon("添加文件夹", theme.FolderIcon(), u.pickDir)
	addFile2 := widget.NewButtonWithIcon("文件", theme.ContentAddIcon(), u.pickFiles)
	addDir2 := widget.NewButtonWithIcon("文件夹", theme.ContentAddIcon(), u.pickDir)
	clear := widget.NewButtonWithIcon("清空", theme.DeleteIcon(), func() { s.items = nil; u.refreshItems() })
	for _, b := range []*widget.Button{addFile2, addDir2, clear} {
		b.Importance = widget.LowImportance
	}
	s.tools = []*widget.Button{addFile, addDir, addFile2, addDir2, clear}

	bigIcon := widget.NewIcon(theme.NewColoredResource(theme.UploadIcon(), theme.ColorNamePlaceHolder))
	hint := widget.NewLabelWithStyle("把文件或文件夹拖到这里", fyne.TextAlignCenter, fyne.TextStyle{})
	hint.Importance = widget.LowImportance
	s.empty = container.NewCenter(container.NewVBox(
		container.NewCenter(sized(bigIcon, 36)), hint,
		container.NewCenter(container.NewHBox(addFile, addDir))))
	s.summary = mutedLabel("")
	s.footer = container.NewBorder(widget.NewSeparator(), nil, nil, container.NewHBox(addFile2, addDir2, clear), s.summary)
	drop := dashedBox(minHeight(container.NewBorder(nil, s.footer, nil, nil, container.NewStack(s.list, s.empty)), 150))

	// 进度块
	s.percent = widget.NewLabelWithStyle("0%", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	s.percent.SizeName = theme.SizeNameSubHeadingText
	s.detail = mutedLabel("")
	s.detail.Truncation = fyne.TextTruncateEllipsis
	s.stopBtn = widget.NewButtonWithIcon("取消", theme.CancelIcon(), func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	s.stopBtn.Importance = widget.LowImportance
	s.bar = newThinBar(theme.ColorNamePrimary)
	s.progBox = container.NewVBox(container.NewBorder(nil, nil, s.percent, s.stopBtn, s.detail), s.bar)
	s.progBox.Hide()
	s.status = widget.NewLabel("")
	s.status.Wrapping = fyne.TextWrapWord
	s.status.Hide()

	u.refreshItems()
	content := container.New(layout.NewCustomPaddedVBoxLayout(8), ipRow, drop, s.progBox, s.status)
	return card(caption(theme.MailSendIcon(), "发送"), content)
}

func (u *UI) pickFiles() {
	go func() {
		paths, err := zenity.SelectFileMultiple(zenity.Title("选择要发送的文件"), zenity.Attach(u.hwnd))
		if err != nil {
			return
		}
		fyne.Do(func() {
			for _, p := range paths {
				u.addItem(p)
			}
			u.refreshItems()
		})
	}()
}

func (u *UI) pickDir() {
	go func() {
		p, err := zenity.SelectFile(zenity.Title("选择要发送的文件夹"), zenity.Directory(), zenity.Attach(u.hwnd))
		if err != nil {
			return
		}
		fyne.Do(func() { u.addItem(p); u.refreshItems() })
	}()
}

func (u *UI) setSending(on bool) {
	s := &u.send
	for _, b := range append([]*widget.Button{s.sendBtn}, s.tools...) {
		if on {
			b.Disable()
		} else {
			b.Enable()
		}
	}
	if on {
		s.ip.Disable()
		s.progBox.Show()
		s.status.Hide()
	} else {
		s.ip.Enable()
		s.progBox.Hide()
	}
}

// setResult 在进度块下方显示结果（成功绿色、失败红色、其他灰色）。
func (u *UI) setResult(text string, imp widget.Importance) {
	s := &u.send
	s.status.Importance = imp
	s.status.SetText(text)
	s.status.Show()
}

func (u *UI) doSend() {
	s := &u.send
	addr := addrOf(s.ip.Text)
	if addr == "" {
		dialog.ShowInformation("缺少对方 IP", "先填写对方的 IP 地址。", u.win)
		return
	}
	if len(s.items) == 0 {
		dialog.ShowInformation("没有文件", "先把要发送的文件或文件夹拖进来。", u.win)
		return
	}
	pin := ""
	if h, ok := u.hist.Get(addr); ok {
		pin = h.PIN
	}
	u.sendTo(addr, pin, false)
}

// sendTo 发送当前列表；对方要求 PIN 时弹框询问后重试。成功后记入 IP 历史。
func (u *UI) sendTo(addr, pin string, retried bool) {
	s := &u.send
	items := make([]string, len(s.items))
	for i, it := range s.items {
		items[i] = it.path
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	u.setSending(true)
	s.bar.SetValue(0)
	s.percent.SetText("…")
	s.detail.SetText("正在连接 " + addr)

	sp := &speedometer{}
	c := client.New(addr, client.Options{
		SenderName: hostname(),
		PIN:        pin,
		OnProgress: func(p client.Progress) {
			pct, text := sp.describe(p)
			fyne.Do(func() {
				if p.Total > 0 {
					s.bar.SetValue(float64(p.Sent) / float64(p.Total))
				}
				s.percent.SetText(pct)
				s.detail.SetText(text)
			})
		},
	})
	go func() {
		start := time.Now()
		res, err := c.Send(ctx, items)
		cancel()
		fyne.Do(func() {
			u.setSending(false)
			s.cancel = nil
			// 对方已确认收到 offer（被接受、拒绝或取消都说明地址可用）时记入历史
			if err == nil || errors.Is(err, client.ErrRejected) || errors.Is(err, client.ErrPeerCancelled) || errors.Is(err, context.Canceled) && res != nil {
				name := ""
				if res != nil {
					name = res.PeerName
				}
				_ = u.hist.Touch(addr, pin, name)
				s.ip.SetOptions(u.historyOptions())
			}
			switch {
			case errors.Is(err, client.ErrPINRequired) || errors.Is(err, client.ErrBadPIN):
				u.setResult(err.Error(), widget.WarningImportance)
				u.askPIN(addr, errors.Is(err, client.ErrBadPIN) || retried, func(p string) {
					if p == "" {
						u.setResult("已取消", widget.LowImportance)
						return
					}
					u.sendTo(addr, p, true)
				})
			case errors.Is(err, client.ErrPINLocked):
				u.setResult(err.Error(), widget.DangerImportance)
			case err == nil:
				msg := fmt.Sprintf("✓ 发送完成：%d 个文件，用时 %s", len(res.Saved), fmtDur(time.Since(start)))
				if res.Resumed {
					msg += "（断点续传）"
				}
				u.setResult(msg, widget.SuccessImportance)
				s.items = nil
				u.refreshItems()
			case errors.Is(err, context.Canceled):
				u.setResult("已取消。重新发送同样的文件会从断点继续。", widget.LowImportance)
			case errors.Is(err, client.ErrRejected):
				u.setResult("对方拒绝了接收（或 60 秒内没有确认）", widget.WarningImportance)
			case errors.Is(err, client.ErrPeerCancelled):
				u.setResult("对方取消了接收。已传部分会保留，重新发送可续传。", widget.WarningImportance)
			default:
				u.setResult("发送失败："+err.Error(), widget.DangerImportance)
			}
		})
	}()
}

// speedometer 计算平滑速度与剩余时间。
type speedometer struct {
	lastT    time.Time
	lastSent int64
	speed    float64 // 字节/秒，指数平滑
}

// describe 返回大号百分比和一行说明。
func (m *speedometer) describe(p client.Progress) (string, string) {
	switch p.Phase {
	case "waiting":
		return "…", fmt.Sprintf("等待对方确认（%d 个文件，%s，最长 60 秒）", p.FileCount, fsutil.HumanBytes(p.Total))
	case "hashing":
		pct := 0.0
		if p.HashTotal > 0 {
			pct = float64(p.Hashed) * 100 / float64(p.HashTotal)
		}
		return fmt.Sprintf("%.0f%%", pct), fmt.Sprintf("续传前校验已传部分：%s（%s）", filepath.Base(p.File), fsutil.HumanBytes(p.HashTotal))
	case "done":
		return "100%", "完成"
	}
	now := time.Now()
	if !m.lastT.IsZero() {
		if dt := now.Sub(m.lastT).Seconds(); dt > 0 {
			cur := max(float64(p.Sent-m.lastSent)/dt, 0)
			if m.speed == 0 {
				m.speed = cur
			} else {
				m.speed = m.speed*0.7 + cur*0.3
			}
		}
	}
	m.lastT, m.lastSent = now, p.Sent
	pct := 100.0
	if p.Total > 0 {
		pct = float64(p.Sent) * 100 / float64(p.Total)
	}
	parts := []string{fmt.Sprintf("%s / %s", fsutil.HumanBytes(p.Sent), fsutil.HumanBytes(p.Total))}
	if m.speed > 1 {
		parts = append(parts, fsutil.HumanBytes(int64(m.speed))+"/s",
			"剩余 "+fmtDur(time.Duration(float64(p.Total-p.Sent)/m.speed*float64(time.Second))))
	}
	parts = append(parts, fmt.Sprintf("%d/%d 个文件", p.FilesDone, p.FileCount))
	if p.Resumed {
		parts = append(parts, "续传")
	}
	return fmt.Sprintf("%.0f%%", pct), strings.Join(parts, " · ")
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%d 小时 %d 分", h, m)
	case m > 0:
		return fmt.Sprintf("%d 分 %d 秒", m, s)
	default:
		return fmt.Sprintf("%d 秒", s)
	}
}
