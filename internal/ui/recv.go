package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ipsend/internal/fsutil"
	"ipsend/internal/server"
)

type logKind int

const (
	logInfo logKind = iota
	logOK
	logFail
)

type logEntry struct {
	kind logKind
	text string
	at   time.Time
}

// recvPanel：上方是进行中的接收任务（每个一行，左侧色条表示状态，可取消），下方是接收记录。
type recvPanel struct {
	errLabel *widget.Label
	idle     fyne.CanvasObject
	active   *fyne.Container
	rows     map[string]*recvRow
	logs     []logEntry
	list     *widget.List
}

type recvRow struct {
	box    fyne.CanvasObject
	stripe *themedRect
	title  *widget.Label
	count  *widget.Label
	status *widget.Label
	bar    *thinBar
	cancel *widget.Button
	closed bool
	from   string
	files  int
}

func (r *recvPanel) setServerErr(s string) {
	r.errLabel.SetText(s)
	r.errLabel.Show()
}

func (r *recvPanel) addLog(kind logKind, s string) {
	r.logs = append([]logEntry{{kind: kind, text: s, at: time.Now()}}, r.logs...)
	if len(r.logs) > 300 {
		r.logs = r.logs[:300]
	}
	r.list.Refresh()
	r.syncIdle()
}

// syncIdle：没有进行中的任务、也没有记录时显示空状态。
func (r *recvPanel) syncIdle() {
	if len(r.logs) == 0 && len(r.rows) == 0 {
		r.idle.Show()
	} else {
		r.idle.Hide()
	}
}

// 记录图标：用到时才创建（包加载时 Fyne 还没启动，提前创建会打出错误日志）。
func icoOK() fyne.Resource   { return theme.NewSuccessThemedResource(theme.ConfirmIcon()) }
func icoFail() fyne.Resource { return theme.NewErrorThemedResource(theme.CancelIcon()) }
func icoInfo() fyne.Resource {
	return theme.NewColoredResource(theme.InfoIcon(), theme.ColorNamePlaceHolder)
}

func (u *UI) buildRecv() fyne.CanvasObject {
	r := &u.recv
	r.rows = map[string]*recvRow{}
	r.errLabel = widget.NewLabel("")
	r.errLabel.Importance = widget.DangerImportance
	r.errLabel.Wrapping = fyne.TextWrapWord
	r.errLabel.Hide()
	idleIcon := widget.NewIcon(theme.NewColoredResource(theme.DownloadIcon(), theme.ColorNameDisabled))
	idleText := widget.NewLabelWithStyle("收到的文件会显示在这里", fyne.TextAlignCenter, fyne.TextStyle{})
	idleText.Importance = widget.LowImportance
	r.idle = container.NewCenter(container.NewVBox(container.NewCenter(sized(idleIcon, 32)), idleText))
	r.active = container.New(layout.NewCustomPaddedVBoxLayout(6))
	r.list = widget.NewList(
		func() int { return len(r.logs) },
		func() fyne.CanvasObject {
			text := widget.NewLabel("")
			text.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, sized(widget.NewIcon(icoInfo()), 16), mutedLabel(""), text)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= len(r.logs) {
				return
			}
			e := r.logs[i]
			c := o.(*fyne.Container)
			c.Objects[0].(*widget.Label).SetText(e.text)
			icon := c.Objects[1].(*fyne.Container).Objects[0].(*widget.Icon)
			switch e.kind {
			case logOK:
				icon.SetResource(icoOK())
			case logFail:
				icon.SetResource(icoFail())
			default:
				icon.SetResource(icoInfo())
			}
			c.Objects[2].(*widget.Label).SetText(e.at.Format("15:04"))
		},
	)
	openDir := widget.NewButtonWithIcon("打开目录", theme.FolderOpenIcon(), u.openRecvDir)
	openDir.Importance = widget.LowImportance
	clear := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { r.logs = nil; r.list.Refresh(); r.syncIdle() })
	clear.Importance = widget.LowImportance
	header := container.NewBorder(nil, nil, caption(theme.DownloadIcon(), "接收"), container.NewHBox(openDir, clear))
	top := container.NewVBox(r.errLabel, r.active)
	return card(header, container.NewBorder(top, nil, nil, nil, container.NewStack(r.list, r.idle)))
}

