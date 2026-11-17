package assets

import (
	"embed"
	"io/fs"
	"strings"
)

// Mesa llvmpipe 软件 OpenGL（https://www.mesa3d.org，MIT 许可，见 mesa/LICENSE.txt），
// 来自 mmozeiko/build-mesa 的 x64 构建，DLL 以 gzip 压缩后内嵌。
//
//go:embed mesa
var mesaFS embed.FS

// MesaFiles 返回 文件名 → gzip 数据（只包含 *.dll.gz）。
func MesaFiles() map[string][]byte {
	out := map[string][]byte{}
	ents, _ := fs.ReadDir(mesaFS, "mesa")
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".dll.gz") {
			continue
		}
		b, err := mesaFS.ReadFile("mesa/" + e.Name())
		if err == nil {
			out[strings.TrimSuffix(e.Name(), ".gz")] = b
		}
	}
	return out
}
