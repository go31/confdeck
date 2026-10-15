package confdeck_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go31/confdeck"
	"github.com/go31/confdeck/server"
	"github.com/go31/confdeck/store/bolt"
)

// 一个典型的服务侧声明。
type Vars struct {
	LogLevel confdeck.Config[string]
	Workers  confdeck.Config[int]
	APIToken confdeck.Config[string]
}

func newSpec() Vars {
	return Vars{
		LogLevel: confdeck.Config[string]{Name: "LOG_LEVEL", Default: "info", Desc: "日志级别。"},
		Workers:  confdeck.Config[int]{Name: "WORKERS", Default: 8, Validate: confdeck.Positive},
		APIToken: confdeck.Config[string]{Name: "API_TOKEN", Required: true, Secret: true},
	}
}

func newCenter(t *testing.T) *httptest.Server {
	t.Helper()

	st, err := bolt.Open(filepath.Join(t.TempDir(), "confdeck.db"))
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ts := httptest.NewServer(server.New(st, server.Options{
		Token: "secret-token",
	}).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestServiceRegistersSchemaThenLoadsValues(t *testing.T) {
	ts := newCenter(t)
	ctx := context.Background()

	svc := confdeck.NewClient(ts.URL, confdeck.WithToken("secret-token"))
	admin := confdeck.NewClient(ts.URL, confdeck.WithToken("secret-token"))

	// 1) 服务启动时注册自己的 schema —— 中心据此知道「应该有哪些配置」
	spec := newSpec()
	if err := svc.Register(ctx, "api", &spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 2) 运维在中心里填值：共享的放 shared，服务专属的放 api
	if err := admin.PutValues(ctx, confdeck.SharedScope, map[string]string{
		"API_TOKEN": "shared-token",
	}); err != nil {
		t.Fatalf("写 shared: %v", err)
	}
	if err := admin.PutValues(ctx, "api", map[string]string{
		"WORKERS":   "16",
		"LOG_LEVEL": "debug",
	}); err != nil {
		t.Fatalf("写 api: %v", err)
	}

	// 3) 服务取值：只给中心这一个来源
	v := spec
	if err := confdeck.Load(&v, confdeck.WithSource(svc.Source("api"))); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.LogLevel.Value != "debug" || v.Workers.Value != 16 {
		t.Errorf("本作用域的值没生效：%q %d", v.LogLevel.Value, v.Workers.Value)
	}
	if v.APIToken.Value != "shared-token" || !v.APIToken.Provided() {
		t.Errorf("共享作用域的值没生效：%q", v.APIToken.Value)
	}
}

func TestScopeOverridesShared(t *testing.T) {
	ts := newCenter(t)
	ctx := context.Background()

	admin := confdeck.NewClient(ts.URL, confdeck.WithToken("secret-token"))
	if err := admin.PutValues(ctx, confdeck.SharedScope, map[string]string{"LOG_LEVEL": "warn"}); err != nil {
		t.Fatal(err)
	}
	if err := admin.PutValues(ctx, "api", map[string]string{"LOG_LEVEL": "debug"}); err != nil {
		t.Fatal(err)
	}

	svc := confdeck.NewClient(ts.URL, confdeck.WithToken("secret-token"))
	v := newSpec()
	if err := confdeck.Load(&v,
		confdeck.WithSource(confdeck.FromMap(map[string]string{"API_TOKEN": "tok"})),
		confdeck.WithSource(svc.Source("api")),
	); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.LogLevel.Value != "debug" {
		t.Errorf("本作用域应覆盖 shared，得到 %q", v.LogLevel.Value)
	}
}

func TestEnvSourceBeatsCenter(t *testing.T) {
	ts := newCenter(t)
	ctx := context.Background()

	admin := confdeck.NewClient(ts.URL, confdeck.WithToken("secret-token"))
	if err := admin.PutValues(ctx, "api", map[string]string{"LOG_LEVEL": "debug"}); err != nil {
		t.Fatal(err)
	}

	svc := confdeck.NewClient(ts.URL, confdeck.WithToken("secret-token"))
	v := newSpec()
	if err := confdeck.Load(&v,
		confdeck.WithSource(confdeck.FromMap(map[string]string{
			"LOG_LEVEL": "error", // 应急覆盖：进程环境优先
			"API_TOKEN": "tok",
		})),
		confdeck.WithSource(svc.Source("api")),
	); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.LogLevel.Value != "error" {
		t.Errorf("进程环境应优先于中心，得到 %q", v.LogLevel.Value)
	}
}

func TestUnreachableCenter(t *testing.T) {
	// 127.0.0.1:1 上不会有东西在监听 → 连接被拒
	dead := confdeck.NewClient("http://127.0.0.1:1", confdeck.WithToken("x"))

	t.Run("默认视为致命错误", func(t *testing.T) {
		v := newSpec()
		err := confdeck.Load(&v, confdeck.WithSource(dead.Source("api")))
		if err == nil || !strings.Contains(err.Error(), "读取配置来源") {
			t.Fatalf("中心不可达应报错：%v", err)
		}
	})

	t.Run("注册降级回调后继续，并由校验兜住", func(t *testing.T) {
		var gotSource string
		var gotErr error

		v := newSpec()
		err := confdeck.Load(&v,
			confdeck.WithSource(dead.Source("api")),
			confdeck.WithSourceErrorHandler(func(source string, err error) {
				gotSource, gotErr = source, err
			}),
		)
		if gotErr == nil {
			t.Fatal("应把来源错误交给回调")
		}
		if gotSource != "confdeck/api" {
			t.Errorf("来源名 = %q", gotSource)
		}
		// 继续跑，但必填项没值 → 仍然被挡住（不会带着缺失配置启动）
		if err == nil || !strings.Contains(err.Error(), "API_TOKEN 未设置") {
			t.Fatalf("应仍因必填项缺失而失败：%v", err)
		}
		if v.LogLevel.Value != "info" {
			t.Errorf("其余项应回落到默认值，得到 %q", v.LogLevel.Value)
		}
	})
}

func TestClientOverview(t *testing.T) {
	ts := newCenter(t)
	ctx := context.Background()

	svc := confdeck.NewClient(ts.URL, confdeck.WithToken("secret-token"))
	admin := confdeck.NewClient(ts.URL, confdeck.WithToken("secret-token"))

	spec := newSpec()
	if err := svc.Register(ctx, "api", &spec); err != nil {
		t.Fatal(err)
	}

	ov, err := admin.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(ov.Scopes) != 1 || ov.Scopes[0].Scope != "api" {
		t.Fatalf("概览 = %+v", ov)
	}
	if len(ov.Scopes[0].Missing) != 1 || ov.Scopes[0].Missing[0] != "API_TOKEN" {
		t.Errorf("应标出缺失的必填项：%+v", ov.Scopes[0].Missing)
	}
	if ov.Insecure {
		t.Error("配置了 token，不应报告 insecure")
	}

	// 填上之后不再缺失
	if err := admin.PutValues(ctx, "api", map[string]string{"API_TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
	ov, err = admin.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Scopes[0].Missing) != 0 {
		t.Errorf("填上后不应再缺失：%v", ov.Scopes[0].Missing)
	}
}

func TestClientRejectsBadToken(t *testing.T) {
	ts := newCenter(t)
	bad := confdeck.NewClient(ts.URL, confdeck.WithToken("wrong"))

	v := newSpec()
	err := confdeck.Load(&v, confdeck.WithSource(bad.Source("api")))
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("错误 token 应被拒：%v", err)
	}
}
