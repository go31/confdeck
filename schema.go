package confdeck

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Schema 产出 JSON Schema（draft 2020-12），供配置中心 UI、编辑器补全与校验使用。
//
// 标准字段（type / description / default / required）之外，用 x-* 携带本库特有的信息：
//
//	x-secret    该值敏感，UI 应打码
//	x-type      confdeck 的类型名（seconds / csv / csv_int64 …），标准 JSON 类型表达不了的
//	x-external  该值由部署环境提供，配置中心不该管它（所以也不列进 required）
//	x-shared    跨服务共享键：值配在 shared 作用域，界面只在 shared 下展示一次
func Schema(dst any, title string) ([]byte, error) {
	specs := Specs(dst)

	properties := make(map[string]any, len(specs))
	required := make([]string, 0, len(specs))
	for _, s := range specs {
		prop := map[string]any{"x-type": s.Type}
		if s.Desc != "" {
			prop["description"] = s.Desc
		}
		if s.Secret {
			prop["x-secret"] = true
		}
		if s.External {
			prop["x-external"] = true
		}
		if s.Shared {
			prop["x-shared"] = true
		}
		switch s.Type {
		case "string":
			prop["type"] = "string"
		case "int", "int64", "seconds", "millis", "duration":
			prop["type"] = "integer"
		case "bool":
			prop["type"] = "boolean"
		case "csv", "csv_int64":
			prop["type"] = "array"
			itemType := "string"
			if s.Type == "csv_int64" {
				itemType = "integer"
			}
			prop["items"] = map[string]any{"type": itemType}
		}
		if s.Default != "" {
			prop["default"] = s.Default
		}
		properties[s.Key] = prop
		// External 的项不进 required：这份 schema 描述的是「配置中心里该有哪些键」，
		// 而 External 的值住在部署环境里，中心里本来就没有。
		// 它在进程侧的必填性由 Go 里的校验负责（Config.check）。
		//
		// Shared 的项**要**留在 required：它的值确实在中心里（住在 shared 作用域），
		// 而且服务端要靠这个信息才知道「这个共享键是必填的」—— 共享那一行的元信息
		// 就是从各服务注册的 schema 里取的。
		if s.Required && !s.External {
			required = append(required, s.Key)
		}
	}

	doc := map[string]any{
		"$schema":    "https://json-schema.org/draft/2020-12/schema",
		"type":       "object",
		"properties": properties,
		"x-confdeck": 1,
	}
	if title != "" {
		doc["title"] = title
	}
	if len(required) > 0 {
		doc["required"] = required
	}
	return json.MarshalIndent(doc, "", "  ")
}

