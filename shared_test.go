package confdeck

import (
	"strings"
	"testing"
)

type sharedVars struct {
	APIToken     Config[string]
	UpstreamAddr Config[string]
	LogLevel     Config[string]
}

func sharedSpec() sharedVars {
	return sharedVars{
		APIToken: Config[string]{
			Name: "API_TOKEN", Required: true, Secret: true, Shared: true,
			Desc: "两个服务共用一份。",
		},
		// 同名不同义：api 里是「上游的地址」、worker 里是「自己监听的地址」——
		// 这种键绝不能标 Shared，所以这里也不标。
		UpstreamAddr: Config[string]{Name: "UPSTREAM_ADDR", Desc: "上游地址。"},
		LogLevel:     Config[string]{Name: "LOG_LEVEL", Default: "info"},
	}
}

func TestSharedShowsUpInSpecs(t *testing.T) {
	spec := sharedSpec()
	byKey := map[string]Spec{}
	for _, s := range Specs(&spec) {
		byKey[s.Key] = s
	}
	if !byKey["API_TOKEN"].Shared {
		t.Error("Shared 应出现在 Spec 里（服务端靠它把键归到 shared 作用域）")
	}
	if byKey["UPSTREAM_ADDR"].Shared {
		t.Error("同名不同义的键不该被标成共享")
	}
}

func TestSharedSchemaAndArtifacts(t *testing.T) {
	spec := sharedSpec()

	doc, err := Schema(&spec, "api")
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if !strings.Contains(string(doc), `"x-shared": true`) {
		t.Error("schema 应标 x-shared")
	}
	// 共享键**要**留在 required 里：服务端靠它才知道这个共享键是必填的
	// （共享那一行的元信息取自各服务注册的 schema）。
	if !strings.Contains(string(doc), `"API_TOKEN"`) {
		t.Error("共享键应留在 required 里")
	}
	start := strings.Index(string(doc), `"required"`)
	if start < 0 || !strings.Contains(string(doc)[start:], `"API_TOKEN"`) {
		t.Errorf("共享键应出现在 required 里：%s", doc)
	}

	seed := Dotenv(&spec, "api")
	if !strings.Contains(seed, "shared/API_TOKEN=") {
		t.Error("种子模板应把共享键写到 shared/ 下（照模板填就得到正确布局）")
	}
	if strings.Contains(seed, "api/API_TOKEN=") {
		t.Error("种子模板不该再写一份 api/ 下的共享键")
	}
	if !strings.Contains(seed, "跨服务共享键") {
		t.Error("种子模板应注明这是共享键")
	}
	// 非共享键照旧写在服务作用域下
	if !strings.Contains(seed, "api/UPSTREAM_ADDR=") {
		t.Error("普通键应写在服务作用域下")
	}

	if md := Markdown(&spec, "api"); !strings.Contains(md, "跨服务共享（配在 shared）") {
		t.Error("文档应标明共享键")
	}
}

func TestSharedStillRequiredInProcess(t *testing.T) {
	// Shared 只影响「值配在哪、界面怎么展示」，进程侧该缺还是缺。
	v := sharedSpec()
	err := Load(&v, WithSource(FromMap(map[string]string{})))
	if err == nil || !strings.Contains(err.Error(), "API_TOKEN 未设置") {
		t.Fatalf("共享键缺失时进程侧仍应报错：%v", err)
	}
}
