// Package confdeck 把「一个服务需要哪些配置」变成 Go 里的一份声明。
//
// 每个变量就是一个 Config[T] 字面量，键名只出现一次：
//
//	type Vars struct {
//		LogLevel confdeck.Config[string]
//		HTTPPort confdeck.Config[int]
//	}
//
//	var Spec = Vars{
//		LogLevel: confdeck.Config[string]{
//			Name: "LOG_LEVEL", Default: "info",
//			Desc: "日志级别，可选 info/debug/warn/error",
//		},
//		HTTPPort: confdeck.Config[int]{
//			Name: "HTTP_PORT", Default: 8080,
//			Desc: "HTTP 监听端口。",
//		},
//	}
//
// 同一份声明同时支撑四件事：
//
//   - 启动时从进程环境 / 配置中心取值并校验（Load / MustLoad）；
//   - 产出 JSON Schema、种子模板与人读文档（Schema / Dotenv / Markdown）；
//   - 向配置中心注册自己（Client.Register），中心据此在 UI 上显示「哪些还没配」；
//   - 列出尚未配好的必填项（Missing）。
//
// 约定：**所有变量必须放在一个结构体里**（上面的 Vars）。Go 无法枚举包级变量，
// 容器是反射收集的唯一入口；容器里出现非 Config 字段会直接 panic。
package confdeck

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// PlaceholderPrefix 标记「必须替换掉的占位符」：种子模板用它，校验时会拒绝。
const PlaceholderPrefix = "replace-with-"

// Config 是一个变量的完整定义。T 就是值的类型。
//
// 约定与取舍：
//
//   - Default 的零值表示「没有默认值」；Required 与 Default 不能同时给出（声明错误，panic）。
//   - Type 只用于展示与序列化（JSON Schema），**不参与解析** —— 否则它会和 T 矛盾，
//     又变回「同一个东西写两处」。解析策略只由 Parse 决定。
//   - 秒 / 毫秒这类无法从类型推断的语义，必须显式给 Parse（见 Seconds / Millis）。
//   - External 表示「这一项由部署环境提供」（例如 compose 注入的 DB_URL）：
//     配置中心不该管它，所以界面把它标成「部署提供」、不计入缺失，schema 里也不列进
//     required（见 External 字段的说明）。
type Config[T any] struct {
	Name     string // 键名（同时是环境变量名），在容器内唯一
	Type     string // 可选：展示用；留空则从 T 推断
	Value    T      // Load 填这里
	Default  T      // 零值 = 没有默认值
	Required bool
	Secret   bool
	// External 表示这一项由**部署环境**提供（compose 注入、systemd Environment= …），
	// 而不是在配置中心里配。它对三件事有影响：
	//
	//   - 配置中心的概览不把它算进「缺失」（中心根本看不到进程环境，算进去就是永久假警报）；
	//   - schema 输出 x-external，界面据此显示「部署提供」并禁止就地编辑；
	//   - 种子模板把它注释掉，并注明由部署提供 —— 免得运维在中心里白填一遍。
	//
	// 与 Required 可以同时用：External + Required 的含义是「部署必须提供它，但中心不管」，
	// 这正是 DB_URL 这类引导值想要的（进程侧照样会在缺失时拒绝启动）。
	External bool
	// Shared 表示这一项是**跨服务共享键**：值配在 shared 作用域，多个服务共用一份。
	//
	// 每个服务仍然各自声明它（各服务需要哪些变量由自己的代码决定），但界面把它
	// **只在 shared 下展示一次** —— 免得同一个键在 api、worker 下各出现一行、被填两遍。
	// 服务端还会因此提示「服务作用域里也有一份覆盖值」（那会盖住共享值）。
	//
	// 注意它只影响「值该配在哪、怎么展示」，不影响读取：取值仍然是
	// shared 先、本作用域覆盖（见 Client.Source）。
	Shared   bool
	Desc     string
	Parse    func(string) (T, error) // 可选：字符串 → T（含 trim / 剥前缀等变换）
	Validate func(T) error           // 可选：值级校验

	present bool // 是否由某个来源显式提供了值（Load 填，供 UI 区分「配了」与「用默认值」）
}

// Provided 报告该变量的值是否由某个来源**显式提供**：
// false 表示当前用的是 Default（供 UI 区分「配了」与「用默认值」）。
func (c *Config[T]) Provided() bool { return c.present }

// Spec 是变量的元信息快照，供模板、文档与配置中心 UI 使用。
type Spec struct {
	Key      string
	Type     string
	Desc     string
	Required bool
	Secret   bool
	// External 表示这一项由部署环境提供，配置中心不参与（见 Config.External）。
	External bool
	// Shared 表示这一项是跨服务共享键（见 Config.Shared）。
	Shared  bool
	Default string
	Value   string
	Present bool
}

// field 让容器里不同 T 的 Config 能被统一处理。
type field interface {
	meta() Spec
	set(raw string) error
	useDefault()
	check() error
	declarationProblems() []string
}

