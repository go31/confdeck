package confdeck

// Overview 是配置中心的概览：每个作用域声明了哪些键、哪些还没配、哪些值不一致。
//
// 它由服务端产出（GET /v1/overview），客户端与 UI 共用同一份结构，
// 避免两边各定义一次而漂移。
type Overview struct {
	Scopes []ScopeView `json:"scopes"`
	// Insecure 表示服务端没有配置任何 token（任何能访问端口的人都能读写）。
	Insecure bool `json:"insecure"`
	// AlertKeys 是所有「需要关注」的键（缺失、占位符、值不一致、未声明）的并集。
	AlertKeys []string `json:"alertKeys,omitempty"`
}

// ScopeView 是一个作用域的配置状况。
type ScopeView struct {
	Scope     string `json:"scope"`
	HasSchema bool   `json:"hasSchema"`
	// Missing 是「必填但没配」或「值仍是占位符」的键。
	Missing []string  `json:"missing,omitempty"`
	Keys    []KeyView `json:"keys"`
}

// KeyView 是单个键的展示信息：声明（来自 schema）+ 当前值。
type KeyView struct {
	Key      string `json:"key"`
	Type     string `json:"type,omitempty"`
	Desc     string `json:"desc,omitempty"`
	Required bool   `json:"required,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	// External 表示这一项由部署环境提供，配置中心不该管它：界面显示「部署提供」、
	// 不提供就地编辑，也不把它算进 Missing。
	External bool `json:"external,omitempty"`
	// Shared 表示这一项被服务声明为**共享键**（Config.Shared）：值配在 shared 作用域，
	// 多个服务共用一份。界面上只在 shared 下展示一次，不在各服务作用域里重复。
	Shared bool `json:"shared,omitempty"`
	// Override 表示这是某个服务作用域里的一份**覆盖值**：该键本身是共享键，
	// 但本作用域也存了值 —— 按合并规则它会盖住 shared 的值，所以必须让人看见。
	Override bool `json:"override,omitempty"`
	// SharedMismatch 表示同一个键有的服务标了 Shared、有的没标（归属没谈拢）。
	SharedMismatch bool   `json:"sharedMismatch,omitempty"`
	Default        string `json:"default,omitempty"`
	Value          string `json:"value,omitempty"`
	// Present 表示这个值来自配置中心（false 表示用的是代码里的默认值）。
	Present bool `json:"present"`
	// FromShared 表示值来自共享作用域。
	FromShared bool `json:"fromShared,omitempty"`
	// Divergent 表示同一个键在多个作用域里被声明，而值不一致 —— 正是
	// 「一边改了、另一边忘了」的表现。
	Divergent bool `json:"divergent,omitempty"`
	// Undeclared 表示中心里有这个值，但没有任何 schema 声明它（配置与代码已漂移）。
	Undeclared bool `json:"undeclared,omitempty"`
	// DeclaredIn 列出声明了这个键的作用域。
	DeclaredIn []string `json:"declaredIn,omitempty"`
}
