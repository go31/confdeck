# confdeck

配置中心 + 配置声明库。**声明只有一处**（Go 代码里），其余全部由它派生。

```go
type Vars struct {
	LogLevel   Config[string]
	Workers    Config[int]
	CacheTTL   Config[time.Duration]
	APIToken   Config[string]
	SigningKey Config[string]
}

var Spec = Vars{
	LogLevel: Config[string]{
		Name: "LOG_LEVEL", Default: "info",
		Desc: "日志级别，可选 info/debug/warn/error",
	},
	Workers: Config[int]{
		Name: "WORKERS", Default: 8,
		Desc:     "并发 Worker 数量。",
		Validate: confdeck.Positive,
	},
	CacheTTL: Config[time.Duration]{
		Name: "CACHE_TTL_SECONDS", Type: "seconds",
		Default: 5 * time.Minute, Parse: confdeck.Seconds,
		Desc: "缓存有效期。",
	},
	APIToken: Config[string]{
		Name: "API_TOKEN", Required: true, Secret: true,
		Desc: "调用上游 API 用的 token。",
	},
	SigningKey: Config[string]{
		Name: "SIGNING_KEY", Secret: true,
		Desc: "签名密钥，留空则回退到 API_TOKEN。",
	},
}
```

## 服务侧：注册 + 取值

```go
client := confdeck.NewClient("http://confdeck:8600", confdeck.WithToken(os.Getenv("CONFDECK_TOKEN")))

// 启动时注册自己的 schema：中心据此知道「这个服务应该有哪些配置」，
// 于是能在界面上标出哪些还没配、每个键什么含义、哪些是必填与敏感。
_ = client.Register(ctx, "api", &Spec)

v := Spec
confdeck.MustLoad(&v,
	// 进程环境优先：应急时给容器设个变量就能顶住，不用动中心
	confdeck.WithSource(confdeck.FromEnv()),
	// 主来源：先读 shared、再读本作用域（本作用域覆盖 shared）
	confdeck.WithSource(client.Source("api")),
	// 跨字段兜底：为空时回退到另一个变量（显式钩子，不藏在解析里）
	confdeck.WithResolve(func(v *Vars) {
		if v.SigningKey.Value == "" {
			v.SigningKey.Value = v.APIToken.Value
		}
	}),
	// 中心短暂不可用时降级为告警（真正缺失的必填项仍会被校验挡住）
	confdeck.WithSourceErrorHandler(func(source string, err error) {
		slog.Warn("读取配置来源失败", "source", source, "error", err)
	}),
)
```

## 服务端：`confdeckd`

```bash
confdeckd -addr :8600 -data /data/confdeck.db -token "$CONFDECK_TOKEN"
```