func (u *UI) newRecvRow(e server.Event) *recvRow {
	r := &u.recv
	row := &recvRow{from: e.RemoteIP, files: e.FileCount}
	switch {
	case e.Sender == "curl":
		row.from += " · curl"
	case e.Sender != "":
		row.from += " · " + e.Sender
	}
	row.title = widget.NewLabelWithStyle(row.from, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	row.title.Truncation = fyne.TextTruncateEllipsis
	row.count = mutedLabel("")
	row.status = mutedLabel("")
	row.status.Truncation = fyne.TextTruncateEllipsis
	row.bar = newThinBar(theme.ColorNamePrimary)
	sid := e.SID
	row.cancel = widget.NewButtonWithIcon("", theme.CancelIcon(), func() {
		if u.srv != nil {
			u.srv.CancelSession(sid)
		}
	})
	row.cancel.Importance = widget.LowImportance
	row.stripe = newThemedRect(theme.ColorNamePrimary, 0)
	content := container.NewVBox(
		container.NewBorder(nil, nil, nil, container.NewHBox(row.count, row.cancel), row.title),
		row.status, row.bar)
	row.box = accentRow(row.stripe, content)
	r.rows[sid] = row
	r.active.Add(row.box)
	r.syncIdle()
	return row
}

// closeRow 结束一行：色条变色、禁用取消按钮，几秒后移除。
func (u *UI) closeRow(sid string, row *recvRow, color fyne.ThemeColorName, keep time.Duration) {
	if row.closed {
		return
	}
	row.closed = true
	row.cancel.Disable()
	row.stripe.setFill(color)
	row.bar.setFill(color)
	time.AfterFunc(keep, func() {
		fyne.Do(func() {
			r := &u.recv
			if r.rows[sid] != row {
				return
			}
			delete(r.rows, sid)
			r.active.Remove(row.box)
			if len(r.rows) == 0 {
				r.syncIdle()
			}
		})
	})
}

func (row *recvRow) update(e server.Event) {
	row.count.SetText(fmt.Sprintf("%d/%d 个文件 · %s", e.FilesDone, e.FileCount, fsutil.HumanBytes(e.SessTotal)))
	if e.SessTotal > 0 {
		row.bar.SetValue(float64(e.SessBytes) / float64(e.SessTotal))
	} else if e.FileCount > 0 {
		row.bar.SetValue(float64(e.FilesDone) / float64(e.FileCount))
	}
}

// onEvent 由服务端在任意 goroutine 调用。
func (u *UI) onEvent(e server.Event) {
	fyne.Do(func() { u.handleEvent(e) })
}

func (u *UI) handleEvent(e server.Event) {
	r := &u.recv
	if e.Kind == "rejected" {
		r.addLog(logFail, "已拒绝 "+e.RemoteIP)
		return
	}
	if e.Kind == "error" && e.SID == "" {
		r.addLog(logFail, fmt.Sprintf("来自 %s 的传输出错：%v", e.RemoteIP, e.Err))
		return
	}
	row := r.rows[e.SID]
	if row == nil {
		if e.Kind == "finished" || e.Kind == "cancelled" {
			return // 行已移除
		}
		row = u.newRecvRow(e)
	}
	if e.Kind != "rehash" {
		row.update(e)
	}

	switch e.Kind {
	case "accepted":
		row.closed = false
		row.cancel.Enable()
		row.stripe.setFill(theme.ColorNamePrimary)
		row.bar.setFill(theme.ColorNamePrimary)
		msg := "开始接收"
		if e.Resume {
			msg = fmt.Sprintf("断点续传：已有 %s，继续接收", fsutil.HumanBytes(e.SessBytes))
		}
		row.status.SetText(msg)
		r.addLog(logInfo, fmt.Sprintf("已接受 %s：%d 个文件，%s", row.from, e.FileCount, fsutil.HumanBytes(e.SessTotal)))
	case "progress":
		row.status.SetText("接收中：" + e.Name)
		row.stripe.setFill(theme.ColorNamePrimary)
	case "rehash":
		pct := 0.0
		if e.Total > 0 {
			pct = float64(e.Bytes) * 100 / float64(e.Total)
		}
		row.status.SetText(fmt.Sprintf("校验已收部分：%s  %.0f%%", e.Name, pct))
	case "saved":
		row.status.SetText("已保存：" + e.Name)
		if row.files <= 20 { // 文件很多时只记汇总，避免刷屏
			r.addLog(logOK, fmt.Sprintf("%s（%s）← %s", e.Name, fsutil.HumanBytes(e.Bytes), e.RemoteIP))
		}
	case "finished":
		row.status.SetText("完成")
		row.bar.SetValue(1)
		if row.files > 20 {
			r.addLog(logOK, fmt.Sprintf("已从 %s 收到 %d 个文件（%s）", row.from, e.FilesDone, fsutil.HumanBytes(e.SessTotal)))
		}
		u.app.SendNotification(fyne.NewNotification("ipsend：接收完成",
			fmt.Sprintf("已从 %s 收到 %d 个文件（%s）", row.from, e.FilesDone, fsutil.HumanBytes(e.SessTotal))))
		u.closeRow(e.SID, row, theme.ColorNameSuccess, 5*time.Second)
	case "interrupted":
		if strings.HasPrefix(e.SID, "curl-") { // curl 不支持续传
			row.status.SetText("传输中断，未保存")
			r.addLog(logFail, fmt.Sprintf("来自 %s 的 curl 上传中断", row.from))
			u.closeRow(e.SID, row, theme.ColorNameError, 8*time.Second)
			return
		}
		row.status.SetText("连接中断，等待对方重发（已收部分会保留，可续传）")
		row.stripe.setFill(theme.ColorNameWarning)
	case "cancelled":
		by := "对方已取消"
		if e.Name == "receiver" {
			by = "已取消"
		}
		row.status.SetText(by + "（已收部分保留，对方重发可续传）")
		r.addLog(logFail, fmt.Sprintf("%s：来自 %s，已完成 %d/%d 个文件", by, row.from, e.FilesDone, e.FileCount))
		u.closeRow(e.SID, row, theme.ColorNamePlaceHolder, 8*time.Second)
	case "error":
		row.status.SetText(fmt.Sprintf("出错：%v", e.Err))
		r.addLog(logFail, fmt.Sprintf("来自 %s 的传输出错：%v", row.from, e.Err))
	}
}
