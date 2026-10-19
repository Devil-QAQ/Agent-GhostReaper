// Package webui 承载内嵌的 Next.js 静态面板。
// 构建链见根目录 Makefile:web 构建产物(out/)同步到本包 dist/ 后编入二进制;
// dist/ 内置占位 index.html,保证未构建前端时 Go 编译与测试仍可通过。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler 返回面板静态文件服务;未命中的路径回退 index.html(供前端路由使用)。
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // embed 资源缺失属构建链断裂,启动即暴露
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
			if _, err := fs.Stat(sub, name); err != nil {
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}
