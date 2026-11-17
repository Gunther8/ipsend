//go:build windows

package sys

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FirewallRuleName 是添加的入站规则名。
// 可执行文件的描述也叫 ipsend，所以 Windows 首次弹出的“允许访问”对话框自动生成的规则也是这个名字，
// 添加前一并删除，避免其中的“阻止”规则覆盖我们的“允许”规则。
const FirewallRuleName = "ipsend"

// FirewallStatus 描述针对本程序的入站规则情况。
type FirewallStatus struct {
	Allowed bool // 存在启用的入站允许规则
	Blocked bool // 存在启用的入站阻止规则（优先级高于允许）
}

// OK 表示其他电脑可以连进来（就 Windows 防火墙而言）。
func (s FirewallStatus) OK() bool { return s.Allowed && !s.Blocked }

// CheckFirewall 查询针对 exe 的入站规则。
// 用防火墙 COM 接口 HNetCfg.FwPolicy2：普通用户可读（NetSecurity 模块需要管理员），
// Direction/Action 是数字，与系统语言无关。Direction 1=入站；Action 1=允许、0=阻止。
func CheckFirewall(exe string) (FirewallStatus, error) {
	const script = `$ErrorActionPreference='Stop';` +
		`$r = @((New-Object -ComObject HNetCfg.FwPolicy2).Rules | ` +
		`Where-Object { $_.Direction -eq 1 -and $_.Enabled -and $_.ApplicationName -and ` +
		`[Environment]::ExpandEnvironmentVariables($_.ApplicationName) -ieq $env:IPSEND_EXE } | ` +
		`ForEach-Object { if ($_.Action -eq 1) { 'Allow' } else { 'Block' } });` +
		`ConvertTo-Json -Compress -InputObject $r`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "IPSEND_EXE="+exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		return FirewallStatus{}, fmt.Errorf("查询防火墙规则失败: %w", err)
	}
	var actions []string
	s := strings.TrimSpace(string(out))
	if s != "" && s != "null" {
		if err := json.Unmarshal([]byte(s), &actions); err != nil {
			var one string // 单个元素时 PowerShell 可能输出字符串
			if json.Unmarshal([]byte(s), &one) != nil {
				return FirewallStatus{}, fmt.Errorf("解析防火墙规则失败: %q", s)
			}
			actions = []string{one}
		}
	}
	var st FirewallStatus
	for _, a := range actions {
		switch a {
		case "Allow":
			st.Allowed = true
		case "Block":
			st.Blocked = true
		}
	}
	return st, nil
}

// ErrUACCancelled 表示用户在 UAC 对话框中点了“否”。
var ErrUACCancelled = errors.New("用户取消了管理员权限请求")

// AddFirewallRule 通过 UAC 提权运行 netsh：删除同名旧规则（含阻止规则），再按程序路径添加入站允许规则。
// 会阻塞直到 netsh 结束。
func AddFirewallRule(exe string) error {
	if strings.ContainsAny(exe, `"&|<>^%`) {
		return fmt.Errorf("程序路径含有特殊字符，无法自动添加规则：%s", exe)
	}
	cmdline := fmt.Sprintf(`/s /c "netsh advfirewall firewall delete rule name=%s >nul 2>&1 & `+
		`netsh advfirewall firewall add rule name=%s dir=in action=allow program="%s" enable=yes profile=any"`,
		FirewallRuleName, FirewallRuleName, exe)
	code, err := runElevated("cmd.exe", cmdline)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("netsh 退出码 %d", code)
	}
	return nil
}

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
)

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIconMonitor uintptr
	hProcess     windows.Handle
}

// runElevated 以管理员身份运行程序（触发 UAC），等待结束并返回退出码。
func runElevated(file, params string) (uint32, error) {
	const (
		seeMaskNoCloseProcess = 0x00000040
		seeMaskNoAsync        = 0x00000100
		swHide                = 0
	)
	verb, _ := windows.UTF16PtrFromString("runas")
	f, _ := windows.UTF16PtrFromString(file)
	p, _ := windows.UTF16PtrFromString(params)
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync, lpVerb: verb, lpFile: f, lpParameters: p, nShow: swHide}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, ErrUACCancelled
		}
		return 0, fmt.Errorf("ShellExecuteEx: %w", callErr)
	}
	if info.hProcess == 0 {
		return 0, errors.New("未取得进程句柄")
	}
	defer windows.CloseHandle(info.hProcess)
	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return 0, err
	}
	return code, nil
}
