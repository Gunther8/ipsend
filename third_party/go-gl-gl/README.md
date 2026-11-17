# go-gl/gl（ipsend 补丁版）

来源：`github.com/go-gl/gl@v0.0.0-20260331235117-4566fea9a276`，MIT 许可（见 LICENSE）。
只保留 Fyne 用到的 `v2.1/gl` 和 `v3.1/gles2`，通过根目录 go.mod 的 `replace` 引用。

## 改动（仅 v2.1/gl）

- 去掉 Windows 下的 `-lopengl32`：exe 不再静态导入 `OPENGL32.dll`。
- `wglGetProcAddress` 改为运行时从 `LoadLibraryA("opengl32.dll")` 取得。

## 为什么

远程桌面会话、老显卡驱动下系统 OpenGL 不可用（AMD 老驱动还会弹 “LoadLibrary failed with error 87”），
Fyne 初始化失败后程序直接退出。ipsend 内置 Mesa llvmpipe 软件渲染，启动时先按完整路径加载它的 opengl32.dll；
静态导入会让系统 opengl32 抢先加载，所以必须去掉。
