package confdeck

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// 测试用的容器：覆盖所有形态（含秒、CSV、int64、剥 @）。
type testVars struct {
	LogLevel      Config[string]
	Workers       Config[int]
	CacheTTL      Config[time.Duration]
	APIToken      Config[string]
	Origins       Config[[]string]
	Owner         Config[string]
	Ratio         Config[int64]
	InternalToken Config[string]
}

var testSpec = testVars{
	LogLevel:      Config[string]{Name: "LOG_LEVEL", Default: "info", Desc: "日志级别。"},
	Workers:       Config[int]{Name: "WORKERS", Default: 8, Validate: AtMost(64)},
	CacheTTL:      Config[time.Duration]{Name: "CACHE_TTL_SECONDS", Type: "seconds", Default: 5 * time.Minute, Parse: Seconds},
	APIToken:      Config[string]{Name: "API_TOKEN", Required: true, Secret: true, Desc: "调用上游 API 用的 token。"},
	Origins:       Config[[]string]{Name: "CORS_ALLOWED_ORIGINS", Default: []string{"http://localhost:5173"}},
	Owner:         Config[string]{Name: "OWNER", Parse: TrimAt},
	Ratio:         Config[int64]{Name: "RATIO", Default: 10},
	InternalToken: Config[string]{Name: "INTERNAL_TOKEN"},
}

