package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go31/confdeck"
	"github.com/go31/confdeck/store/bolt"
)

const sampleSchema = `{
  "properties": {
    "LOG_LEVEL": {"type": "string", "description": "日志级别。", "default": "info", "x-type": "string"},
    "API_TOKEN": {"type": "string", "description": "调用上游 API 用的 token。", "x-type": "string", "x-secret": true},
    "WORKERS": {"type": "integer", "default": "8", "x-type": "int"}
  },
  "required": ["API_TOKEN"]
}`

func newTestServer(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	st, err := bolt.Open(filepath.Join(t.TempDir(), "confdeck.db"))
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ts := httptest.NewServer(New(st, opts).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, method, path, token, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("构造请求：%v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s：%v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestHealth(t *testing.T) {
	ts := newTestServer(t, Options{})
	if code, _ := call(t, ts, http.MethodGet, "/healthz", "", ""); code != http.StatusOK {
		t.Fatalf("healthz = %d", code)
	}
}

func TestOpenModeAllowsEverything(t *testing.T) {
	ts := newTestServer(t, Options{})
	if code, body := call(t, ts, http.MethodPut, "/v1/values/api", "",
		`{"values":{"LOG_LEVEL":"debug"}}`); code != http.StatusOK {
		t.Fatalf("开放模式写入应成功：%d %s", code, body)
	}
	if code, body := call(t, ts, http.MethodGet, "/v1/values/api", "", ""); code != http.StatusOK ||
		!strings.Contains(string(body), "debug") {
		t.Fatalf("开放模式读取应成功：%d %s", code, body)
	}
}

func TestTokenEnforcement(t *testing.T) {
	ts := newTestServer(t, Options{Token: "secret-token"})

	cases := []struct {
		name, method, path, token, body string
		want                            int
	}{
		{"无 token 读", http.MethodGet, "/v1/values/api", "", "", http.StatusUnauthorized},
		{"错 token 读", http.MethodGet, "/v1/values/api", "wrong", "", http.StatusForbidden},
		{"对 token 读", http.MethodGet, "/v1/values/api", "secret-token", "", http.StatusOK},
		{"对 token 写", http.MethodPut, "/v1/values/api", "secret-token", `{"values":{"A":"1"}}`, http.StatusOK},
		{"对 token 读概览", http.MethodGet, "/v1/overview", "secret-token", "", http.StatusOK},
		{"对 token 注册 schema", http.MethodPost, "/v1/schema/api", "secret-token", sampleSchema, http.StatusOK},
		{"无 token 写", http.MethodPut, "/v1/values/api", "", `{"values":{"A":"1"}}`, http.StatusUnauthorized},
		{"无 token 读概览", http.MethodGet, "/v1/overview", "", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := call(t, ts, tc.method, tc.path, tc.token, tc.body); code != tc.want {
				t.Fatalf("期望 %d，实际 %d：%s", tc.want, code, body)
			}
		})
	}
}

func TestUIAndHealthNeedNoToken(t *testing.T) {
	ts := newTestServer(t, Options{Token: "secret-token"})

	// 探活与静态页面不鉴权：页面里没有密钥，数据都要靠 token 才取得到。
	if code, _ := call(t, ts, http.MethodGet, "/healthz", "", ""); code != http.StatusOK {
		t.Fatalf("healthz 不该要 token")
	}

	code, body := call(t, ts, http.MethodGet, "/ui/", "", "")
	if code != http.StatusOK {
		t.Fatalf("/ui/ = %d", code)
	}
	if !strings.Contains(string(body), "confdeck") {
		t.Fatalf("页面内容不对：%s", body)
	}

	code, body = call(t, ts, http.MethodGet, "/ui/app.js", "", "")
	if code != http.StatusOK || !strings.Contains(string(body), "use strict") {
		t.Fatalf("app.js = %d %s", code, body)
	}

	// 根路径重定向到 UI（httptest 默认客户端会跟随重定向，所以这里断言最终是 200）
	if code, _ := call(t, ts, http.MethodGet, "/", "", ""); code != http.StatusOK {
		t.Fatalf("/ 应重定向到 UI")
	}
}

func TestSchemaRegistrationAndErrors(t *testing.T) {
	ts := newTestServer(t, Options{})

	if code, body := call(t, ts, http.MethodPost, "/v1/schema/api", "", sampleSchema); code != http.StatusOK {
		t.Fatalf("注册 schema：%d %s", code, body)
	}
	if code, body := call(t, ts, http.MethodGet, "/v1/schema/api", "", ""); code != http.StatusOK ||
		!strings.Contains(string(body), "properties") {
		t.Fatalf("读取 schema：%d %s", code, body)
	}
	if code, _ := call(t, ts, http.MethodGet, "/v1/schema/worker", "", ""); code != http.StatusNotFound {
		t.Fatal("未注册的 schema 应返回 404")
	}
	if code, _ := call(t, ts, http.MethodPost, "/v1/schema/api", "", "{不是 JSON"); code != http.StatusBadRequest {
		t.Fatal("非法 JSON 应返回 400")
	}
	if code, _ := call(t, ts, http.MethodPost, "/v1/schema/api", "", `{"type":"object"}`); code != http.StatusBadRequest {
		t.Fatal("缺少 properties 的 schema 应返回 400")
	}
	if code, _ := call(t, ts, http.MethodPost, "/v1/schema/BAD", "", sampleSchema); code != http.StatusBadRequest {
		t.Fatal("非法作用域名应返回 400")
	}
}

func TestValuesAndDelete(t *testing.T) {
	ts := newTestServer(t, Options{})

	code, body := call(t, ts, http.MethodPut, "/v1/values/api", "",
		`{"values":{"LOG_LEVEL":"debug","WORKERS":"16"}}`)
	if code != http.StatusOK {
		t.Fatalf("写入：%d %s", code, body)
	}
	if code, _ := call(t, ts, http.MethodDelete, "/v1/values/api/LOG_LEVEL", "", ""); code != http.StatusNoContent {
		t.Fatal("删除应返回 204")
	}
	_, body = call(t, ts, http.MethodGet, "/v1/values/api", "", "")
	if strings.Contains(string(body), "LOG_LEVEL") {
		t.Fatalf("删除后不应再出现：%s", body)
	}
	if code, _ := call(t, ts, http.MethodPut, "/v1/values/api", "", `{}`); code != http.StatusBadRequest {
		t.Fatal("缺少 values 应返回 400")
	}
}

func TestOverviewShowsMissingSharedUndeclaredAndDivergent(t *testing.T) {
	ts := newTestServer(t, Options{})

	// api 与 worker 都声明了 API_TOKEN（共享键）与 LOG_LEVEL
	if code, body := call(t, ts, http.MethodPost, "/v1/schema/api", "", sampleSchema); code != http.StatusOK {
		t.Fatalf("注册 api schema：%d %s", code, body)
	}
	workerSchema := strings.Replace(sampleSchema, `"LOG_LEVEL"`, `"WORKER_ONLY"`, 1)
	if code, body := call(t, ts, http.MethodPost, "/v1/schema/worker", "", workerSchema); code != http.StatusOK {
		t.Fatalf("注册 worker schema：%d %s", code, body)
	}

	// 共享作用域里放 API Token：两个服务都能读到
	if code, body := call(t, ts, http.MethodPut, "/v1/values/shared", "",
		`{"values":{"API_TOKEN":"shared-token"}}`); code != http.StatusOK {
		t.Fatalf("写入 shared：%d %s", code, body)
	}
	// api 作用域里再放一个「没在任何 schema 里声明」的键
	if code, body := call(t, ts, http.MethodPut, "/v1/values/api", "",
		`{"values":{"LEGACY_KEY":"x","WORKERS":"16"}}`); code != http.StatusOK {
		t.Fatalf("写入 api：%d %s", code, body)
	}
	// 两个作用域里都声明了 API_TOKEN，但 api 里给了不同的值 → 应标记不一致
	if code, body := call(t, ts, http.MethodPut, "/v1/values/api", "",
		`{"values":{"API_TOKEN":"api-own-token"}}`); code != http.StatusOK {
		t.Fatalf("写入 api：%d %s", code, body)
	}

	_, raw := call(t, ts, http.MethodGet, "/v1/overview", "", "")
	var ov confdeck.Overview
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatalf("解析概览：%v\n%s", err, raw)
	}
	if len(ov.Scopes) != 3 {
		t.Fatalf("应有 api / worker / shared 三个作用域：%s", raw)
	}

	byScope := map[string]confdeck.ScopeView{}
	for _, s := range ov.Scopes {
		byScope[s.Scope] = s
	}

	// 共享作用域里的键是由各服务声明的，不该被误报成「未声明」
	sharedScope := byScope[confdeck.SharedScope]
	if sharedScope.HasSchema {
		t.Error("shared 没有自己的 schema")
	}
	for _, k := range sharedScope.Keys {
		if k.Undeclared {
			t.Errorf("shared 里的 %s 被误报为未声明（它由 api/worker 声明）", k.Key)
		}
		if len(k.DeclaredIn) != 2 {
			t.Errorf("shared 里的 %s 应显示被 api 与 worker 声明，实际 %v", k.Key, k.DeclaredIn)
		}
	}

	worker := byScope["worker"]
	if !worker.HasSchema || len(worker.Missing) != 0 {
		t.Errorf("worker 不应缺配置：%+v", worker)
	}
	var sharedKey *confdeck.KeyView
	for i := range worker.Keys {
		if worker.Keys[i].Key == "API_TOKEN" {
			sharedKey = &worker.Keys[i]
		}
	}
	if sharedKey == nil || !sharedKey.Present || !sharedKey.FromShared || sharedKey.Value != "shared-token" {
		t.Errorf("worker 的共享键应从 shared 取值：%+v", sharedKey)
	}
	if sharedKey != nil && !sharedKey.Divergent {
		t.Error("api 里给了不同的值，应标记不一致")
	}

	api := byScope["api"]
	var undeclared *confdeck.KeyView
	for i := range api.Keys {
		if api.Keys[i].Key == "LEGACY_KEY" {
			undeclared = &api.Keys[i]
		}
	}
	if undeclared == nil || !undeclared.Undeclared {
		t.Errorf("中心里有值但没声明的键应被标记：%+v", undeclared)
	}
	if len(api.Missing) != 0 {
		t.Errorf("api 的必填项都配了：%v", api.Missing)
	}
}

