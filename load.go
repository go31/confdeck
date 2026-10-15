package confdeck

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

type loadOptions struct {
	ctx           context.Context
	sources       []Source
	resolve       []any
	onSourceError func(source string, err error)
}

// Option 调整 Load 的行为。
type Option func(*loadOptions)

// WithSource 追加一个来源；多次调用时**先出现的优先**。
// 不调用时默认使用进程环境（FromEnv）。
func WithSource(s Source) Option {
	return func(o *loadOptions) { o.sources = append(o.sources, s) }
}

// WithContext 给来源读取（例如访问配置中心的 HTTP 请求）设置超时/取消。
func WithContext(ctx context.Context) Option {
	return func(o *loadOptions) { o.ctx = ctx }
}

// WithResolve 注册跨字段兜底，在「填值之后、校验之前」执行。
//
//	fn 必须是 func(*Vars)，Vars 与容器类型一致；签名不符会 panic（声明错误）。
//
// 这是本库里唯一需要反射调用的地方 —— 跨字段依赖（例如「为空时回退到另一个变量」）
// 无法用单变量描述，所以显式写成钩子，而不是藏在解析里。
func WithResolve(fn any) Option {
	return func(o *loadOptions) { o.resolve = append(o.resolve, fn) }
}

// WithSourceErrorHandler 把「某个来源读取失败」从致命错误降级为告警后继续。
//
// 典型用法是配置中心：中心短暂不可用时不该让服务完全起不来 —— 真正缺失的必填项
// 仍会被校验挡住，所以不会带着缺失配置跑起来。这里显式注册回调（而不是默认降级），
// 是为了避免「来源静默失败」这种最难查的情况。
func WithSourceErrorHandler(fn func(source string, err error)) Option {
	return func(o *loadOptions) { o.onSourceError = fn }
}

// Load 按来源填充容器并校验，把**值层面**的问题聚合成一个错误返回：
// 来源读取失败（未注册降级回调时）、未设置（Required 且没有任何来源提供）、
// 仍是占位符、解析失败、自定义校验失败。
//
// 声明层面的错误由 Validate 直接 panic，不会走到这里。
func Load(dst any, opts ...Option) error {
	Validate(dst)

	o := loadOptions{ctx: context.Background()}
	for _, opt := range opts {
		opt(&o)
	}
	if len(o.sources) == 0 {
		o.sources = []Source{FromEnv()}
	}

	// 1) 取全部来源的值：先出现的来源里已有的键胜出；空值按「未设置」丢弃。
	merged := make(map[string]string)
	var problems []string
	for _, s := range o.sources {
		values, err := s.Values(o.ctx)
		if err != nil {
			if o.onSourceError == nil {
				return fmt.Errorf("读取配置来源 %s 失败：%w", s.Name(), err)
			}
			o.onSourceError(s.Name(), err)
			continue
		}
		for k, v := range values {
			if v == "" {
				continue
			}
			if _, exists := merged[k]; exists {
				continue
			}
			merged[k] = v
		}
	}

	// 2) 填值：来源里有就用它，否则用默认值。
	for _, f := range collect(dst) {
		key := f.meta().Key
		raw, ok := merged[key]
		if !ok {
			f.useDefault()
			continue
		}
		if err := f.set(raw); err != nil {
			problems = append(problems, err.Error())
		}
	}

	// 3) 跨字段兜底。
	for _, fn := range o.resolve {
		callResolve(dst, fn)
	}

	// 4) 校验。
	for _, f := range collect(dst) {
		if err := f.check(); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("配置未就绪：%s", strings.Join(problems, "；"))
	}
	return nil
}

// MustLoad 与 Load 相同，但出错时 panic —— 启动阶段「配错了就别跑」的最短写法：
//
//	v := Spec
//	confdeck.MustLoad(&v, confdeck.WithSource(confdeck.FromEnv()))
func MustLoad(dst any, opts ...Option) {
	if err := Load(dst, opts...); err != nil {
		panic(err)
	}
}

func callResolve(dst any, fn any) {
	fv := reflect.ValueOf(fn)
	if fv.Kind() != reflect.Func || fv.Type().NumIn() != 1 || fv.Type().NumOut() != 0 {
		panic("confdeck: WithResolve 需要 func(*T)，其中 T 与容器类型一致")
	}
	dv := reflect.ValueOf(dst)
	if fv.Type().In(0) != dv.Type() {
		panic(fmt.Sprintf("confdeck: WithResolve 的参数应为 %s，实际是 %s", dv.Type(), fv.Type().In(0)))
	}
	fv.Call([]reflect.Value{dv})
}
