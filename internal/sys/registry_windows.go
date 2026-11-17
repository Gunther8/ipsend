//go:build windows

package sys

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "ipsend"

func autoStartCmd(exe string) string { return `"` + exe + `" --hidden` }

// AutoStartEnabled 报告开机自启是否已指向当前 exe。
func AutoStartEnabled(exe string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	return err == nil && strings.EqualFold(v, autoStartCmd(exe))
}

// SetAutoStart 开启/关闭当前用户的开机自启（启动时只显示托盘）。
func SetAutoStart(exe string, on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(runValue, autoStartCmd(exe))
	}
	if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// RegisterAppID 注册 AppUserModelID 的显示名和图标，使系统通知的标题显示“ipsend”而不是内部 ID。
func RegisterAppID(id, displayName, iconPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+id, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("DisplayName", displayName); err != nil {
		return err
	}
	if iconPath != "" {
		return k.SetStringValue("IconUri", iconPath)
	}
	return nil
}
