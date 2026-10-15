package confdeck

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 预置解析器：写 Config[T].Parse 时直接用函数名。
//
//	CacheTTL: confdeck.Config[time.Duration]{
//		Name: "CACHE_TTL_SECONDS", Default: 5 * time.Minute,
//		Parse: confdeck.Seconds,
//	}

// Seconds 把 "600" 解析成 600s。用于 *_SECONDS 这类键。
func Seconds(s string) (time.Duration, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("不是合法的秒数：%q", s)
	}
	return time.Duration(n) * time.Second, nil
}

// Millis 把 "500" 解析成 500ms。用于 *_MILLISECONDS 这类键。
func Millis(s string) (time.Duration, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("不是合法的毫秒数：%q", s)
	}
	return time.Duration(n) * time.Millisecond, nil
}

// Duration 解析 Go 自己的时长写法（"1h30m"、"600ms"）。
func Duration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("不是合法的时长：%q", s)
	}
	return d, nil
}

// CSV 按逗号切分并去掉空白项（等价于既有的 getenvCSV 语义）。
func CSV(s string) ([]string, error) { return splitCSV(s), nil }

// CSVInt64 按逗号切分成整数列表（例如用户 id 列表）。
func CSVInt64(s string) ([]int64, error) {
	parts := splitCSV(s)
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("不是合法的整数列表：%q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

// Trim 去掉首尾空白。
func Trim(s string) (string, error) { return strings.TrimSpace(s), nil }

// TrimAt 去掉开头的 @（账号名可带可不带 @ 前缀）。
func TrimAt(s string) (string, error) {
	return strings.Trim(strings.TrimSpace(s), "@"), nil
}

// TrimSlash 去掉结尾的 /（URL 前缀）。
func TrimSlash(s string) (string, error) {
	return strings.TrimRight(strings.TrimSpace(s), "/"), nil
}

// Positive 是一个可直接用于 Config[int].Validate 的校验器：必须大于 0。
//
//	Workers: confdeck.Config[int]{
//		Name: "WORKERS", Default: 8, Validate: confdeck.Positive,
//	}
func Positive(v int) error {
	if v <= 0 {
		return fmt.Errorf("必须大于 0（实际 %d）", v)
	}
	return nil
}

// AtMost 返回一个校验器：值不能超过 n。
func AtMost(n int) func(int) error {
	return func(v int) error {
		if v > n {
			return fmt.Errorf("不能超过 %d（实际 %d）", n, v)
		}
		return nil
	}
}
