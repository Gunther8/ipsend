# ipsend 需求与协议定稿

只要知道对方 IP 就能直接发文件的 Windows 工具。不做设备发现，也不做 NAT 穿透。

## 1. 范围

| 场景 | 支持 |
|---|---|
| Windows ↔ Windows 互发（文件、多文件、文件夹） | ✅ |
| Linux 用 curl 向 Windows 发单个文件 | ✅（不能续传、不做哈希校验，只校验长度） |
| Windows 向 Linux 发送 | ❌ |
| 加密传输 | ❌ 明文 HTTP，只在可信局域网内使用 |
| 符号链接 | 跳过 |
| 设备发现、NAT 穿透 | ❌ |

## 2. 用户界面（M2 / M4）

- 普通窗口。主界面列出本机所有网卡的 IPv4 地址（可一键复制）、端口、接收目录。
- 发送区：输入框填 IP，下拉列出历史 IP（M4）；可选文件或文件夹，也能把文件拖到窗口上；显示进度、速度、剩余时间；可以取消。
- 最小化和关闭都会隐藏到托盘。托盘右键菜单有“打开”和“退出”两项。
- 单实例：用命名互斥锁 `Local\ipsend-singleton` 判断；第二个实例通过命名管道 `\\.\pipe\ipsend` 通知已有实例显示窗口，然后自己退出。
- 收到文件时弹出确认窗口：
  - 内容：对方 IP、对方机器名、文件数、总大小，前几个文件名，是否为续传（“已收 xx%”）
  - 按钮：接受 / 拒绝 / 拒绝并屏蔽此 IP
  - 60 秒内无人操作自动拒绝，标题栏显示倒计时
  - 多个请求排队，同一时间只显示一个
  - 弹窗是一个独立窗口，用 Win32 设为置顶并闪烁任务栏，同时发一条托盘通知
