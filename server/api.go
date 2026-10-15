package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/go31/confdeck"
)

const maxBody = 4 << 20 // 4MiB：配置里不会有这么大的东西，超了直接拒

// ---------------------------------------------------------------- 健康检查

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- schema

func (s *Server) handlePutSchema(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	doc, err := parseSchema(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.PutSchema(scope, body); err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.log.Info("已注册 schema", "scope", scope, "keys", len(doc.Properties))
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "keys": len(doc.Properties)})
}

func (s *Server) handleGetSchema(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	raw, err := s.store.Schema(scope)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if len(raw) == 0 {
		writeError(w, http.StatusNotFound, fmt.Sprintf("作用域 %s 还没有注册过 schema", scope))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// ---------------------------------------------------------------- 值

func (s *Server) handleGetValues(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	values, err := s.store.Values(scope)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if values == nil {
		values = map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "values": values})
}

func (s *Server) handlePutValues(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var payload struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式应为 {\"values\":{\"KEY\":\"值\"}}")
		return
	}
	if payload.Values == nil {
		writeError(w, http.StatusBadRequest, "缺少 values 字段")
		return
	}
	if err := s.store.PutValues(scope, payload.Values); err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.log.Info("已写入配置", "scope", scope, "keys", len(payload.Values))
	values, err := s.store.Values(scope)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "values": values})
}