// Dotenv 产出「种子文件」：给运维照着填，然后下发给配置中心。
//
// 行格式是 <scope>/<KEY>=<value>（scope 为空时退化成 <KEY>=<value>），
// 与 ops.sh / confdeckctl 的下发格式一致。
func Dotenv(dst any, scope string) string {
	prefix := ""
	if scope != "" {
		prefix = scope + "/"
	}

	var b strings.Builder
	b.WriteString("# ==============================================================================\n")
	b.WriteString("# 由 confdeck 从 Go 里的配置声明生成 —— 不要手改。\n")
	b.WriteString("# 重新生成：go run ./cmd/<service> -print-schema\n")
	b.WriteString("#\n")
	b.WriteString("# 约定：注释掉的项表示代码里有默认值，不设置就用默认值；\n")
	b.WriteString("#       没有注释的项需要填写；" + PlaceholderPrefix + "* 是必须替换的占位符；\n")
	b.WriteString("#       标注「由部署环境提供」的项不属于配置中心，写在这里也不会生效。\n")
	b.WriteString("# ==============================================================================\n")

	for _, s := range Specs(dst) {
		b.WriteString("\n")
		for _, line := range strings.Split(strings.TrimSpace(s.Desc), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				fmt.Fprintf(&b, "# %s\n", line)
			}
		}
		fmt.Fprintf(&b, "# type: %s\n", strings.Join(flagsOf(s), ", "))
		switch {
		case s.Shared:
			// 共享键的家在 shared 作用域：模板直接写 shared/<KEY>，
			// 照模板填就得到「两个服务共用一份值」的正确布局。
			b.WriteString("# 跨服务共享键：值配在 shared 作用域，各服务只声明、不各存一份\n")
			switch {
			case s.Required:
				fmt.Fprintf(&b, "shared/%s=%s\n", s.Key, placeholderFor(s.Key))
			case s.Default != "":
				fmt.Fprintf(&b, "# 默认值：%s\n", s.Default)
				fmt.Fprintf(&b, "# shared/%s=%s\n", s.Key, s.Default)
			default:
				fmt.Fprintf(&b, "shared/%s=\n", s.Key)
			}
		case s.External:
			// 部署环境提供：中心不该管它，所以整行注释掉并写明原因，
			// 免得运维照着模板在中心里白填一遍。
			b.WriteString("# 由部署环境提供（不在配置中心里配，写到这个文件里也不会生效）\n")
			if s.Default != "" {
				fmt.Fprintf(&b, "# 默认值：%s\n", s.Default)
				fmt.Fprintf(&b, "# %s%s=%s\n", prefix, s.Key, s.Default)
			} else {
				fmt.Fprintf(&b, "# %s%s=\n", prefix, s.Key)
			}
		case s.Required:
			fmt.Fprintf(&b, "%s%s=%s\n", prefix, s.Key, placeholderFor(s.Key))
		case s.Default != "":
			fmt.Fprintf(&b, "# 默认值：%s\n", s.Default)
			fmt.Fprintf(&b, "# %s%s=%s\n", prefix, s.Key, s.Default)
		default:
			fmt.Fprintf(&b, "%s%s=\n", prefix, s.Key)
		}
	}
	return b.String()
}

// Markdown 产出人读的变量清单。
func Markdown(dst any, title string) string {
	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "# %s\n\n", title)
	}
	b.WriteString("| 变量 | 类型 | 必填 | 敏感 | 默认值 | 说明 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, s := range Specs(dst) {
		yes := func(v bool) string {
			if v {
				return "是"
			}
			return ""
		}
		desc := strings.ReplaceAll(strings.TrimSpace(s.Desc), "\n", " ")
		if s.External {
			desc = strings.TrimSpace("**由部署环境提供** " + desc)
		}
		if s.Shared {
			desc = strings.TrimSpace("**跨服务共享（配在 shared）** " + desc)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n",
			s.Key, s.Type, yes(s.Required), yes(s.Secret), codeOrDash(s.Default), desc)
	}
	return b.String()
}

// Missing 列出「还没配好」的键：必填但没有任何来源提供，或值仍是占位符。
//
// 给 CLI（confdeckctl check）与配置中心 UI 用，也适合放在启动日志里。
// External 的项不算在内：它们由部署环境提供，配置中心根本看不到，算进去就是永久假警报。
func Missing(dst any) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	add := func(key string) {
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	for _, s := range Specs(dst) {
		if s.External {
			continue
		}
		if s.Required && !s.Present {
			add(s.Key)
		}
		if strings.HasPrefix(strings.TrimSpace(s.Value), PlaceholderPrefix) {
			add(s.Key)
		}
	}
	return out
}

func flagsOf(s Spec) []string {
	flags := []string{s.Type}
	if s.Required {
		flags = append(flags, "required")
	}
	if s.Secret {
		flags = append(flags, "secret")
	}
	if s.External {
		flags = append(flags, "external")
	}
	if s.Shared {
		flags = append(flags, "shared")
	}
	return flags
}

func placeholderFor(key string) string {
	return PlaceholderPrefix + strings.ToLower(strings.ReplaceAll(key, "_", "-"))
}

func codeOrDash(v string) string {
	if v == "" {
		return "—"
	}
	return "`" + v + "`"
}