打开 `http://<addr>/ui/` 就是配置界面。一个二进制、一个数据文件、一个端口
（UI 与 CSS/JS 都内嵌在二进制里，没有构建步骤，升级就是换二进制）。
存储用 [bbolt](https://github.com/etcd-io/bbolt)（纯 Go、单文件、ACID 事务，
etcd 同款引擎），所以**只能单实例**运行；备份就是把那个文件拷走，
也可以走 JSON 快照（`store.Export` / `Import`）。

**鉴权只有一个 token**：所有 `/v1` 接口都要求 `Authorization: Bearer <token>` ——
服务取值、注册 schema、运维改配置共用它（少一个概念，也少一处配错的机会）。
`/healthz` 与 `/ui/` 静态页面不鉴权（页面里没有密钥，数据仍要 token 才取得到）。

| 接口 | 谁用 |
| --- | --- |
| `POST /v1/schema/{scope}` | 服务启动时注册自己的 schema |
| `GET /v1/schema/{scope}` | CLI / UI |
| `GET /v1/values/{scope}` | 服务取值 |
| `PUT /v1/values/{scope}` | 运维改配置（空串 = 删除该键） |
| `DELETE /v1/values/{scope}/{key}` | 运维删键 |
| `GET /v1/overview` | UI / CLI：缺哪些、哪些值不一致 |
| `GET /healthz` · `GET /ui/` | 探活 · 界面（不鉴权） |

没配 token 时接口是开放的 —— 启动日志会打印警告，请把端口只绑在回环上。

### 用容器跑（推荐）

```bash
docker run -d --name confdeck \
  -p 127.0.0.1:8600:8600 \
  -v confdeck-data:/data \
  -e CONFDECK_TOKEN='<你的 token>' \
  ghcr.io/go31/confdeck:beta
```

镜像约 22MB（alpine + 静态二进制），以非 root（uid 10001）运行，数据在 `/data/confdeck.db`，
自带 `HEALTHCHECK`。用 bind mount 代替命名卷时，注意宿主目录要能被 uid 10001 写。

放进别的项目编排里：

```yaml
confdeck:
  image: ghcr.io/go31/confdeck:beta
  restart: unless-stopped
  environment:
    CONFDECK_TOKEN: ${CONFDECK_TOKEN}
  volumes:
    - confdeck-data:/data
  ports:
    # 只绑回环：UI 走 SSH 隧道看（ssh -L 8600:127.0.0.1:8600 <server>）
    - "127.0.0.1:8600:8600"
```

发布：打 tag 即由 GitHub Actions 跑测试并推镜像到 GHCR。

```bash
git tag v0.1.0-beta.1 && git push origin v0.1.0-beta.1
# → ghcr.io/go31/confdeck:v0.1.0-beta.1  与  :beta
```

## 界面

深色、等宽、按服务声明的 schema 渲染，打开 `/ui/` 后在左下角粘贴 token 即可
（存在 sessionStorage，不进 cookie）。

- 左侧列出所有作用域；有缺项的显示「N 缺」并标红
- 主区是键的表格：**键 / 值（可直接改）/ 类型 / 标记 / 说明**
- 「标记」列会写出 `必填未配`、`需替换占位符`、`值不一致`、`未声明`、`来自 shared`
- 敏感项默认打码（可点「显示」）；值留空再保存就是删除该键
- 顶部有搜索与筛选（全部 / 需处理 / 未配置 / 已配置 / 未声明），以及「保存修改（N）」
- 有未保存改动时切作用域或关页面会被拦住

### 中心比普通 KV 多知道什么

因为它拿到了各服务注册的 schema，`/v1/overview` 能直接回答这些问题：

| 字段 | 含义 |
| --- | --- |
| `missing` | 必填但没配，或值仍是 `replace-with-*` 占位符（`External` 的项不算，见下） |
| `fromShared` | 这个值来自 `shared` 作用域（两个服务共用一份） |
| `divergent` | 同一个键被多个作用域声明，但**值不一致** —— 正是「一边改了、另一边忘了」 |
| `undeclared` | 中心里有这个值，但没有任何 schema 声明它（配置与代码已漂移） |
| `external` | 这一项由部署环境提供，中心不管：界面显示「部署提供」、不可编辑、不计入缺失 |
| `desc` / `type` / `default` / `required` / `secret` | 直接来自服务的声明，UI 不需要另写文档 |

### 由部署环境提供的值（`External`）

有些变量天生不属于配置中心 —— 例如 `DB_URL`：它由 compose 拼好直接注入进程环境，
中心永远看不到它。如果按普通必填项处理，界面就会**一直报「缺这一项」**（永久假警报）。

```go
DBURL: confdeck.Config[string]{
    Name: "DB_URL", Required: true, External: true, Secret: true,
    Desc: "连接串，由 compose 注入。",
},
```

`External` 对三件事有影响，且都与「进程侧」无关：

| 位置 | 行为 |
| --- | --- |
| 配置中心概览 | 不进 `missing`、不进 `alertKeys`；键上带 `external` 标记 |
| 中心界面 | 标「部署提供」、值只读（在中心里改只会造成误解） |
| `Schema` | 输出 `x-external: true`，且**不列进 `required`** —— 这份 schema 描述的是「中心里该有哪些键」 |
| 种子模板 | 整行注释掉并注明原因，免得运维照着模板白填一遍 |
| **进程侧校验** | **不受影响**：`Required` 照旧生效，缺了还是拒绝启动，错误信息会写「（由部署环境提供）」 |

换句话说：`External` 只改变「中心怎么看待它」，不改变「进程需不需要它」。
`Required + External` 是完全合法的组合，含义是「部署必须提供，但中心不管」。

## 设计要点

**键名只出现一次。** 传统写法里同一个键要在「元信息定义」和「读值」两处各写一遍，
时间一长必然漂移 —— 声明与读取对不上时，配置中心里改的值**毫无效果且不报错**，
这类静默失效最难查。这里声明即绑定。

**容器是唯一入口。** Go 无法枚举包级变量，所以所有变量必须放在一个结构体里，由反射收集。
好处是结构清晰、生成物顺序稳定；代价是一条使用约定 —— 容器里出现非 `Config[T]` 字段会直接
panic（这是声明错误，测试跑一遍就会暴露）。

**错误分两类处理**（刻意的）：

| 类型 | 例子 | 行为 |
| --- | --- | --- |
| 声明错误（写代码时犯的错） | 键名重复、`Required` 与 `Default` 同时给、`time.Duration` 没写明单位、容器里混入非 `Config` 字段 | **panic** —— 越早炸越好 |
| 值错误（配错了） | 必填项没配、值仍是占位符、解析失败、自定义校验失败 | **聚合成一条** `配置未就绪：…；…` 返回（`MustLoad` 则 panic） |

值错误聚合是有意的：panic 只报第一个，一次只暴露一个问题，有几个错就要启动几次才知道全貌。

**来源是批量的、可报错的。** `Source` 接口一次取回一批键值（而不是逐键 `Lookup`）：
远程来源一次请求就够，而且能返回「中心不可达」。空字符串统一按「未设置」处理
（沿用既有语义），所以本库不表达「显式设为空」。

**时长的单位必须写明**（`Parse` 与 `Type` 都显式）：秒与毫秒无法从类型推断，只要求 `Parse`
的话 `Type` 会派生成 `duration`，界面会误导运维去填 `30s` 而不是 `30`。

## 预置解析器与校验器

```go
confdeck.Seconds / Millis / Duration   // 时长（秒 / 毫秒 / Go 写法）
confdeck.CSV / CSVInt64                // 逗号分隔（去掉空白项）
confdeck.Trim / TrimAt / TrimSlash     // 去掉空白 / 开头的 @ / 结尾的 /
confdeck.Positive / AtMost(n)          // 校验器
```

## 产出

```go
confdeck.Schema(&Spec, "api")     // JSON Schema（带 x-secret / x-type），注册给中心用
confdeck.Dotenv(&Spec, "api")     // 种子模板（带注释、默认值、必填标记）
confdeck.Markdown(&Spec, "api")   // 人读的变量清单
confdeck.Missing(&Spec)           // 还没配好的键
```

## 目录

```
confdeck/
├── *.go                    库：声明 + 加载 + 校验 + 生成物 + 客户端（零外部依赖）
├── store/                  Store 接口与校验（只有服务端需要）
│   └── bolt/               bbolt 实现（默认存储）
├── server/                 HTTP API + 鉴权（web/ 下的 UI 用 embed 打进二进制）
└── cmd/confdeckd/          服务端二进制
```

业务服务只 import 根包 —— 不会把 `server/`、`store/bolt`、内嵌 UI 与 bbolt 编进它的二进制
（Go 只编译被 import 的包）。

## 引用代价（实测）

用两个探针工程量过（Go 1.25.5 / windows-amd64，`-ldflags` 默认）：

| 探针 | 二进制 | 编译进去的外部模块 |
| --- | --- | --- |
| hello world | 1,599,488 字节 | — |
| **只 import 根包** | 5,037,056 字节（+3.4MB） | **只有 `github.com/go31/confdeck`** |
| 连服务端一起用 | 7,287,296 字节 | confdeck + bbolt + x/sys |

- ✅ **服务端代码不参与**：`server/`、`store/`、`embed`、bbolt 都不在根包的依赖里（已用
  `go list -deps` 核对）。
- ⚠️ **+3.4MB** 来自 `client.go` 里的 `net/http`（连带 `crypto/tls`、`crypto/x509`）。
  只对「完全不接中心、只把 confdeck 当声明与校验库」的用法有影响；接中心的服务本来就有
  HTTP 栈，这 3.4MB 早就付过了。
- ⚠️ **`go.sum` 里会有 bbolt**（模块图要求它），`go mod download` 会拉下来 —— 只占磁盘，
  不参与编译。
- ⚠️ **最低 Go 版本被抬到 1.25**（bbolt v1.5.0 的要求）。

**刻意不拆包**：注册 schema 与取值都要用 `Client`，只要服务接中心，`net/http` 就一定在 ——
把 client 拆成子包省不掉任何东西，只会多一个 import 路径要记。

将来真要动，有两个便宜的选择：

| 想解决 | 怎么做 |
| --- | --- |
| 让老 Go 项目也能引用 | 降 bbolt：`v1.4.3` 要求 Go 1.23、`v1.3.11` 要求 Go 1.22；再改 `go.mod` 的 go 指令即可 |
| 让库变成零依赖 | 把服务端（`server/` + `store/` + `cmd/`）拆成独立 module，库模块回到 `go 1.22` + 无 `go.sum` |

两者都是机械改动，但都会破坏引用方的 import 路径或构建方式 —— 所以现在不做（还没有外部使用者）。

## 路线图

| 期 | 内容 | 状态 |
| --- | --- | --- |
| 1 | 库：`Config[T]` + 容器反射 + 来源 + 校验 + Schema/模板/文档 | ✅ |
| 2 | 客户端（注册 + 取值）、`store/bolt`、服务端 API + 鉴权 + `confdeckd` | ✅ |
| 3 | 内嵌 UI（缺项标红、直接编辑、密钥打码）+ 统一 token 鉴权 | ✅ |
| 4 | `confdeckctl`：`check` / `schema` / `export` / `import` / `push` | 待做 |

## 开发

```bash
go test ./...          # 全部测试
go test -cover ./...   # 覆盖率（根包 82% / server 81% / store 80%）
```

`example_test.go` 里的示例是可运行的（`go test` 时一起校验输出）。
