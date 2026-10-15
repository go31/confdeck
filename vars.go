package confdeck

import (
	"fmt"
	"reflect"
	"strings"
)

// Specs 反射遍历容器，返回全部变量的元信息（按声明顺序）。
func Specs(dst any) []Spec {
	fields := collect(dst)
	out := make([]Spec, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.meta())
	}
	return out
}

// Keys 返回容器里全部变量的键名（按声明顺序）。
func Keys(dst any) []string {
	fields := collect(dst)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.meta().Key)
	}
	return out
}

// Validate 检查容器的**声明**是否正确，发现问题直接 panic。
//
// 这里检查的是「写代码时犯的错」（键名为空或重复、Required 与 Default 同时给、
// 有歧义的类型没给 Parse、容器里混入了非 Config 字段），应该在测试里就被炸出来。
// 值层面的问题（缺值、占位符、解析失败）由 Load 聚合成错误返回，不走 panic。
func Validate(dst any) {
	var problems []string
	seen := map[string]bool{}
	for _, f := range collect(dst) {
		problems = append(problems, f.declarationProblems()...)
		key := f.meta().Key
		if key == "" {
			continue
		}
		if seen[key] {
			problems = append(problems, fmt.Sprintf("键名重复：%s", key))
		}
		seen[key] = true
	}
	if len(problems) > 0 {
		panic("confdeck: 配置声明有误：\n  - " + strings.Join(problems, "\n  - "))
	}
}

// collect 取容器里的全部变量字段。
//
// 容器必须是「指向结构体的非空指针」，且结构体里只能有 confdeck.Config[T] 字段
// （未导出字段会被忽略，方便嵌辅助结构）；违反约定直接 panic —— 这是声明错误。
func collect(dst any) []field {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		panic("confdeck: 需要传入指向配置容器的非空指针（例如 &v）")
	}
	ev := rv.Elem()
	if ev.Kind() != reflect.Struct {
		panic(fmt.Sprintf("confdeck: 容器必须是结构体，实际是 %s", ev.Kind()))
	}

	out := make([]field, 0, ev.NumField())
	for i := 0; i < ev.NumField(); i++ {
		st := ev.Type().Field(i)
		if st.PkgPath != "" { // 未导出字段：跳过
			continue
		}
		fv := ev.Field(i)
		if impl, ok := fv.Addr().Interface().(field); ok {
			out = append(out, impl)
			continue
		}
		panic(fmt.Sprintf("confdeck: 字段 %s 必须是 confdeck.Config[T]（容器里只放配置变量，实际是 %s）",
			st.Name, st.Type))
	}
	if len(out) == 0 {
		panic("confdeck: 容器里没有任何 confdeck.Config[T] 字段")
	}
	return out
}