func TestLoadFillsValuesAndDefaults(t *testing.T) {
	v := testSpec
	err := Load(&v, WithSource(FromMap(map[string]string{
		"API_TOKEN":            "tok",
		"WORKERS":              "16",
		"CACHE_TTL_SECONDS":    "600",
		"CORS_ALLOWED_ORIGINS": "a.example, b.example ,,c.example",
		"OWNER":                "@team",
		"RATIO":                "99",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if v.LogLevel.Value != "info" {
		t.Errorf("未提供的项应取默认值，得到 %q", v.LogLevel.Value)
	}
	if v.LogLevel.Provided() {
		t.Error("用默认值的项 Present 应为 false")
	}
	if v.Workers.Value != 16 || !v.Workers.Provided() {
		t.Errorf("Workers = %d present=%v", v.Workers.Value, v.Workers.Provided())
	}
	if v.CacheTTL.Value != 10*time.Minute {
		t.Errorf("秒解析错误：%v", v.CacheTTL.Value)
	}
	if got := strings.Join(v.Origins.Value, "|"); got != "a.example|b.example|c.example" {
		t.Errorf("CSV 解析错误：%q", got)
	}
	if v.Owner.Value != "team" {
		t.Errorf("剥 @ 失败：%q", v.Owner.Value)
	}
	if v.Ratio.Value != 99 {
		t.Errorf("int64 解析错误：%d", v.Ratio.Value)
	}
	if v.InternalToken.Value != "" || v.InternalToken.Provided() {
		t.Error("没有默认值又没提供的项应保持零值")
	}
}

func TestSourcesFirstWins(t *testing.T) {
	v := testSpec
	err := Load(&v,
		WithSource(FromMap(map[string]string{"LOG_LEVEL": "debug", "API_TOKEN": "tok"})),
		WithSource(FromMap(map[string]string{"LOG_LEVEL": "error"})),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.LogLevel.Value != "debug" {
		t.Errorf("先出现的来源应优先，得到 %q", v.LogLevel.Value)
	}
}

func TestEmptyValueMeansUnset(t *testing.T) {
	v := testSpec
	err := Load(&v, WithSource(FromMap(map[string]string{
		"LOG_LEVEL": "", // 空值 = 未设置
		"API_TOKEN": "tok",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.LogLevel.Value != "info" || v.LogLevel.Provided() {
		t.Errorf("空值应按未设置处理，得到 %q present=%v", v.LogLevel.Value, v.LogLevel.Provided())
	}
}

func TestEnvSourceIsDefault(t *testing.T) {
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("API_TOKEN", "tok")

	v := testSpec
	if err := Load(&v); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.LogLevel.Value != "warn" {
		t.Errorf("默认应使用进程环境，得到 %q", v.LogLevel.Value)
	}
}

func TestLoadAggregatesAllProblems(t *testing.T) {
	v := testSpec
	err := Load(&v, WithSource(FromMap(map[string]string{
		"WORKERS":   "abc",                           // 解析失败
		"API_TOKEN": PlaceholderPrefix + "api-token", // 占位符
		"RATIO":     "not-a-number",                  // 解析失败
	})))
	if err == nil {
		t.Fatal("期望聚合错误")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "配置未就绪：") {
		t.Errorf("错误文案前缀不对：%s", msg)
	}
	for _, want := range []string{"WORKERS", "API_TOKEN 仍是占位符", "RATIO"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息缺少 %q：%s", want, msg)
		}
	}
	if got := strings.Count(msg, "；"); got < 2 {
		t.Errorf("应把多个问题聚合在一起，实际：%s", msg)
	}
}

func TestValidateHookRejectsValue(t *testing.T) {
	v := testSpec
	err := Load(&v, WithSource(FromMap(map[string]string{
		"WORKERS":   "999",
		"API_TOKEN": "tok",
	})))
	if err == nil || !strings.Contains(err.Error(), "WORKERS 不能超过 64") {
		t.Fatalf("自定义校验未生效：%v", err)
	}
}

func TestRequiredMissing(t *testing.T) {
	v := testSpec
	err := Load(&v, WithSource(FromMap(map[string]string{})))
	if err == nil || !strings.Contains(err.Error(), "API_TOKEN 未设置") {
		t.Fatalf("必填校验未生效：%v", err)
	}
}

func TestResolveRunsBeforeValidate(t *testing.T) {
	v := testSpec
	err := Load(&v,
		WithSource(FromMap(map[string]string{"API_TOKEN": "tok"})),
		WithResolve(func(v *testVars) {
			if v.InternalToken.Value == "" {
				v.InternalToken.Value = v.APIToken.Value
			}
		}),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.InternalToken.Value != "tok" {
		t.Errorf("跨字段兜底未生效：%q", v.InternalToken.Value)
	}
}

func TestResolveWrongSignaturePanics(t *testing.T) {
	v := testSpec
	mustPanic(t, "WithResolve", func() {
		_ = Load(&v, WithResolve(func(v *testVars, extra int) {}))
	})
}

func TestMustLoadPanicsOnProblems(t *testing.T) {
	v := testSpec
	mustPanic(t, "配置未就绪", func() {
		MustLoad(&v, WithSource(FromMap(map[string]string{})))
	})
}

func TestSpecsKeysAndMissing(t *testing.T) {
	specs := Specs(&testSpec)
	if len(specs) != 8 {
		t.Fatalf("应收集到 8 个变量，实际 %d", len(specs))
	}
	if specs[0].Key != "LOG_LEVEL" {
		t.Errorf("应按声明顺序返回，首个是 %q", specs[0].Key)
	}

	v := testSpec
	_ = Load(&v, WithSource(FromMap(map[string]string{
		"API_TOKEN":         PlaceholderPrefix + "x",
		"WORKERS":           "8",
		"CACHE_TTL_SECONDS": "600",
		"OWNER":             "a",
		"RATIO":             "1",
	})))
	if got := strings.Join(Missing(&v), ","); got != "API_TOKEN" {
		t.Errorf("Missing = %q", got)
	}
}

func TestDotenvOutput(t *testing.T) {
	out := Dotenv(&testSpec, "api")
	for _, want := range []string{
		"api/API_TOKEN=" + PlaceholderPrefix + "api-token",
		"# 默认值：info",
		"# api/LOG_LEVEL=info",
		"# type: string, required, secret",
		"api/INTERNAL_TOKEN=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("种子模板缺少 %q\n---\n%s", want, out)
		}
	}
}

func TestSchemaOutput(t *testing.T) {
	raw, err := Schema(&testSpec, "api 配置")
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	s := string(raw)
	for _, want := range []string{
		`"$schema": "https://json-schema.org/draft/2020-12/schema"`,
		`"title": "api 配置"`,
		`"API_TOKEN"`,
		`"x-secret": true`,
		`"x-type": "seconds"`,
		`"required": [`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON Schema 缺少 %q\n---\n%s", want, s)
		}
	}
}

func TestMarkdownOutput(t *testing.T) {
	out := Markdown(&testSpec, "api 配置")
	if !strings.Contains(out, "| `LOG_LEVEL` | string |") {
		t.Errorf("Markdown 表格不对：\n%s", out)
	}
	if !strings.Contains(out, "| `API_TOKEN` | string | 是 | 是 |") {
		t.Errorf("必填/敏感标记不对：\n%s", out)
	}
}

func TestDeclarationPanics(t *testing.T) {
	t.Run("键名重复", func(t *testing.T) {
		type V struct {
			A Config[string]
			B Config[int]
		}
		v := V{A: Config[string]{Name: "DUP"}, B: Config[int]{Name: "DUP"}}
		mustPanic(t, "键名重复", func() { Validate(&v) })
	})

	t.Run("Required 与 Default 同时给出", func(t *testing.T) {
		type V struct{ A Config[string] }
		v := V{A: Config[string]{Name: "A", Required: true, Default: "x"}}
		mustPanic(t, "Required 与 Default", func() { Validate(&v) })
	})

	t.Run("有歧义的类型没给 Parse", func(t *testing.T) {
		type V struct{ A Config[time.Duration] }
		v := V{A: Config[time.Duration]{Name: "A", Type: "seconds"}}
		mustPanic(t, "必须显式指定 Parse", func() { Validate(&v) })
	})

	t.Run("时长没写明单位", func(t *testing.T) {
		type V struct{ A Config[time.Duration] }
		v := V{A: Config[time.Duration]{Name: "A", Parse: Seconds}}
		mustPanic(t, "时长的单位必须写明", func() { Validate(&v) })
	})

	t.Run("容器里混入非 Config 字段", func(t *testing.T) {
		type V struct {
			A Config[string]
			B string
		}
		v := V{A: Config[string]{Name: "A"}}
		mustPanic(t, "必须是 confdeck.Config[T]", func() { Validate(&v) })
	})

	t.Run("Name 为空", func(t *testing.T) {
		type V struct{ A Config[string] }
		v := V{A: Config[string]{}}
		mustPanic(t, "Name 为空", func() { Validate(&v) })
	})

	t.Run("传的不是指针", func(t *testing.T) {
		mustPanic(t, "非空指针", func() { Specs(testSpec) })
	})
}

func TestTypeNameDerivation(t *testing.T) {
	specs := Specs(&testSpec)
	byKey := map[string]Spec{}
	for _, s := range specs {
		byKey[s.Key] = s
	}
	cases := map[string]string{
		"LOG_LEVEL":            "string",
		"WORKERS":              "int",
		"CACHE_TTL_SECONDS":    "seconds", // 显式写明的单位优先于从 T 推断
		"CORS_ALLOWED_ORIGINS": "csv",
		"RATIO":                "int64",
	}
	for key, want := range cases {
		if got := byKey[key].Type; got != want {
			t.Errorf("%s 的类型名 = %q，期望 %q", key, got, want)
		}
	}
}

func TestSplitCSVAndPresets(t *testing.T) {
	if got := CSVInt64OrFail(t, "-1, 2 ,3"); len(got) != 3 || got[0] != -1 || got[2] != 3 {
		t.Errorf("CSVInt64 = %v", got)
	}
	if _, err := CSVInt64("1,x"); err == nil {
		t.Error("CSVInt64 应拒绝非整数")
	}
	if d, err := Millis("500"); err != nil || d != 500*time.Millisecond {
		t.Errorf("Millis = %v, %v", d, err)
	}
	if d, err := Duration("1h30m"); err != nil || d != 90*time.Minute {
		t.Errorf("Duration = %v, %v", d, err)
	}
	if s, err := TrimSlash("https://a.example/"); err != nil || s != "https://a.example" {
		t.Errorf("TrimSlash = %q, %v", s, err)
	}
	if err := Positive(0); err == nil {
		t.Error("Positive 应拒绝 0")
	}
}

func CSVInt64OrFail(t *testing.T, raw string) []int64 {
	t.Helper()
	out, err := CSVInt64(raw)
	if err != nil {
		t.Fatalf("CSVInt64(%q): %v", raw, err)
	}
	return out
}

func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("期望 panic（含 %q），但没有", want)
		}
		if !strings.Contains(fmt.Sprint(r), want) {
			t.Fatalf("panic 内容不含 %q：%v", want, r)
		}
	}()
	fn()
}