func (s *Server) handleDeleteValue(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	key := r.PathValue("key")
	if err := s.store.PutValues(scope, map[string]string{key: ""}); err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.log.Info("已删除配置项", "scope", scope, "key", key)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- 概览（UI 与 CLI 用）

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	scopes, err := s.store.Scopes()
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	shared, err := s.store.Values(confdeck.SharedScope)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	// 先收集每个作用域的 schema 与值，便于后面判断「同一键在多个作用域里不一致」。
	type scopeData struct {
		doc    *schemaDoc
		values map[string]string
	}
	data := map[string]scopeData{}
	declaredIn := map[string][]string{} // key → 声明了它的服务作用域
	// 共享作用域里的键是由各服务的作用域声明的，所以「是否已声明」要看全局，
	// 否则 shared 里的每个键都会被误报成「未声明」。
	declaredAnywhere := map[string]bool{}
	// key → 把它标成「共享键」的服务作用域（Config.Shared）。
	// 这些键归到 shared 作用域展示一次，不在各服务作用域里重复。
	sharedDeclaredBy := map[string][]string{}
	for _, scope := range scopes {
		raw, err := s.store.Schema(scope)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		values, err := s.store.Values(scope)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		entry := scopeData{values: values}
		if len(raw) > 0 {
			doc, err := parseSchema(raw)
			if err != nil {
				s.log.Warn("已注册的 schema 无法解析", "scope", scope, "error", err)
			} else {
				entry.doc = doc
				for key, prop := range doc.Properties {
					declaredAnywhere[key] = true
					if scope != confdeck.SharedScope {
						declaredIn[key] = append(declaredIn[key], scope)
						if prop.XShared {
							sharedDeclaredBy[key] = append(sharedDeclaredBy[key], scope)
						}
					}
				}
			}
		}
		data[scope] = entry
	}

	// 同一键在多个作用域里被声明且值不同 → 标记出来（这正是「一边改了、另一边忘了」）。
	divergent := map[string]bool{}
	for key, owners := range declaredIn {
		if len(owners) < 2 {
			continue
		}
		seen := map[string]bool{}
		for _, scope := range owners {
			values := data[scope].values
			v, ok := values[key]
			if !ok {
				v, ok = shared[key]
			}
			if ok {
				seen[v] = true
			}
		}
		if len(seen) > 1 {
			divergent[key] = true
		}
	}

	// 共享键：由各服务声明、值配在 shared。它们的键列表来自**声明**而不是数据，
	// 所以只要有人声明了共享键，shared 作用域就必须出现 —— 哪怕它还一个值都没有。
	sharedKeys := make([]string, 0, len(sharedDeclaredBy))
	for key := range sharedDeclaredBy {
		sharedKeys = append(sharedKeys, key)
	}
	sort.Strings(sharedKeys)
	if len(sharedKeys) > 0 {
		hasShared := false
		for _, scope := range scopes {
			if scope == confdeck.SharedScope {
				hasShared = true
				break
			}
		}
		if !hasShared {
			scopes = append(scopes, confdeck.SharedScope)
			sort.Strings(scopes)
		}
	}

	// sharedKeyView 组装共享键在 shared 作用域里的那一行。
	//
	// 元信息取各声明者的并集：Required / Secret / External 用 OR（任何一方需要就得满足，
	// 少报会漏掉必填）；Type / Desc / Default 取第一个声明者（各服务的说明各写各的，
	// 界面展示一份，DeclaredIn 会列出全部声明者）。
	sharedKeyView := func(key string) confdeck.KeyView {
		owners := sharedDeclaredBy[key]
		item := confdeck.KeyView{
			Key:        key,
			Shared:     true,
			DeclaredIn: owners,
			Divergent:  divergent[key],
			// 有服务标了共享、有的没标 → 归属没谈拢，值得提醒。
			SharedMismatch: len(owners) < len(declaredIn[key]),
		}
		for i, owner := range owners {
			doc := data[owner].doc
			if doc == nil {
				continue
			}
			prop := doc.Properties[key]
			if i == 0 {
				item.Type = prop.XType
				item.Desc = prop.Description
				item.Default = stringify(prop.Default)
			}
			item.Required = item.Required || doc.required(key)
			item.Secret = item.Secret || prop.XSecret
			item.External = item.External || prop.XExternal
		}
		return item
	}

	resp := confdeck.Overview{Scopes: make([]confdeck.ScopeView, 0, len(scopes)), Insecure: s.Insecure()}
	alerts := map[string]bool{}
	for _, scope := range scopes {
		entry := data[scope]
		view := confdeck.ScopeView{Scope: scope, HasSchema: entry.doc != nil}

		seen := map[string]bool{}
		switch {
		case scope == confdeck.SharedScope:
			// 共享键只在这里展示一次。
			for _, key := range sharedKeys {
				seen[key] = true
				item := sharedKeyView(key)
				if v, ok := shared[key]; ok {
					item.Value, item.Present = v, true
				}
				if item.SharedMismatch {
					alerts[key] = true
				}
				if !item.External {
					if item.Required && !item.Present {
						view.Missing = append(view.Missing, key)
						alerts[key] = true
					}
					if isPlaceholder(item.Value) {
						view.Missing = append(view.Missing, key)
						alerts[key] = true
					}
				}
				if item.Divergent {
					alerts[key] = true
				}
				view.Keys = append(view.Keys, item)
			}

		case entry.doc != nil:
			keys := make([]string, 0, len(entry.doc.Properties))
			for key := range entry.doc.Properties {
				keys = append(keys, key)
			}
			sort.Strings(keys)

			for _, key := range keys {
				seen[key] = true
				// 共享键不在服务作用域里重复展示 —— 但本作用域要是存了一份值，
				// 它会按合并规则盖住 shared 的值，这种情况必须让人看见（否则就是静默的坑）。
				if len(sharedDeclaredBy[key]) > 0 {
					if v, ok := entry.values[key]; ok {
						item := sharedKeyView(key)
						item.Value, item.Present, item.Override = v, true, true
						alerts[key] = true
						view.Keys = append(view.Keys, item)
					}
					continue
				}

				prop := entry.doc.Properties[key]
				item := confdeck.KeyView{
					Key:        key,
					Type:       prop.XType,
					Desc:       prop.Description,
					Required:   entry.doc.required(key),
					Secret:     prop.XSecret,
					External:   prop.XExternal,
					Default:    stringify(prop.Default),
					Divergent:  divergent[key],
					DeclaredIn: declaredIn[key],
				}
				if v, ok := entry.values[key]; ok {
					item.Value, item.Present = v, true
				} else if v, ok := shared[key]; ok {
					item.Value, item.Present, item.FromShared = v, true, true
				}
				// External 的项由部署环境提供，中心看不到它的值 ——
				// 算进缺失就是永久假警报，所以这里跳过（进程侧的必填校验照样管）。
				if !item.External {
					if item.Required && !item.Present {
						view.Missing = append(view.Missing, key)
						alerts[key] = true
					}
					if isPlaceholder(item.Value) {
						view.Missing = append(view.Missing, key)
						alerts[key] = true
					}
				}
				if item.Divergent {
					alerts[key] = true
				}
				view.Keys = append(view.Keys, item)
			}
		}

		// 本作用域里有值、但这个作用域的 schema 没声明它。
		// 如果别的服务声明了它，说明这是一份「覆盖共享值」的本地值；谁都没声明才是真的漂移。
		extra := make([]string, 0)
		for key := range entry.values {
			if !seen[key] {
				extra = append(extra, key)
			}
		}
		sort.Strings(extra)
		for _, key := range extra {
			item := confdeck.KeyView{
				Key:        key,
				Value:      entry.values[key],
				Present:    true,
				Undeclared: !declaredAnywhere[key],
				DeclaredIn: declaredIn[key],
				Divergent:  divergent[key],
			}
			view.Keys = append(view.Keys, item)
			if item.Undeclared || item.Divergent {
				alerts[key] = true
			}
		}

		if len(view.Missing) > 0 {
			sort.Strings(view.Missing)
		}
		resp.Scopes = append(resp.Scopes, view)
	}

	for key := range alerts {
		resp.AlertKeys = append(resp.AlertKeys, key)
	}
	sort.Strings(resp.AlertKeys)
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------- schema 解析

type schemaDoc struct {
	Properties map[string]schemaProp
	Required   map[string]bool
}

type schemaProp struct {
	Description string `json:"description"`
	Default     any    `json:"default"`
	XSecret     bool   `json:"x-secret"`
	XExternal   bool   `json:"x-external"`
	XShared     bool   `json:"x-shared"`
	XType       string `json:"x-type"`
}

func (d *schemaDoc) required(key string) bool { return d.Required[key] }

func parseSchema(raw []byte) (*schemaDoc, error) {
	var payload struct {
		Properties map[string]schemaProp `json:"properties"`
		Required   []string              `json:"required"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("schema 不是合法 JSON：%w", err)
	}
	if payload.Properties == nil {
		return nil, fmt.Errorf("schema 缺少 properties 字段（应由 confdeck.Schema 生成）")
	}
	doc := &schemaDoc{Properties: payload.Properties, Required: map[string]bool{}}
	for _, key := range payload.Required {
		doc.Required[key] = true
	}
	return doc, nil
}

// ---------------------------------------------------------------- 小工具

func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败：%w", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("请求体过大（上限 %d 字节）", maxBody)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("请求体为空")
	}
	return body, nil
}

func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%v", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return string(raw)
	}
}

func isPlaceholder(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), confdeck.PlaceholderPrefix)
}
