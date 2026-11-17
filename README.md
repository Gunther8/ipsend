# ipsend

知道对方 IP 就能直接发文件的 Windows 小工具。单个 exe，不用安装，不用登录，不经过任何服务器。

- Windows 之间可以互发文件、多个文件和整个文件夹（保留目录结构）
- Linux 可以用 curl 向 Windows 发送
- 每次接收都要你确认：弹窗显示对方 IP、文件数和大小，60 秒没人点就自动拒绝
- 支持 10GB 以上的大文件：断了能续传，传完用 xxh3 校验，接收前检查磁盘空间
- 不会覆盖已有文件：遇到同名自动改名为 `xxx (1).ext`
- 关闭窗口后最小化到托盘，继续在后台接收

> 只适合在可信的局域网里用。传输是明文 HTTP，没有加密。

---

## 快速开始

1. 两台电脑都运行 `ipsend.exe`。
2. 第一次启动时，程序会询问是否添加 Windows 防火墙规则。点 **添加规则**，并在管理员权限确认（UAC）窗口中选"是"。不添加的话，别的电脑可能连不进来。
3. 在**接收方**窗口顶部找到本机 IP（蓝框那个通常就是局域网地址），点一下就复制了，把它告诉发送方。
4. **发送方**在"对方 IP"里填上这个地址，把文件或文件夹拖进虚线框，点 **发送**。
5. 接收方会弹出确认窗口，点 **接受** 后开始传输。文件默认保存在 `下载\ipsend` 文件夹。

---

## 界面说明

### 顶部状态栏

| 状态 | 含义 | 点击后 |
|---|---|---|
| 🟢 接收中 · 52100 · PIN | 正常接收中（设了 PIN 才会显示 PIN） | 打开设置 |
| 🟡 防火墙未放行 | 其他电脑可能连不进来 | 引导添加防火墙规则 |
| 🔴 端口无法监听 | 端口被其他程序占用 | 打开设置换个端口 |

右上角的 ⚙ 打开设置。

### 本机地址

列出本机所有网卡的 IPv4 地址，点击即复制。VMware、Hyper-V、WSL、ZeroTier 这类虚拟网卡默认收起，点"+N 虚拟网卡"可以展开。

### 发送

- **对方 IP**：可以带端口，例如 `192.168.1.20:52100`。右边的下拉框里有最近发送过的地址，会附上对方的电脑名。
- **添加文件**：把文件或文件夹拖进虚线框，或者点"添加文件"/"添加文件夹"。列表里会显示每一项的大小，文件夹会自动统计文件数。
- **发送过程**：显示百分比、已传大小、速度、剩余时间和文件数，可以随时取消。
- **断点续传**：传到一半中断了（取消、断网、对方关机都算），只要对方接收目录里的断点数据还在，**再发一次同样的文件就会从断点继续**。弹窗里会提示"断点续传：已收 xx%"。

### 接收

- 有请求时会弹出置顶的确认窗口，同时发一条系统通知：
  - **接受**：开始接收
  - **拒绝**：这次不收
  - **屏蔽此 IP**：以后来自这个 IP 的请求一律自动拒绝（可以在设置里解除）
  - 60 秒没人操作就自动拒绝；同时有多个请求时会排队，一个一个弹出
- 正在进行的接收每个占一行，左侧色条表示状态：蓝色是接收中，绿色是完成，黄色是连接中断，灰色是已取消。点 ✕ 可以取消接收，已收的部分会保留，对方重发时可以续传。
- 下方是接收记录。点"打开目录"打开接收文件夹。

### 托盘

- 点窗口的关闭或最小化按钮，程序都会缩到托盘，继续接收。
- 左键点托盘图标打开窗口，右键菜单里有"打开"和"退出"。
- 程序只会运行一份。再次双击 exe，只会把已经在运行的窗口调出来。

---

## 设置

点右上角 ⚙ 打开。

| 项目 | 说明 |
|---|---|
| 端口 | 默认 `52100`，只用 TCP。改了以后要告诉发送方新端口 |
| 接收目录 | 默认 `%USERPROFILE%\Downloads\ipsend` |
| 开机自动启动 | 开机后只显示托盘图标，在后台等着接收 |
| PIN | 设置后，对方发送时必须输入正确的 PIN。留空表示不需要。4～32 位字母或数字 |
| 防火墙 | 显示当前是否放行，可以重新添加规则 |
| 屏蔽的 IP | 被屏蔽的地址列表，可以逐个解除 |