- 接收完成后发通知，带“打开文件夹”按钮。
- 设置（M4）：端口、接收目录、开机自启、PIN 码（可选）、屏蔽列表。
- 配置和日志放在 `%APPDATA%\ipsend\`（`config.json`、`history.json`、`ipsend.log`）。

## 3. HTTP 协议（默认端口 52100）

服务端只设 `ReadHeaderTimeout = 10s`，不设 `ReadTimeout`/`WriteTimeout`，避免大文件传到一半被断开。
所有 JSON 错误响应的格式都是 `{"error":"<code>","message":"<说明>"}`。

### 3.1 GET /api/ping
```json
{"app":"ipsend","version":"0.1.0","proto":1,"name":"PC-NAME"}
```
发送方先调用它，以区分三种情况：对方没运行、被防火墙拦截、版本不兼容。

### 3.2 POST /api/offer（长轮询，最长等 60 秒加排队时间）
请求：
```json
{
  "transfer_id": "32位hex",
  "sender_name": "PC-NAME",
  "files": [{"fid":0,"path":"照片/2024/a.jpg","size":123,"mtime":1700000000}],
  "dirs":  ["照片/空文件夹"]
}
```
- `transfer_id` 由客户端算出，等于 xxh3-128（各文件的绝对路径、size、mtime 排序后拼接）。同一批文件再发一次，ID 不变，接收方据此自动续传。
- `path` 使用 `/` 分隔的相对路径，根是用户选中项的名字（选中一个文件夹时，以文件夹名为根）。
- 请求体最大 16MB，最多 100000 个文件。

响应：
| 状态码 | 含义 |
|---|---|
| 200 | `{"sid":"32位hex","resume":true,"files":[{"fid":0,"offset":0}]}`，`offset` 是已收字节数 |
| 400 | 清单格式不合法 |
| 401 | 需要 PIN（M4） |
| 403 | 被拒绝、超时或已屏蔽 |
| 429 | 该 IP 排队请求过多（同一 IP 最多 3 个） |
| 503 | 全局队列已满（最多 10 个） |
| 507 | 磁盘剩余空间不足，或 FAT32 上有大于等于 4GiB 的文件 |

`sid` 是 128 位随机数，并绑定来源 IP；其他 IP 用这个 sid 请求一律返回 404。

### 3.3 GET /api/status/{sid}/{fid}
```json
{"received":1048576,"size":123456789,"done":false}
```

### 3.4 PUT /api/data/{sid}/{fid}?offset=N[&commit=<xxh3>]
- `N` 必须等于当前已收字节数，否则返回 `409 {"error":"offset_mismatch","received":M}`。
- body 是从 `N` 开始的原始字节，可以不传完（连接断开后再续）。超过声明大小的部分会被截断，并返回 400。
- 响应：`200 {"received":M}`。
- 可选参数 `commit`：数据收齐后立即按 §3.5 校验并落盘，响应与 commit 相同（`200 {"name":..,"received":..}` 或 422）。
  客户端对不超过 4MB 的文件先读入内存算好哈希，一次请求完成，并以 4 路并发发送；大文件仍然逐个流式发送。

### 3.5 POST /api/commit/{sid}/{fid}
请求：`{"xxh3":"32位hex(xxh3-128)"}`
- `200 {"name":"实际保存的相对路径"}`：校验通过，`.part` 已改名为正式文件，并设置了 mtime。
- `409 incomplete`：数据还没收完。
- `422 hash_mismatch`：哈希不符，`.part` 已删除，需要从 0 重传。

### 3.6 POST /api/finish/{sid}
创建空文件夹，清理会话目录。响应 `200 {"saved":[...]}`。

### 3.6.1 POST /api/cancel/{sid}
发送方取消。已收数据保留，可续传。接收方取消时，该会话的所有请求都返回 `410 cancelled`，客户端收到后停止发送，并提示“对方取消了接收”。

会话超过 30 秒没有任何请求时（对方崩溃、断网，或在拿到 sid 前就取消了），接收方界面显示“连接中断，等待对方重发”。
闲置超过 6 小时的会话会从内存中移除，但磁盘上的断点数据要到 7 天后才清理。

### 3.7 PUT /upload/{文件名}（curl 专用）
```
curl -T file --expect100-timeout 120 http://IP:52100/upload/file
```
- 必须带 `Content-Length`，没有就返回 411。
- 服务端收到请求头后先不读 body，弹窗确认；接受后才开始读，此时 Go 会自动回 `100 Continue`。拒绝返回 403。
- 文件名做 URL 解码，可以带子路径（`a/b.txt`），并按 §5 清洗。
- 收到的字节数不等于 `Content-Length` 时删除 `.part`。curl 上传不支持续传。
- 成功返回 `200`，正文为 `saved: <名字>\n`。

## 4. 哈希与续传

- 算法：xxh3-128（`github.com/zeebo/xxh3`），十六进制大端编码。
- 发送方边读边算；续传时先把 `[0,offset)` 读一遍算哈希，再接着发。
- 接收方边写边算。PUT 请求的 offset 大于 0、而内存里没有对应偏移的哈希状态时（比如程序重启过），先把已收部分重新哈希一遍。
- 断点数据统一放在 `<接收目录>\.ipsend\<transfer_id>\`：
  - `session.json`：文件清单和来源，接受时写一次，之后不再改写
  - `journal.log`：顶层目录映射和已完成的 fid，追加写入（避免文件很多时每提交一个就重写整个清单）
  - `<fid>.part`：未完成的文件数据
- 续传条件：`transfer_id` 相同，且清单里的 path、size、mtime 完全一致。续传也要弹窗，弹窗上标明“续传”。
- 超过 7 天没动过的会话目录在启动时清理。

## 5. 文件名与落盘安全

- 路径的每一段都要清洗：
  - 去掉 `..` 和 `.`
  - 去掉开头的 `/`、`\`、盘符（`C:`）、UNC 前缀
  - 把 `<>:"|?*` 和控制字符替换为 `_`（冒号尤其要处理，否则会写成 NTFS 备用数据流）
  - 去掉末尾的点和空格
  - 遇到 CON、PRN、AUX、NUL、COM1-9、LPT1-9（含带扩展名的形式）时，在前面加 `_`
  - 每段最多 200 字节
  - 清洗后为空的段改成 `_`
- 同一批文件里只差大小写的路径视为冲突，后出现的自动改名。
- 不覆盖已有文件：
  - 顶层文件夹与已有内容重名时，把整个文件夹改名为 `dir (1)`
  - 单个文件重名时改名为 `xxx (1).ext`
  - commit 时用 `MoveFileEx` 改名，不带 REPLACE_EXISTING 标志；失败就把编号加一再试
- 接受前检查磁盘：剩余空间必须不小于（总大小 − 已收字节数 + 64MB）。还要检查文件系统，FAT32 上有大于等于 4GiB 的文件时直接拒绝。

## 5.1 设置、PIN、历史（M4）

