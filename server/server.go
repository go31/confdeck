// Package server 提供配置中心的 HTTP API 与内嵌 UI。
//
// 只有服务端需要它：业务服务用根包里的 Client。
package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go31/confdeck/store"
)

// Options 配置服务端。
type Options struct {
	// Token 是唯一凭证：所有 /v1 接口都要求 Authorization: Bearer <token>。
	// 服务取值、注册 schema、运维改配置共用它（够用且少一个概念）；
	// 留空表示不校验 —— 只适合把端口绑在回环上的私网部署。
	Token string
	// Logger 为空则用 slog 默认 logger。
	Logger *slog.Logger
}

// Server 是配置中心的 HTTP 处理器。
type Server struct {
	store store.Store
	opts  Options
	log   *slog.Logger
	mux   *http.ServeMux
}

// New 创建服务端。
func New(st store.Store, opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{store: st, opts: opts, log: log, mux: http.NewServeMux()}

	// 探活与静态页面不鉴权（页面里没有密钥，数据都要靠 token 才能取）。
	s.mux.HandleFunc("GET /healthz", s.handleHealth)

	// 业务接口：全部走同一个 token。
	s.mux.HandleFunc("POST /v1/schema/{scope}", s.authorize(s.handlePutSchema))
	s.mux.HandleFunc("GET /v1/schema/{scope}", s.authorize(s.handleGetSchema))
	s.mux.HandleFunc("GET /v1/values/{scope}", s.authorize(s.handleGetValues))
	s.mux.HandleFunc("PUT /v1/values/{scope}", s.authorize(s.handlePutValues))
	s.mux.HandleFunc("DELETE /v1/values/{scope}/{key}", s.authorize(s.handleDeleteValue))
	s.mux.HandleFunc("GET /v1/overview", s.authorize(s.handleOverview))

	// 内嵌 UI。
	s.mux.Handle("GET /ui/", s.uiHandler())
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
	return s
}

// Handler 返回可挂到 http.Server 上的处理器。
func (s *Server) Handler() http.Handler { return s.mux }

// Insecure 报告是否完全没有开启鉴权（启动时应据此打印警告）。
func (s *Server) Insecure() bool { return s.opts.Token == "" }

// authorize 是唯一的鉴权入口：所有 /v1 接口都经过它。
func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Insecure() {
			next(w, r)
			return
		}
		token := bearerToken(r)
		switch {
		case token == "":
			writeError(w, http.StatusUnauthorized, "缺少 Authorization: Bearer <token>")
		case token != s.opts.Token:
			writeError(w, http.StatusForbidden, "token 不正确")
		default:
			next(w, r)
		}
	}
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return header[len(prefix):]
	}
	return ""
}

// ---------------------------------------------------------------- 响应工具

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeStoreError 把存储层错误映射成合适的状态码：非法输入是 400，其余是 500。
func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Error("存储操作失败", "error", err)
	writeError(w, http.StatusInternalServerError, err.Error())
}