**关于 PIN**

- 发送时如果对方要求 PIN，会弹框让你输入。输对一次后会记在 IP 历史里，下次不用再输。
- 同一个 IP 连续输错 5 次会被锁 10 分钟。

---

## 从 Linux 用 curl 发送

```bash
curl -T 文件名 --expect100-timeout 120 http://对方IP:52100/upload/文件名
```

- 对方设置了 PIN 时，加上 `-H "X-Ipsend-Pin: PIN"`。
- 为什么要加 `--expect100-timeout 120`：Windows 端要等用户点"接受"后才开始收数据。curl 默认只等 1 秒确认，到点就直接开始上传；加上这个参数，curl 会最多等 120 秒。
- **老版本 curl**（7.47 以前，例如 CentOS 7 自带的 7.29）不认识这个参数，会报 `option --expect100-timeout: is unknown`。直接去掉这个参数就行：

```bash
curl -T 文件名 http://对方IP:52100/upload/文件名
```

  去掉后照样能传。curl 等 1 秒后会开始发数据，但对方点"接受"之前 ipsend 不会读取，数据塞满网络缓冲区后 curl 会自己停下来等。区别只是被拒绝时，老版本 curl 可能报"连接被重置"，而不是显示 `rejected by receiver`。查看 curl 版本：`curl --version`。
- 被拒绝时返回 `403`，需要 PIN 时返回 `401`。
- 文件名可以带子目录，例如 `.../upload/照片/a.jpg`，会保存到接收目录下的 `照片\a.jpg`。
- curl 方式**不支持续传**，中断了要重新发。
- 必须是普通文件。`tar ... | curl -T -` 这种管道上传没有文件长度，会被拒绝（返回 411）。要发文件夹，可以先打包：

```bash
tar czf dir.tgz 目录 && curl -T dir.tgz --expect100-timeout 120 http://对方IP:52100/upload/dir.tgz
```

---

## 命令行用法

同一个 exe 也可以在命令行里用，适合写脚本或排查问题：

```bash
ipsend.exe recv -dir D:\收件 -port 52100 -pin 1234
```

```bash
ipsend.exe send -pin 1234 192.168.1.20 D:\资料 D:\视频.mp4
```

| 命令 | 参数 |
|---|---|
| `ipsend` | 启动图形界面；`--hidden` 表示启动时只显示托盘 |
| `ipsend recv` | `-port` 端口，`-dir` 接收目录，`-pin` 要求对方提供的 PIN，`-yes` 自动接受所有请求（仅测试用） |
| `ipsend send` | `-port` 对方端口（地址里没写端口时使用），`-pin` 对方要求的 PIN，后面跟 IP 和要发的文件或文件夹 |

命令行模式下，`recv` 收到请求时会在终端里询问 `y/n/b`（接受/拒绝/拒绝并屏蔽），60 秒不回答就自动拒绝。

---

## 安全

- **文件名清洗**：对方传来的路径会去掉 `..`、盘符和绝对路径，替换掉 `<>:"|?*` 和控制字符，`CON`、`NUL`、`COM1` 这类 Windows 保留名会自动加前缀。文件无法写到接收目录以外。
- **不覆盖**：遇到同名文件改名为 `xxx (1).ext`；同名的文件夹整个改名为 `文件夹 (1)`。
- **会话绑定**：每次传输有一个随机的会话号，并且只认发起方的 IP。
- **防骚扰**：同一个 IP 最多同时排队 3 个请求，全局最多 10 个。
- **没有加密**：局域网里的其他人理论上能截获传输内容。不要在公共网络中使用。

---

## 常见问题

**对方提示"连接超时：IP 不可达，或被对方防火墙拦截"**
1. 看接收方顶部状态栏是不是黄色的"防火墙未放行"，是的话点它添加规则。
2. 确认两台电脑在同一个局域网里，并且 IP 填对了。
3. 在发送方电脑上测试端口通不通：
```bash
powershell -Command "Test-NetConnection 对方IP -Port 52100"
```
`TcpTestSucceeded : True` 表示端口是通的。
4. 公司网络中，交换机或路由器可能有访问控制，需要放行 **TCP 52100**。

**提示"连接被拒绝：对方未运行 ipsend 或端口不对"**
对方没开 ipsend，或者在设置里改了端口。

**远程桌面里打开报 "LoadLibrary failed with error 87"**
当前版本已经解决。程序内置了软件渲染，不依赖显卡驱动，远程桌面、老显卡、虚拟机下都能正常显示。如果你在这些环境以外遇到界面显示问题，可以设置环境变量 `IPSEND_GL=native` 改回使用系统显卡。

