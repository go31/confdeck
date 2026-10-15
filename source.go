package confdeck

import (
	"context"
	"os"
	"strings"
)

// Source 提供一批「键 → 值」。
//
// Load 会按 WithSource 的顺序询问各来源，**先出现的来源里已有的键胜出**：
//
//	confdeck.WithSource(confdeck.Sources(confdeck.FromEnv(), client.Source("api")))
//
// 上面这行的含义是「进程环境优先，其次配置中心」。
//
// 空字符串统一按「未设置」处理（沿用既有语义：空值不能覆盖默认值），
// 所以「显式设为空」这件事在本库里不表达 —— 需要空值时用默认值或校验规则表达。
//
// 接口刻意设计成**批量**而不是逐键：远程来源（配置中心）一次就能把值全取回来，
// 而且可以返回错误 —— 逐键 Lookup 的签名没法上报「中心不可达」。
type Source interface {
	Name() string
	Values(ctx context.Context) (map[string]string, error)
}

type envSource struct{}

// FromEnv 读取全部进程环境变量。它是 Load 的默认来源，通常也放在第一位：
// 应急时给容器设一个环境变量就能顶住，不用动配置中心。
func FromEnv() Source { return envSource{} }

func (envSource) Name() string { return "env" }

func (envSource) Values(context.Context) (map[string]string, error) {
	environ := os.Environ()
	out := make(map[string]string, len(environ))
	for _, kv := range environ {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out, nil
}

type mapSource struct {
	name   string
	values map[string]string
}

// FromMap 从一个 map 取值 —— 主要给测试用，不污染进程环境。
func FromMap(values map[string]string) Source {
	return mapSource{name: "map", values: values}
}

// NamedMap 与 FromMap 相同，但给来源起个名字（会出现在错误与组合名里）。
func NamedMap(name string, values map[string]string) Source {
	return mapSource{name: name, values: values}
}

func (m mapSource) Name() string { return m.name }

func (m mapSource) Values(context.Context) (map[string]string, error) { return m.values, nil }

// Sources 把多个来源合成一个（先出现者胜出）。嵌套使用也合法。
func Sources(list ...Source) Source { return multiSource(list) }

type multiSource []Source

func (m multiSource) Name() string {
	names := make([]string, 0, len(m))
	for _, s := range m {
		names = append(names, s.Name())
	}
	return strings.Join(names, " > ")
}

func (m multiSource) Values(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string)
	for _, s := range m {
		values, err := s.Values(ctx)
		if err != nil {
			return nil, err
		}
		for k, v := range values {
			if _, exists := out[k]; exists {
				continue // 先出现的胜出
			}
			out[k] = v
		}
	}
	return out, nil
}