// 共享键（x-shared）：只在 shared 作用域展示一次，不在各服务作用域里重复；
// shared 作用域即使还没有任何值也要出现（键列表来自声明）。
func TestOverviewShowsSharedKeysOnlyUnderShared(t *testing.T) {
	ts := newTestServer(t, Options{})

	apiSchema := `{
	  "properties": {
	    "API_TOKEN": {"type": "string", "description": "两个服务共用。", "x-type": "string", "x-secret": true, "x-shared": true},
	    "HTTP_PORT": {"type": "integer", "x-type": "int"}
	  },
	  "required": ["API_TOKEN"]
	}`
	workerSchema := `{
	  "properties": {
	    "API_TOKEN": {"type": "string", "description": "两个服务共用。", "x-type": "string", "x-secret": true, "x-shared": true},
	    "PUBLIC_URL": {"type": "string", "x-type": "string"}
	  },
	  "required": ["API_TOKEN"]
	}`
	for scope, schema := range map[string]string{"api": apiSchema, "worker": workerSchema} {
		if code, body := call(t, ts, http.MethodPost, "/v1/schema/"+scope, "", schema); code != http.StatusOK {
			t.Fatalf("注册 %s schema：%d %s", scope, code, body)
		}
	}

	overview := func() confdeck.Overview {
		t.Helper()
		_, raw := call(t, ts, http.MethodGet, "/v1/overview", "", "")
		var ov confdeck.Overview
		if err := json.Unmarshal(raw, &ov); err != nil {
			t.Fatalf("解析概览：%v\n%s", err, raw)
		}
		return ov
	}
	scopeOf := func(ov confdeck.Overview, name string) (confdeck.ScopeView, bool) {
		for _, s := range ov.Scopes {
			if s.Scope == name {
				return s, true
			}
		}
		return confdeck.ScopeView{}, false
	}
	hasKey := func(s confdeck.ScopeView, key string) bool {
		for _, k := range s.Keys {
			if k.Key == key {
				return true
			}
		}
		return false
	}

	// 1) 一个值都没配时：shared 作用域已经出现，且带着那一行共享键。
	ov := overview()
	shared, ok := scopeOf(ov, confdeck.SharedScope)
	if !ok {
		t.Fatalf("有共享键声明时 shared 作用域就该出现：%+v", ov.Scopes)
	}
	if !hasKey(shared, "API_TOKEN") {
		t.Error("shared 下应列出共享键")
	}
	if len(shared.Missing) != 1 || shared.Missing[0] != "API_TOKEN" {
		t.Errorf("共享键必填没配时应在 shared 里报缺失：%v", shared.Missing)
	}
	for _, name := range []string{"api", "worker"} {
		s, _ := scopeOf(ov, name)
		if hasKey(s, "API_TOKEN") {
			t.Errorf("%s 作用域不该重复展示共享键", name)
		}
	}

	// 2) 把值写进 shared：两边都算配好了，且不在服务作用域里多出副本。
	if code, body := call(t, ts, http.MethodPut, "/v1/values/"+confdeck.SharedScope, "",
		`{"values":{"API_TOKEN":"tok"}}`); code != http.StatusOK {
		t.Fatalf("写 shared：%d %s", code, body)
	}
	ov = overview()
	shared, _ = scopeOf(ov, confdeck.SharedScope)
	if len(shared.Missing) != 0 {
		t.Errorf("配好之后 shared 不该再报缺失：%v", shared.Missing)
	}
	for _, name := range []string{"api", "worker"} {
		s, _ := scopeOf(ov, name)
		if hasKey(s, "API_TOKEN") {
			t.Errorf("配好之后 %s 里也不该出现共享键", name)
		}
	}

	// 3) 服务作用域里塞一份覆盖值：它会被展示出来（会盖住 shared 的值），并进 alertKeys。
	if code, body := call(t, ts, http.MethodPut, "/v1/values/api", "",
		`{"values":{"API_TOKEN":"override"}}`); code != http.StatusOK {
		t.Fatalf("写 api：%d %s", code, body)
	}
	ov = overview()
	api, _ := scopeOf(ov, "api")
	var override *confdeck.KeyView
	for i := range api.Keys {
		if api.Keys[i].Key == "API_TOKEN" {
			override = &api.Keys[i]
		}
	}
	if override == nil || !override.Override {
		t.Fatalf("服务作用域里的覆盖值应被展示并标记：%+v", api.Keys)
	}
	if override.Value != "override" {
		t.Errorf("覆盖值应显示本作用域的值：%+v", override)
	}
	found := false
	for _, key := range ov.AlertKeys {
		if key == "API_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Errorf("覆盖值应进 alertKeys：%v", ov.AlertKeys)
	}
}

