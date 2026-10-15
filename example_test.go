package confdeck

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 最小可用示例：声明 → 从来源加载 → 读取值。
func Example() {
	type Vars struct {
		LogLevel Config[string]
		Workers  Config[int]
		APIToken Config[string]
	}

	spec := Vars{
		LogLevel: Config[string]{Name: "LOG_LEVEL", Default: "info", Desc: "日志级别。"},
		Workers:  Config[int]{Name: "WORKERS", Default: 8, Validate: Positive},
		APIToken: Config[string]{Name: "API_TOKEN", Required: true, Secret: true},
	}

	v := spec
	err := Load(&v, WithSource(FromMap(map[string]string{
		"API_TOKEN": "123:abc",
		"WORKERS":   "16",
	})))
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	fmt.Println(v.LogLevel.Value, v.Workers.Value, v.APIToken.Provided())

	// Output: info 16 true
}

// 值层面的问题会聚合成一条错误（而不是崩在第一个上）。
func ExampleLoad_problems() {
	type Vars struct {
		APIToken Config[string]
		Workers  Config[int]
	}

	v := Vars{
		APIToken: Config[string]{Name: "API_TOKEN", Required: true},
		Workers:  Config[int]{Name: "WORKERS", Default: 8},
	}

	err := Load(&v, WithSource(FromMap(map[string]string{"WORKERS": "八"})))
	fmt.Println(err)

	// Output: 配置未就绪：WORKERS：不是合法整数："八"；API_TOKEN 未设置
}

// 秒 / 毫秒有歧义，必须写明单位（Parse 与 Type 都显式）。
func ExampleConfig_duration() {
	type Vars struct {
		CacheTTL Config[time.Duration]
	}

	v := Vars{
		CacheTTL: Config[time.Duration]{
			Name: "CACHE_TTL_SECONDS", Type: "seconds",
			Default: 24 * time.Hour, Parse: Seconds,
			Desc: "缓存有效期。",
		},
	}

	if err := Load(&v, WithSource(FromMap(map[string]string{
		"CACHE_TTL_SECONDS": "600",
	}))); err != nil {
		fmt.Println("load:", err)
		return
	}
	fmt.Println(v.CacheTTL.Value)

	// Output: 10m0s
}

// 同一份声明还能产出种子模板与「哪些还没配」。
func ExampleMissing() {
	type Vars struct {
		APIToken Config[string]
		DBURL    Config[string]
		LogLevel Config[string]
	}

	v := Vars{
		APIToken: Config[string]{Name: "API_TOKEN", Required: true, Secret: true, Desc: "调用上游 API 用的 token。"},
		DBURL: Config[string]{
			Name: "DB_URL", Required: true, Secret: true, External: true,
			Desc: "连接串，由部署环境提供。",
		},
		LogLevel: Config[string]{Name: "LOG_LEVEL", Default: "info", Desc: "日志级别。"},
	}

	fmt.Println("缺失：", strings.Join(Missing(&v), ","))
	fmt.Println("---种子模板---")
	fmt.Print(Dotenv(&v, "api"))

	// Output:
	// 缺失： API_TOKEN
	// ---种子模板---
	// # ==============================================================================
	// # 由 confdeck 从 Go 里的配置声明生成 —— 不要手改。
	// # 重新生成：go run ./cmd/<service> -print-schema
	// #
	// # 约定：注释掉的项表示代码里有默认值，不设置就用默认值；
	// #       没有注释的项需要填写；replace-with-* 是必须替换的占位符；
	// #       标注「由部署环境提供」的项不属于配置中心，写在这里也不会生效。
	// # ==============================================================================
	//
	// # 调用上游 API 用的 token。
	// # type: string, required, secret
	// api/API_TOKEN=replace-with-api-token
	//
	// # 连接串，由部署环境提供。
	// # type: string, required, secret, external
	// # 由部署环境提供（不在配置中心里配，写到这个文件里也不会生效）
	// # api/DB_URL=
	//
	// # 日志级别。
	// # type: string
	// # 默认值：info
	// # api/LOG_LEVEL=info
}

// External：由部署环境提供的项不进「缺失」，也不进 schema 的 required。
func Example_external() {
	type Vars struct {
		DBURL    Config[string]
		APIToken Config[string]
	}

	spec := Vars{
		DBURL: Config[string]{
			Name: "DB_URL", Required: true, External: true, Secret: true,
			Desc: "连接串，由 compose 注入。",
		},
		APIToken: Config[string]{Name: "API_TOKEN", Required: true},
	}

	// 只给了 API Token：DB_URL 由部署环境提供，所以「缺失」里不该有它。
	// 1) 部署没给：照样拒绝启动（错误信息会说清该去哪儿给）。
	missing := spec
	fmt.Println("部署没给：", Load(&missing,
		WithSource(FromMap(map[string]string{"API_TOKEN": "tok"}))))

	// 2) 部署给了（compose 注入进程环境）：加载通过，而且中心的缺失列表里没有它。
	v := spec
	err := Load(&v, WithSource(FromMap(map[string]string{
		"API_TOKEN": "tok",
		"DB_URL":    "postgres://u:p@h/db",
	})))
	fmt.Println("部署给了：", err)
	fmt.Printf("中心的缺失列表：%q\n", strings.Join(Missing(&v), ","))

	// 3) schema 里标成 x-external，且不列进 required —— 中心据此显示「部署提供」。
	doc, _ := Schema(&v, "api")
	var parsed struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(doc, &parsed)
	_, marked := parsed.Properties["DB_URL"]["x-external"]
	fmt.Println("schema 的 required：", strings.Join(parsed.Required, ","))
	fmt.Println("schema 标了 x-external：", marked)

	// Output:
	// 部署没给： 配置未就绪：DB_URL 未设置（由部署环境提供）
	// 部署给了： <nil>
	// 中心的缺失列表：""
	// schema 的 required： API_TOKEN
	// schema 标了 x-external： true
}
