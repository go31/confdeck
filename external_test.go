package confdeck

import (
	"strings"
	"testing"
)

type externalVars struct {
	DBURL    Config[string]
	APIToken Config[string]
	LogLevel Config[string]
}

func externalSpec() externalVars {
	return externalVars{
		DBURL: Config[string]{
			Name: "DB_URL", Required: true, External: true, Secret: true,
			Desc: "连接串，由部署环境提供。",
		},
		APIToken: Config[string]{Name: "API_TOKEN", Required: true},
		LogLevel: Config[string]{Name: "LOG_LEVEL", Default: "info"},
	}
}

// Required 与 External 同时给出是合法的：含义是「部署必须提供，但中心不管」。
func TestExternalWithRequiredIsLegalDeclaration(t *testing.T) {
	spec := externalSpec()
	Validate(&spec) // 有声明问题会 panic
}

func TestExternalShowsUpInSpecs(t *testing.T) {
	spec := externalSpec()
	byKey := map[string]Spec{}
	for _, s := range Specs(&spec) {
		byKey[s.Key] = s
	}
	if !byKey["DB_URL"].External {
		t.Error("External 应出现在 Spec 里（UI 靠它显示「部署提供」）")
	}
	if byKey["API_TOKEN"].External {
		t.Error("普通项不该被标成 External")
	}
}

func TestExternalNotCountedAsMissing(t *testing.T) {
	// 一份没加载过的声明：API Token 必填没值，DB_URL 必填但由部署提供。
	spec := externalSpec()
	got := Missing(&spec)
	if len(got) != 1 || got[0] != "API_TOKEN" {
		t.Fatalf("External 不该进缺失，得到 %v", got)
	}
}

func TestExternalStillRequiredInProcess(t *testing.T) {
	// 部署没给 → 进程侧照样拒绝启动，且说清该去哪儿给。
	v := externalSpec()
	err := Load(&v, WithSource(FromMap(map[string]string{"API_TOKEN": "tok"})))
	if err == nil {
		t.Fatal("External + Required 缺值时应该报错")
	}
	if !strings.Contains(err.Error(), "DB_URL 未设置（由部署环境提供）") {
		t.Errorf("错误信息应说明由部署环境提供：%v", err)
	}

	// 部署给了 → 加载通过，缺失列表为空。
	ok := externalSpec()
	if err := Load(&ok, WithSource(FromMap(map[string]string{
		"API_TOKEN": "tok",
		"DB_URL":    "postgres://u:p@h/db",
	}))); err != nil {
		t.Fatalf("部署提供了值就该通过：%v", err)
	}
	if got := Missing(&ok); len(got) != 0 {
		t.Errorf("都配好了不该有缺失：%v", got)
	}
}

func TestExternalSchemaAndDocs(t *testing.T) {
	spec := externalSpec()

	doc, err := Schema(&spec, "api")
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	text := string(doc)
	if !strings.Contains(text, `"x-external": true`) {
		t.Error("schema 应标 x-external")
	}
	// required 描述的是「配置中心里该有哪些键」，External 的值住在部署环境里。
	if strings.Contains(text, `"required"`) && strings.Contains(text, `"DB_URL"`) {
		start := strings.Index(text, `"required"`)
		if strings.Contains(text[start:], `"DB_URL"`) {
			t.Error("External 的项不该出现在 required 里")
		}
	}

	md := Markdown(&spec, "api")
	if !strings.Contains(md, "由部署环境提供") {
		t.Error("文档应标明由部署环境提供")
	}

	seed := Dotenv(&spec, "api")
	for _, line := range strings.Split(seed, "\n") {
		if strings.HasPrefix(line, "api/DB_URL=") {
			t.Error("种子模板里 External 的项必须被注释掉（写进去也不生效）")
		}
	}
	if !strings.Contains(seed, "由部署环境提供") {
		t.Error("种子模板应注明原因")
	}
}