**断点续传没有生效，又从头开始了**
续传要求发送的是**同一批文件**：路径、大小、修改时间都没变，而且接收方 7 天内没有清理断点数据。文件被修改过、换了一批文件，或者用 curl 发送，都会从头开始。

**接收目录里有个隐藏的 `.ipsend` 文件夹**
里面是没传完的断点数据（`.part` 文件）。7 天没动过的会自动清理，也可以在不接收的时候手动删掉。

**exe 为什么有 50MB**
大约 25MB 是程序本身，5MB 是内嵌的中文字体，21MB 是内嵌的软件渲染库。软件渲染库是为了远程桌面和老显卡下也能正常显示。

**系统提示"Windows 已保护你的电脑"**
exe 没有数字签名，SmartScreen 会拦一下。点"更多信息"→"仍要运行"即可。

---

## 文件位置

| 内容 | 位置 |
|---|---|
| 设置 | `%APPDATA%\ipsend\config.json` |
| IP 历史（含对方 PIN） | `%APPDATA%\ipsend\history.json` |
| 日志（超过 5MB 自动清空） | `%APPDATA%\ipsend\ipsend.log` |
| 软件渲染库（首次运行时释放） | `%LOCALAPPDATA%\ipsend\mesa-*` |
| 断点数据 | `<接收目录>\.ipsend\` |
| 开机自启 | 注册表 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\ipsend` |
| 防火墙规则 | 名为 `ipsend` 的入站规则，按程序路径放行 |

**卸载**：在托盘里点"退出"，关掉"开机自动启动"，删除 exe 和上面这些文件夹。防火墙规则可以用下面的命令删除（需要管理员权限）：

```bash
netsh advfirewall firewall delete rule name=ipsend
```

**移动 exe 的位置之后**：防火墙规则是按程序路径匹配的，下次启动时会重新提示添加；开机自启的路径会自动更新。

---

## 从源码编译

需要：
- [Go](https://go.dev) 1.27 以上；
- MinGW-w64 gcc，Fyne 需要 CGO。推荐用 MSYS2 的 `mingw-w64-ucrt-x86_64-gcc`；
- 可选：`go install github.com/tc-hib/go-winres@latest`，用来嵌入图标和版本信息。

```bash
powershell -ExecutionPolicy Bypass -File build.ps1
```

`build.ps1` 默认到 `%LOCALAPPDATA%\Programs\msys64\ucrt64\bin` 找 gcc，生成的 `ipsend.exe` 在项目根目录。

运行测试：

```bash
go test ./internal/...
```

调试用的环境变量：

| 变量 | 作用 |
|---|---|
| `IPSEND_GL=native` | 不用内置的软件渲染，改用系统 OpenGL |
| `IPSEND_RATE_MBPS=100` | 发送限速（MB/s），方便观察进度 |
| `IPSEND_DEBUG=1` | 输出重试原因 |

### 目录结构

```
cmd/ipsend/        程序入口（图形界面 / recv / send）
internal/proto/    HTTP 协议数据结构
internal/server/   接收端：确认排队、断点续传、哈希校验、PIN
internal/client/   发送端：小文件并发、大文件流式续传
internal/fsutil/   文件名清洗、防覆盖改名、磁盘空间检查
internal/ui/       Fyne 界面
internal/sys/      单实例、托盘窗口钩子、防火墙、开机自启、软件渲染加载
internal/config/   设置和 IP 历史
assets/            字体、图标、软件渲染库
third_party/       打过补丁的 go-gl（见其中 README）
winres/            exe 图标、manifest、版本信息
```

协议和设计细节见 [SPEC.md](SPEC.md)。

---

## 许可与致谢

- 界面：[Fyne](https://fyne.io)（BSD-3）
- 字体：[Noto Sans SC](https://fonts.google.com/noto/specimen/Noto+Sans+SC)（SIL OFL 1.1，见 `assets/OFL.txt`）
- 软件渲染：[Mesa 3D](https://www.mesa3d.org) llvmpipe，[mmozeiko/build-mesa](https://github.com/mmozeiko/build-mesa) 构建（MIT，见 `assets/mesa/LICENSE.txt`）
- 哈希：[zeebo/xxh3](https://github.com/zeebo/xxh3)
- 原生文件对话框：[ncruces/zenity](https://github.com/ncruces/zenity)
