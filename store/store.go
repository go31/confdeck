// Package store 定义配置中心的存储接口。
//
// 只有服务端需要它：库的使用者（业务服务）不依赖这个包。
package store

// Store 保存各作用域的值与 schema。实现必须保证单次调用是原子的
// （要么全部生效，要么完全不生效），否则崩在中间会留下半个配置。
type Store interface {
	// Values 返回某作用域的全部键值；作用域不存在时返回空 map（不是错误）。
	Values(scope string) (map[string]string, error)

	// PutValues 写入若干键：值为空串表示**删除**该键。
	PutValues(scope string, values map[string]string) error

	// Schema 返回某作用域注册的 schema（JSON Schema 原文）；没注册过时返回 nil。
	Schema(scope string) ([]byte, error)

	// PutSchema 注册（覆盖）某作用域的 schema。
	PutSchema(scope string, schema []byte) error

	// Scopes 返回已知作用域（有值或有 schema 的都算），顺序不保证。
	Scopes() ([]string, error)

	// Export 导出一份可恢复的快照（备份用）。
	Export() ([]byte, error)

	// Import 用快照覆盖当前全部数据（恢复用）。
	Import(data []byte) error

	// Close 释放资源（例如关闭数据文件）。
	Close() error
}
