package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// UI 是内嵌在二进制里的页面：原生 JS + CSS，没有构建步骤，
// 所以改一行样式不需要 Node，也不需要单独部署静态资源。
//
//go:embed web
var uiFS embed.FS

func (s *Server) uiHandler() http.Handler {
	sub, err := fs.Sub(uiFS, "web")
	if err != nil {
		panic("confdeck: 内嵌 UI 缺失：" + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 静态资源不做长缓存：升级二进制后刷新就能拿到新界面。
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		http.StripPrefix("/ui/", files).ServeHTTP(w, r)
	})
}
