package confdeck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SharedScope 是跨服务共享的作用域名：多个服务都读它，值只需配一次。
//
// 取值时先读 shared、再读本作用域，**本作用域覆盖 shared**（更具体的胜出）。
// 中心会把「同一个键在多个作用域里值不同」标记出来，避免两边悄悄跑偏。
const SharedScope = "shared"

// Client 是配置中心的客户端：注册自己的 schema、取值。
//
// 服务只需要它，不需要依赖服务端与存储。
type Client struct {
	addr  string
	token string
	http  *http.Client
}

// ClientOption 调整客户端行为。
type ClientOption func(*Client)

// WithToken 设置 Bearer token（服务端开启了鉴权时必须给）。
func WithToken(token string) ClientOption {
	return func(c *Client) { c.token = token }
}

// WithHTTPClient 换掉底层 HTTP 客户端（例如加超时、加 TLS 配置）。
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) { c.http = hc }
}

// NewClient 创建客户端。addr 形如 http://confdeck:8600。
func NewClient(addr string, opts ...ClientOption) *Client {
	c := &Client{
		addr: strings.TrimRight(addr, "/"),
		http: &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Register 把容器的 schema 注册到中心（幂等，可以每次启动都调）。
//
// 中心据此知道「这个服务应该有哪些配置」，从而在 UI 上显示哪些还没配、
// 每个键是什么含义、哪些是必填与敏感。注册失败不该阻止服务启动 —— 由调用方决定。
func (c *Client) Register(ctx context.Context, scope string, dst any) error {
	raw, err := Schema(dst, scope)
	if err != nil {
		return fmt.Errorf("生成 schema：%w", err)
	}
	_, err = c.do(ctx, http.MethodPost, "/v1/schema/"+scope, raw)
	if err != nil {
		return fmt.Errorf("注册 schema 到 %s：%w", c.addr, err)
	}
	return nil
}

// Source 返回一个从中心取值的来源：shared 与本作用域合并，本作用域覆盖 shared。
//
//	v := Spec
//	confdeck.MustLoad(&v,
//		confdeck.WithSource(confdeck.FromEnv()),          // 应急覆盖，优先级最高
//		confdeck.WithSource(client.Source("api")),        // 主来源
//	)
func (c *Client) Source(scope string) Source {
	return clientSource{client: c, scope: scope}
}

// Ping 探活（给健康检查与 CLI 用）。
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/healthz", nil)
	return err
}

// PutValues 写入某作用域的值（值为空串表示**删除**该键）。需要管理 token。
//
// 库本身是给服务用的（读 + 注册 schema）；这个写接口是给 confdeckctl 与测试用的。
func (c *Client) PutValues(ctx context.Context, scope string, values map[string]string) error {
	body, err := json.Marshal(map[string]any{"values": values})
	if err != nil {
		return err
	}
	if _, err := c.do(ctx, http.MethodPut, "/v1/values/"+scope, body); err != nil {
		return fmt.Errorf("写入 %s：%w", scope, err)
	}
	return nil
}

// Overview 拉取中心概览：每个作用域声明了哪些键、哪些还没配、哪些值不一致。需要管理 token。
func (c *Client) Overview(ctx context.Context) (*Overview, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/overview", nil)
	if err != nil {
		return nil, err
	}
	var out Overview
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析概览响应：%w", err)
	}
	return &out, nil
}

type clientSource struct {
	client *Client
	scope  string
}

func (s clientSource) Name() string { return "confdeck/" + s.scope }

func (s clientSource) Values(ctx context.Context) (map[string]string, error) {
	shared, err := s.client.values(ctx, SharedScope)
	if err != nil {
		return nil, err
	}
	own, err := s.client.values(ctx, s.scope)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(shared)+len(own))
	for k, v := range shared {
		out[k] = v
	}
	for k, v := range own {
		out[k] = v // 本作用域覆盖 shared
	}
	return out, nil
}

func (c *Client) values(ctx context.Context, scope string) (map[string]string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/values/"+scope, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("解析 %s 的响应：%w", scope, err)
	}
	return payload.Values, nil
}

// do 发一个请求并返回响应体；非 2xx 会把服务端的错误信息带出来。
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.addr+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &payload) == nil && payload.Error != "" {
			return nil, fmt.Errorf("%s：%s", resp.Status, payload.Error)
		}
		return nil, fmt.Errorf("%s：%s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}
