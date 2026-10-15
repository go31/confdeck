// Command confdeckd 是配置中心的服务端：一个二进制、一个数据文件、一个 HTTP 端口。
//
//	confdeckd -addr :8600 -data /data/confdeck.db -token "$CONFDECK_TOKEN"
//
// 打开 http://<addr>/ui/ 就是配置界面（深色、按服务声明的 schema 渲染）。
//
// 数据用 bbolt 存在单个文件里（纯 Go、ACID、单写者），所以**只能单实例**运行；
// 备份就是把那个文件拷走（Export/Import 也提供 JSON 快照）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go31/confdeck/server"
	"github.com/go31/confdeck/store/bolt"
)

// version 由构建时注入：-ldflags "-X main.version=v0.1.0-beta.1"（见 Dockerfile / CI）。
var version = "dev"

func main() {
	var (
		showVersion = flag.Bool("version", false, "打印版本并退出")
		addr        = flag.String("addr", ":8600", "监听地址")
		dataPath    = flag.String("data", "confdeck.db", "数据文件路径（bbolt 单文件）")
		token       = flag.String("token", os.Getenv("CONFDECK_TOKEN"),
			"访问 token：所有 /v1 接口共用它（服务取值、注册 schema、运维改配置）；留空表示不校验")
		quiet = flag.Bool("quiet", false, "只输出警告与错误")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("confdeckd " + version)
		return
	}

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelWarn
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	st, err := bolt.Open(*dataPath)
	if err != nil {
		logger.Error("打开数据文件失败", "error", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	srv := server.New(st, server.Options{
		Token:  *token,
		Logger: logger,
	})
	if srv.Insecure() {
		logger.Warn("未设置 token：任何能访问该端口的人都能读写配置。" +
			"请把端口只绑在回环上，或用 -token 开启鉴权")
	}

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("confdeckd 已启动", "version", version, "addr", *addr, "data", *dataPath)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP 服务退出", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("正在停止…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("停止时出错", "error", err)
	}
	logger.Info("已停止")
}