// 同一个键有的服务标了 Shared、有的没标 → 归属没谈拢，要提醒。
func TestOverviewFlagsSharedMismatch(t *testing.T) {
	ts := newTestServer(t, Options{})

	apiSchema := `{"properties":{"LOG_LEVEL":{"type":"string","x-type":"string","x-shared":true,"description":"日志级别。"}}}`
	workerSchema := `{"properties":{"LOG_LEVEL":{"type":"string","x-type":"string","description":"日志级别。"}}}`

	for scope, schema := range map[string]string{"api": apiSchema, "worker": workerSchema} {
		if code, body := call(t, ts, http.MethodPost, "/v1/schema/"+scope, "", schema); code != http.StatusOK {
			t.Fatalf("注册 %s schema：%d %s", scope, code, body)
		}
	}

	_, raw := call(t, ts, http.MethodGet, "/v1/overview", "", "")
	var ov confdeck.Overview
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatalf("解析概览：%v", err)
	}

	var mismatched bool
	for _, s := range ov.Scopes {
		for _, k := range s.Keys {
			if k.Key == "LOG_LEVEL" && k.SharedMismatch {
				mismatched = true
			}
		}
	}
	if !mismatched {
		t.Errorf("归属不一致应被标记：%s", raw)
	}
}

func TestOverviewMarksMissingRequired(t *testing.T) {
	ts := newTestServer(t, Options{})
	if code, _ := call(t, ts, http.MethodPost, "/v1/schema/api", "", sampleSchema); code != http.StatusOK {
		t.Fatal("注册 schema 失败")
	}
	_, raw := call(t, ts, http.MethodGet, "/v1/overview", "", "")
	var ov confdeck.Overview
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatalf("解析概览：%v", err)
	}
	if len(ov.Scopes) != 1 || len(ov.Scopes[0].Missing) != 1 || ov.Scopes[0].Missing[0] != "API_TOKEN" {
		t.Fatalf("应标出缺失的必填项：%s", raw)
	}
	if len(ov.AlertKeys) != 1 || ov.AlertKeys[0] != "API_TOKEN" {
		t.Fatalf("AlertKeys 应包含缺失项：%s", raw)
	}
}

