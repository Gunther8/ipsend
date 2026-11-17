# 编译 ipsend.exe（需要 Go 和 MSYS2 ucrt64 gcc；可选 go-winres 嵌入图标/版本信息）
#   go install github.com/tc-hib/go-winres@latest
$ErrorActionPreference = "Stop"
$gcc = "$env:LOCALAPPDATA\Programs\msys64\ucrt64\bin"
if (Test-Path "$gcc\gcc.exe") { $env:PATH = "$gcc;$env:PATH" }
if (Test-Path "C:\Program Files\Go\bin\go.exe") { $env:PATH = "C:\Program Files\Go\bin;$env:PATH" }
$env:CGO_ENABLED = "1"
Set-Location $PSScriptRoot

# 图标、manifest（DPI 感知、asInvoker、长路径）、版本信息 → cmd/ipsend/rsrc_windows_amd64.syso
$winres = "$env:USERPROFILE\go\bin\go-winres.exe"
if (Test-Path $winres) {
    & $winres make --arch amd64 --in winres/winres.json --out cmd/ipsend/rsrc
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} else {
    Write-Warning "未找到 go-winres，exe 将没有图标和版本信息"
}

go build -trimpath -ldflags "-H windowsgui -s -w" -o ipsend.exe ./cmd/ipsend
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Get-Item ipsend.exe | Select-Object Name, @{n='MB';e={[math]::Round($_.Length/1MB,1)}}