- `%APPDATA%\ipsend\config.json`：端口、接收目录、开机自启、PIN、屏蔽列表、是否拒绝过添加防火墙规则。
- `%APPDATA%\ipsend\history.json`：最近 30 个对方地址（含对方的 PIN 和机器名），发送区下拉选择。
- PIN：offer 和 `/upload` 都要校验，通过请求头 `X-Ipsend-Pin`（或查询参数 `pin`）提供。
  - 缺少 PIN 返回 `401 pin_required`，PIN 错误返回 `401 bad_pin`。
  - 同一 IP 连续错 5 次锁定 10 分钟，返回 `429 pin_locked`。
  - `/api/ping` 返回 `"pin": true`，界面据此提前弹出 PIN 输入框。
  - curl 用法：`curl -T file -H "X-Ipsend-Pin: <PIN>" http://IP:52100/upload/file`
- 开机自启：写入 `HKCU\...\Run\ipsend = "<exe>" --hidden`，启动时只显示托盘。exe 被移动后，下次启动时自动更新路径。

## 5.2 界面渲染

Fyne 需要 OpenGL 2.1。远程桌面会话、老显卡驱动、虚拟机下系统 OpenGL 不可用（AMD 老驱动还会弹 “LoadLibrary failed with error 87”），程序会直接退出。所以：

- 内置 Mesa llvmpipe 软件渲染（`assets/mesa/opengl32.dll.gz`，约 21MB），首次启动时释放到 `%LOCALAPPDATA%\ipsend\mesa-<hash>\`，在 GUI 初始化前按完整路径加载。
- `third_party/go-gl-gl` 是打过补丁的 go-gl，去掉了对 opengl32 的静态导入（原因见其 README）。
- 设置环境变量 `IPSEND_GL=native` 时，改用系统 OpenGL。

## 6. 防火墙（M4）

- 每次启动时通过防火墙 COM 接口 `HNetCfg.FwPolicy2` 检查规则（普通用户可读，与系统语言无关），按 exe 路径匹配，并区分允许和阻止规则。
  不用 `netsh show rule`，因为它的输出随系统语言变化；也不用 NetSecurity 模块，因为它需要管理员权限。
- 没有规则或路径不一致时，弹窗询问，再用 UAC 提权执行：
  `netsh advfirewall firewall add rule name=ipsend dir=in action=allow program="<exe>" profile=any`
- 用户拒绝 UAC 后记在配置里，以后不再主动询问（设置页里可以手动重试）。

## 7. 打包（M4）

- 编译：运行 `build.ps1`，需要 Go、MSYS2 ucrt64 gcc（CGO），可选 go-winres。
- 用 go-winres 嵌入图标、manifest（asInvoker、PerMonitorV2 DPI、长路径）和版本信息（`winres/winres.json`）。
- 单个 exe 约 51MB：程序 25MB，中文字体 5MB，软件渲染 21MB。
- 字体使用子集化的 Noto Sans SC（OFL 授权），通过 go:embed 嵌入。
- exe 未签名，会被 SmartScreen 拦截，需在说明里写明。

## 8. 里程碑与验收

### M1 收发核心 + CLI
完整实现 §3 协议，包括哈希、续传、文件夹（后续里程碑只加界面，不改协议）。用命令行确认接收：60 秒无人回答自动拒绝，多个请求排队。
- [ ] `ipsend recv` 启动后，用 curl 发 1 个文件成功；输入 n 时 curl 收到 403
- [ ] `ipsend send IP 文件 文件夹` 能收到文件，并保留目录结构和空文件夹
- [ ] 中文、空格、`..`、`CON.txt`、`a:b.txt` 等文件名都被正确清洗
- [ ] 同名文件改名为 `(1)`，不覆盖原文件
- [ ] 10GB 文件传到一半杀掉发送端，重发后从断点续传，哈希校验通过
- [ ] 剩余空间不足时返回 507

### M2 Fyne 界面 + 托盘 + 弹窗
- [ ] 关闭、最小化都隐藏到托盘；托盘菜单“打开/退出”能用
- [ ] 重复启动时唤起已有窗口
- [ ] 窗口隐藏时来请求，弹窗置顶；60 秒自动拒绝；多个请求排队
- [ ] 拖入文件即可发送，显示进度和速度

### M3 续传和大文件的界面与加固
- [ ] 界面上显示续传、重新哈希的进度；双方都能取消
- [ ] 上万个小文件的性能可以接受
- [ ] 过期会话自动清理

### M4 防火墙、设置、历史、打包
- [ ] 首次启动自动添加防火墙规则（触发 UAC）
- [ ] 端口、接收目录、开机自启、PIN 码、屏蔽列表都能保存并生效
- [ ] IP 历史下拉可用
- [ ] 打包成单个 exe，带图标
