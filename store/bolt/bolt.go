// Package bolt 用 bbolt 实现 store.Store：纯 Go、单文件、ACID 事务。
//
// 单写者模型正好匹配配置中心的写入频率（改配置是低频人工操作）；
// 代价是**只能单实例**运行 —— 一个数据文件同一时刻只能被一个进程打开。
package bolt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	bolt "go.etcd.io/bbolt"

	"github.com/go31/confdeck/store"
)

// 桶布局：
//
//	meta          → "version"
//	schema        → scope → JSON Schema 原文
//	values/<scope> → KEY  → 值
var (
	bucketMeta   = []byte("meta")
	bucketSchema = []byte("schema")
	valuesBucket = func(scope string) []byte { return []byte("values/" + scope) }
)

// Store 是基于 bbolt 的存储实现。
type Store struct {
	db *bolt.DB
}

// Open 打开（不存在则创建）数据文件。权限 0600、所在目录 0700 —— 里面有明文密钥。
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建数据目录 %s：%w", dir, err)
		}
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("打开数据文件 %s：%w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketMeta); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketSchema); err != nil {
			return err
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Values(scope string) (map[string]string, error) {
	if err := store.CheckScope(scope); err != nil {
		return nil, err
	}
	out := map[string]string{}
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(valuesBucket(scope))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			out[string(k)] = string(v)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) PutValues(scope string, values map[string]string) error {
	if err := store.CheckScope(scope); err != nil {
		return err
	}
	for key := range values {
		if err := store.CheckKey(key); err != nil {
			return err
		}
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(valuesBucket(scope))
		if err != nil {
			return err
		}
		// 按 key 排序，保证写入顺序稳定（便于排查与复现）。
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := values[k]
			if v == "" { // 空值 = 删除
				if err := b.Delete([]byte(k)); err != nil {
					return err
				}
				continue
			}
			if err := b.Put([]byte(k), []byte(v)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Schema(scope string) ([]byte, error) {
	if err := store.CheckScope(scope); err != nil {
		return nil, err
	}
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketSchema).Get([]byte(scope)); v != nil {
			out = append([]byte(nil), v...)
		}
		return nil
	})
	return out, err
}

func (s *Store) PutSchema(scope string, schema []byte) error {
	if err := store.CheckScope(scope); err != nil {
		return err
	}
	if !json.Valid(schema) {
		return fmt.Errorf("%w：schema 不是合法 JSON", store.ErrInvalid)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSchema).Put([]byte(scope), schema)
	})
}

func (s *Store) Scopes() ([]string, error) {
	seen := map[string]bool{}
	err := s.db.View(func(tx *bolt.Tx) error {
		if err := tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			if scope, ok := cutPrefix(string(name), "values/"); ok {
				seen[scope] = true
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket(bucketSchema).ForEach(func(k, _ []byte) error {
			seen[string(k)] = true
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for scope := range seen {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out, nil
}

// snapshot 是 Export/Import 的线格式。
type snapshot struct {
	Version int                          `json:"version"`
	Schemas map[string]json.RawMessage   `json:"schemas,omitempty"`
	Values  map[string]map[string]string `json:"values,omitempty"`
}

func (s *Store) Export() ([]byte, error) {
	snap := snapshot{Version: 1, Schemas: map[string]json.RawMessage{}, Values: map[string]map[string]string{}}
	scopes, err := s.Scopes()
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if raw, err := s.Schema(scope); err != nil {
			return nil, err
		} else if len(raw) > 0 {
			snap.Schemas[scope] = raw
		}
		values, err := s.Values(scope)
		if err != nil {
			return nil, err
		}
		if len(values) > 0 {
			snap.Values[scope] = values
		}
	}
	return json.MarshalIndent(snap, "", "  ")
}

func (s *Store) Import(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("解析快照：%w", err)
	}
	if snap.Version != 1 {
		return fmt.Errorf("不支持的快照版本：%d", snap.Version)
	}
	for scope := range snap.Values {
		if err := store.CheckScope(scope); err != nil {
			return err
		}
	}
	for scope := range snap.Schemas {
		if err := store.CheckScope(scope); err != nil {
			return err
		}
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		// 先清空：Import 的语义是「用快照覆盖当前全部数据」。
		if err := tx.DeleteBucket(bucketSchema); err != nil && err != bolt.ErrBucketNotFound {
			return err
		}
		if _, err := tx.CreateBucket(bucketSchema); err != nil {
			return err
		}
		var stale [][]byte
		if err := tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			if _, ok := cutPrefix(string(name), "values/"); ok {
				stale = append(stale, append([]byte(nil), name...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, name := range stale {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}

		for scope, raw := range snap.Schemas {
			if err := tx.Bucket(bucketSchema).Put([]byte(scope), raw); err != nil {
				return err
			}
		}
		for scope, values := range snap.Values {
			b, err := tx.CreateBucketIfNotExists(valuesBucket(scope))
			if err != nil {
				return err
			}
			for k, v := range values {
				if err := store.CheckKey(k); err != nil {
					return err
				}
				if err := b.Put([]byte(k), []byte(v)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) Close() error { return s.db.Close() }

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return "", false
	}
	return s[len(prefix):], true
}

// 保证实现完整。
var _ store.Store = (*Store)(nil)
