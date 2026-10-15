package bolt

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/go31/confdeck/store"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sub", "confdeck.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestValuesRoundTrip(t *testing.T) {
	st := openTemp(t)

	if err := st.PutValues("api", map[string]string{"LOG_LEVEL": "debug", "WORKERS": "16"}); err != nil {
		t.Fatalf("PutValues: %v", err)
	}
	values, err := st.Values("api")
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if len(values) != 2 || values["LOG_LEVEL"] != "debug" || values["WORKERS"] != "16" {
		t.Fatalf("Values = %v", values)
	}

	// 空值 = 删除
	if err := st.PutValues("api", map[string]string{"LOG_LEVEL": ""}); err != nil {
		t.Fatalf("PutValues(删除): %v", err)
	}
	values, _ = st.Values("api")
	if _, ok := values["LOG_LEVEL"]; ok {
		t.Fatalf("空值应删除该键，实际 %v", values)
	}

	// 不存在的作用域返回空 map，而不是错误
	empty, err := st.Values("nobody")
	if err != nil || len(empty) != 0 {
		t.Fatalf("不存在的作用域应返回空 map：%v, %v", empty, err)
	}
}

func TestSchemaRoundTrip(t *testing.T) {
	st := openTemp(t)

	if raw, err := st.Schema("api"); err != nil || raw != nil {
		t.Fatalf("未注册时应返回 nil：%q, %v", raw, err)
	}
	schema := []byte(`{"properties":{"LOG_LEVEL":{"type":"string"}}}`)
	if err := st.PutSchema("api", schema); err != nil {
		t.Fatalf("PutSchema: %v", err)
	}
	raw, err := st.Schema("api")
	if err != nil || string(raw) != string(schema) {
		t.Fatalf("Schema = %q, %v", raw, err)
	}

	if err := st.PutSchema("api", []byte("{不是 JSON")); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("非法 schema 应返回 ErrInvalid，实际 %v", err)
	}
}

func TestScopesIncludesSchemaOnlyScopes(t *testing.T) {
	st := openTemp(t)
	if err := st.PutValues("api", map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSchema("worker", []byte(`{"properties":{"B":{"type":"string"}}}`)); err != nil {
		t.Fatal(err)
	}
	scopes, err := st.Scopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 || scopes[0] != "api" || scopes[1] != "worker" {
		t.Fatalf("Scopes = %v", scopes)
	}
}

func TestInvalidScopeAndKey(t *testing.T) {
	st := openTemp(t)
	if err := st.PutValues("API", map[string]string{"A": "1"}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("大写作用域名应被拒：%v", err)
	}
	if err := st.PutValues("api", map[string]string{"1BAD": "x"}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("数字开头的键名应被拒：%v", err)
	}
	if _, err := st.Values("../etc"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("路径穿越式作用域名应被拒：%v", err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	st := openTemp(t)
	if err := st.PutValues("api", map[string]string{"LOG_LEVEL": "debug"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutValues("shared", map[string]string{"API_TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSchema("api", []byte(`{"properties":{"LOG_LEVEL":{"type":"string"}}}`)); err != nil {
		t.Fatal(err)
	}

	snap, err := st.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// 换一个空库导入，验证恢复
	fresh := openTemp(t)
	if err := fresh.PutValues("api", map[string]string{"STALE": "should-be-gone"}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Import(snap); err != nil {
		t.Fatalf("Import: %v", err)
	}

	values, _ := fresh.Values("api")
	if len(values) != 1 || values["LOG_LEVEL"] != "debug" {
		t.Fatalf("导入后 api = %v（旧数据应被覆盖）", values)
	}
	shared, _ := fresh.Values("shared")
	if shared["API_TOKEN"] != "tok" {
		t.Fatalf("导入后 shared = %v", shared)
	}
	if raw, _ := fresh.Schema("api"); len(raw) == 0 {
		t.Fatal("导入后 schema 丢了")
	}

	if err := fresh.Import([]byte(`{"version":99}`)); err == nil {
		t.Fatal("不支持的快照版本应报错")
	}
}
