package ui

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ipsend/internal/fsutil"
	"ipsend/internal/server"
	"ipsend/internal/sys"
)

// Approve 实现 server.Approver：弹出独立的置顶确认窗口。
// 服务端保证同一时间只调用一次，排队由服务端负责。
func (u *UI) Approve(ctx context.Context, r server.Request) server.Decision {
	ch := make(chan server.Decision, 1)
	decide := func(d server.Decision) {
		select {
		case ch <- d:
		default:
		}
	}

	var w fyne.Window
	var countdown *widget.Label
	var bar *thinBar
	fyne.DoAndWait(func() { w, countdown, bar = u.buildPrompt(r, decide) })
	defer fyne.Do(func() { w.Close() })

	fyne.Do(func() {
		u.app.SendNotification(fyne.NewNotification("ipsend：收到文件请求",
			fmt.Sprintf("%s 想发送 %d 个文件（%s）", r.RemoteIP, r.FileCount, fsutil.HumanBytes(r.TotalSize))))
	})

	total := time.Until(r.Deadline).Seconds()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case d := <-ch:
			return d
		case <-ctx.Done():
			return server.Reject
		case <-tick.C:
			left := time.Until(r.Deadline).Seconds()
			fyne.Do(func() {
				countdown.SetText(fmt.Sprintf("%d 秒后自动拒绝", int(max(left, 0)+0.5)))
				if total > 0 {
					bar.SetValue(left / total)
				}
			})
		}
	}
}

func (u *UI) buildPrompt(r server.Request, decide func(server.Decision)) (fyne.Window, *widget.Label, *thinBar) {
	w := u.app.NewWindow("ipsend - 文件接收请求")
	w.SetIcon(iconRes)

	// 头部：圆形图标 + 谁 + 摘要
	avatarBg := newThemedRect(colAccentBg, 999)
	avatar := container.NewStack(avatarBg, container.NewCenter(sized(widget.NewIcon(theme.NewPrimaryThemedResource(theme.DownloadIcon())), 22)))
	title := widget.NewLabelWithStyle(r.RemoteIP+" 想发文件给你", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := fmt.Sprintf("%d 个文件 · %s", r.FileCount, fsutil.HumanBytes(r.TotalSize))
	if r.SenderName != "" && r.SenderName != "curl" {
		sub = r.SenderName + " · " + sub
	} else if r.Kind == "curl" {
		sub = "curl 上传 · " + sub
	}
	head := container.NewBorder(nil, nil, container.NewCenter(sized(avatar, 40)), nil,
		container.New(layout.NewCustomPaddedVBoxLayout(-6), title, mutedLabel(sub)))

	// 文件列表（带大小）
	files := container.New(layout.NewCustomPaddedVBoxLayout(-8))
	for i, n := range r.Names {
		name := widget.NewLabel(n)
		name.Truncation = fyne.TextTruncateEllipsis
		size := ""
		if i < len(r.Sizes) && r.Sizes[i] >= 0 {
			size = fsutil.HumanBytes(r.Sizes[i])
		}
		files.Add(container.NewBorder(nil, nil, nil, mutedLabel(size), name))
	}
	if r.FileCount > len(r.Names) {
		files.Add(mutedLabel(fmt.Sprintf("… 等共 %d 个文件", r.FileCount)))
	}
	fileBox := container.NewStack(newThemedRect(colField, 8), container.New(layout.NewCustomPaddedLayout(2, 2, 8, 8), files))

	parts := []fyne.CanvasObject{head, fileBox}
	if r.Resume && r.TotalSize > 0 {
		resume := widget.NewLabel(fmt.Sprintf("断点续传：已收 %.1f%%，接受后接着传", float64(r.ResumedBytes)*100/float64(r.TotalSize)))
		resume.Importance = widget.HighImportance
		resume.SizeName = theme.SizeNameCaptionText
		parts = append(parts, resume)
	}

	bar := newThinBar(theme.ColorNameWarning)
	bar.SetValue(1)
	countdown := mutedLabel(fmt.Sprintf("%d 秒后自动拒绝", int(time.Until(r.Deadline).Seconds()+0.5)))

	accept := widget.NewButton("接受", func() { decide(server.Accept) })
	accept.Importance = widget.HighImportance
	reject := widget.NewButton("拒绝", func() { decide(server.Reject) })
	block := widget.NewButton("屏蔽此 IP", func() { decide(server.RejectAndBlock) })
	block.Importance = widget.LowImportance
	buttons := container.NewHBox(block, layout.NewSpacer(), reject, accept)
	parts = append(parts, container.New(layout.NewCustomPaddedVBoxLayout(0), bar, countdown), buttons)

	content := container.New(layout.NewCustomPaddedVBoxLayout(10), parts...)
	w.SetContent(container.NewStack(newThemedRect(colCard, 0), container.New(layout.NewCustomPaddedLayout(14, 12, 16, 16), content)))
	w.SetOnClosed(func() { decide(server.Reject) })
	w.Resize(fyne.NewSize(440, 0))
	w.SetFixedSize(true)
	w.CenterOnScreen()
	w.Show()
	h := nativeHWND(w)
	sys.DisableMinMax(h)
	sys.SetTopmost(h)
	w.Canvas().Focus(nil)
	return w, countdown, bar
}