// x-external 的键（由部署环境提供）不该被算进缺失，也不该进 AlertKeys ——
// 中心根本看不到进程环境，算进去就是永久假警报。
func TestOverviewExcludesExternalFromMissing(t *testing.T) {
	ts := newTestServer(t, Options{})

	schema := `{
	  "properties": {
	    "DB_URL": {"type": "string", "description": "连接串。", "x-type": "string", "x-secret": true, "x-external": true},
	    "API_TOKEN": {"type": "string", "x-type": "string", "x-secret": true}
	  },
	  "required": ["API_TOKEN"]
	}`
	if code, body := call(t, ts, http.MethodPost, "/v1/schema/api", "", schema); code != http.StatusOK {
		t.Fatalf("注册 schema：%d %s", code, body)
	}

	_, raw := call(t, ts, http.MethodGet, "/v1/overview", "", "")
	var ov confdeck.Overview
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatalf("解析概览：%v", err)
	}
	if len(ov.Scopes) != 1 {
		t.Fatalf("作用域数不对：%s", raw)
	}
	scope := ov.Scopes[0]

	if len(scope.Missing) != 1 || scope.Missing[0] != "API_TOKEN" {
		t.Errorf("只该缺 API_TOKEN（DB_URL 是 external）：%v", scope.Missing)
	}
	if len(ov.AlertKeys) != 1 || ov.AlertKeys[0] != "API_TOKEN" {
		t.Errorf("AlertKeys 只该有 API_TOKEN：%v", ov.AlertKeys)
	}

	var dbURL *confdeck.KeyView
	for i := range scope.Keys {
		if scope.Keys[i].Key == "DB_URL" {
			dbURL = &scope.Keys[i]
		}
	}
	if dbURL == nil {
		t.Fatalf("概览里应列出 DB_URL：%s", raw)
	}
	if !dbURL.External {
		t.Error("KeyView.External 应为 true（UI 靠它显示「部署提供」并禁止编辑）")
	}
	if dbURL.Required {
		t.Error("schema 的 required 里没有它，所以 Required 应为 false")
	}
}
