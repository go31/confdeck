package store

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrInvalid 表示调用方给的作用域名 / 键名不合法。
// 服务端用 errors.Is(err, store.ErrInvalid) 把它映射成 400，而不是 500。
var ErrInvalid = errors.New("非法的输入")

var (
	// 作用域名会进 URL 与桶名：限制成小写字母/数字/._-，避免大小写与路径歧义。
	scopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// 键名同时是环境变量名：字母或下划线开头。
	keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// CheckScope 校验作用域名。
func CheckScope(scope string) error {
	if !scopePattern.MatchString(scope) {
		return fmt.Errorf("%w：作用域名 %q 不合法（小写字母或数字开头，可用小写字母/数字/._-，1-64 字符）",
			ErrInvalid, scope)
	}
	return nil
}

// CheckKey 校验键名。
func CheckKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("%w：键名 %q 不合法（字母或下划线开头，后跟字母/数字/下划线，最长 128 字符）",
			ErrInvalid, key)
	}
	return nil
}