func (c *Config[T]) meta() Spec {
	return Spec{
		Key:      c.Name,
		Type:     c.typeName(),
		Desc:     c.Desc,
		Required: c.Required,
		Secret:   c.Secret,
		External: c.External,
		Shared:   c.Shared,
		Default:  format(c.Default),
		Value:    format(c.Value),
		Present:  c.present,
	}
}

func (c *Config[T]) set(raw string) error {
	parse := c.Parse
	if parse == nil {
		parse = defaultParse[T]()
	}
	if parse == nil {
		return fmt.Errorf("%s：类型 %s 需要显式指定 Parse", c.Name, c.typeName())
	}
	v, err := parse(raw)
	if err != nil {
		return fmt.Errorf("%s：%w", c.Name, err)
	}
	c.Value = v
	c.present = true
	return nil
}

func (c *Config[T]) useDefault() {
	c.Value = c.Default
	c.present = false
}

func (c *Config[T]) check() error {
	if c.Required && !c.present {
		// External 的项中心看不到，所以报错时要说清楚「该去哪儿给」，
		// 否则运维会去中心里找一个根本不该出现在那儿的键。
		if c.External {
			return fmt.Errorf("%s 未设置（由部署环境提供）", c.Name)
		}
		return fmt.Errorf("%s 未设置", c.Name)
	}
	if s, ok := any(c.Value).(string); ok && strings.HasPrefix(strings.TrimSpace(s), PlaceholderPrefix) {
		return fmt.Errorf("%s 仍是占位符", c.Name)
	}
	if c.Validate != nil {
		if err := c.Validate(c.Value); err != nil {
			return fmt.Errorf("%s %w", c.Name, err)
		}
	}
	return nil
}

func (c *Config[T]) declarationProblems() []string {
	var out []string
	if strings.TrimSpace(c.Name) == "" {
		out = append(out, "有变量的 Name 为空")
	}
	if c.Required && !isZero(c.Default) {
		out = append(out, fmt.Sprintf("%s：Required 与 Default 不能同时给出", c.Name))
	}
	if c.Parse == nil && defaultParse[T]() == nil {
		out = append(out, fmt.Sprintf("%s：类型 %s 有歧义（例如秒与毫秒），必须显式指定 Parse",
			c.Name, c.typeName()))
	}
	// 时长的单位无法从类型推断：必须写明 Type，否则 UI 与文档会误导运维
	// （让人以为该填 "30s"，实际这个变量期望的是秒数 30）。
	if _, isDuration := any(c.Default).(time.Duration); isDuration && c.Type == "" {
		out = append(out, fmt.Sprintf(
			"%s：时长的单位必须写明，请设置 Type（seconds / millis / duration）", c.Name))
	}
	return out
}

func (c *Config[T]) typeName() string {
	if c.Type != "" {
		return c.Type
	}
	return typeNameOf[T]()
}

func typeNameOf[T any]() string {
	var zero T
	switch any(zero).(type) {
	case string:
		return "string"
	case int:
		return "int"
	case int64:
		return "int64"
	case bool:
		return "bool"
	case []string:
		return "csv"
	case []int64:
		return "csv_int64"
	case time.Duration:
		return "duration"
	}
	return fmt.Sprintf("%T", zero)
}

// defaultParse 按 T 给出默认解析器；返回 nil 表示该类型有歧义，必须显式指定 Parse。
func defaultParse[T any]() func(string) (T, error) {
	var zero T
	switch any(zero).(type) {
	case string:
		return func(s string) (T, error) { return any(s).(T), nil }
	case int:
		return func(s string) (T, error) {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return zero, fmt.Errorf("不是合法整数：%q", s)
			}
			return any(n).(T), nil
		}
	case int64:
		return func(s string) (T, error) {
			n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			if err != nil {
				return zero, fmt.Errorf("不是合法整数：%q", s)
			}
			return any(n).(T), nil
		}
	case bool:
		return func(s string) (T, error) {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true", "1", "yes", "on":
				return any(true).(T), nil
			case "false", "0", "no", "off":
				return any(false).(T), nil
			}
			return zero, fmt.Errorf("不是合法布尔值：%q", s)
		}
	case []string:
		return func(s string) (T, error) { return any(splitCSV(s)).(T), nil }
	case []int64:
		return func(s string) (T, error) {
			parts := splitCSV(s)
			out := make([]int64, 0, len(parts))
			for _, p := range parts {
				n, err := strconv.ParseInt(p, 10, 64)
				if err != nil {
					return zero, fmt.Errorf("不是合法的整数列表：%q", p)
				}
				out = append(out, n)
			}
			return any(out).(T), nil
		}
	}
	return nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func format[T any](v T) string {
	switch x := any(v).(type) {
	case string:
		return x
	case []string:
		return strings.Join(x, ",")
	case []int64:
		parts := make([]string, 0, len(x))
		for _, n := range x {
			parts = append(parts, strconv.FormatInt(n, 10))
		}
		return strings.Join(parts, ",")
	case time.Duration:
		return x.String()
	}
	return fmt.Sprint(v)
}

func isZero[T any](v T) bool {
	rv := reflect.ValueOf(v)
	return !rv.IsValid() || rv.IsZero()
}
