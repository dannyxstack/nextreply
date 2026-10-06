// nextreply-server：NextReply 中转服务。
//
//	nextreply-server            启动服务（默认）
//	nextreply-server migrate    只执行数据库迁移
//	nextreply-server backup F   把数据库快照写到文件 F（服务运行中也可执行）
//	nextreply-server healthcheck  请求本机 /v1/health，供容器健康检查使用
//	nextreply-server reset-limits 清空按 IP 的每日限额计数（开发测试用）
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata" // 镜像里没有系统时区数据，后台按 ADMIN_TZ 显示时间需要内置

	"github.com/nextreply/server/internal/admin"
	"github.com/nextreply/server/internal/ai"
	"github.com/nextreply/server/internal/app"
	"github.com/nextreply/server/internal/config"
	"github.com/nextreply/server/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "migrate":
		err = withDB(func(ctx context.Context, _ *config.Config, db dbHandle) error { return nil })
	case "backup":
		if len(os.Args) < 3 {
			err = errors.New("usage: nextreply-server backup <dest-file>")
			break
		}
		err = withDB(func(ctx context.Context, _ *config.Config, db dbHandle) error {
			return store.Backup(ctx, db, os.Args[2])
		})
	case "reset-limits":
		err = withDB(func(ctx context.Context, _ *config.Config, db dbHandle) error {
			n, err := store.ResetIPCounters(ctx, db)
			if err == nil {
				slog.Info("ip counters cleared", "rows", n)
			}
			return err
		})
	case "healthcheck":
		err = healthcheck()
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		slog.Error("fatal", "cmd", cmd, "err", err.Error())
		os.Exit(1)
	}
}

func withDB(fn func(context.Context, *config.Config, dbHandle) error) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	return fn(ctx, cfg, db)
}

func serve() error {
	return withDB(func(ctx context.Context, cfg *config.Config, db dbHandle) error {
		var gen ai.Generator
		if cfg.MockAI {
			slog.Warn("MOCK_AI enabled: replies are fixed and no model is called")
			gen = ai.Mock{}
		} else {
			gen = ai.NewClaude(ai.Settings{
				APIKey: cfg.AnthropicAPIKey, Thinking: cfg.Thinking, Fallbacks: cfg.Fallbacks, MaxTokens: cfg.MaxTokens,
			})
		}
		if cfg.AnthropicAPIKey == "" && !cfg.MockAI {
			slog.Warn("ANTHROPIC_API_KEY is empty: /v1/reply will fail")
		}
		if cfg.DevMode {
			slog.Warn("DEV_MODE enabled: login codes are shown on the page and mock checkout is available; never use in production")
		}
		srv := app.New(cfg, db, gen)

		ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		go janitor(ctx, db, srv)

		httpSrv := &http.Server{
			Addr:              cfg.ListenAddr,
			Handler:           srv.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			// 模型调用最长约 25s × 2（含一次重试）
			WriteTimeout: 90 * time.Second,
			IdleTimeout:  120 * time.Second,
		}
		errCh := make(chan error, 1)
		go func() {
			slog.Info("listening", "addr", cfg.ListenAddr, "db", cfg.DatabasePath,
				"model_trial", srv.Settings().Get("model.trial"), "model_free", srv.Settings().Get("model.free"),
				"model_pro", srv.Settings().Get("model.pro"), "model_pro_plus", srv.Settings().Get("model.pro_plus"))
			errCh <- httpSrv.ListenAndServe()
		}()

		// 管理后台：独立的监听地址，只对本机开放（见 TECH_DESIGN §5.8）
		var adminSrv *http.Server
		if cfg.AdminToken != "" {
			adm, err := admin.New(cfg, db, srv.Credits(), srv.Settings(), srv.IPLimits)
			if err != nil {
				return err
			}
			adminSrv = &http.Server{Addr: cfg.AdminAddr, Handler: adm.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
			go func() {
				slog.Info("admin listening", "addr", cfg.AdminAddr)
				errCh <- adminSrv.ListenAndServe()
			}()
		} else {
			slog.Info("admin disabled (ADMIN_TOKEN not set)")
		}

		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
		}
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if adminSrv != nil {
			_ = adminSrv.Shutdown(shutdownCtx)
		}
		return httpSrv.Shutdown(shutdownCtx)
	})
}

// janitor 定期清理过期数据、退还异常中断的预扣。
func janitor(ctx context.Context, db dbHandle, srv *app.Server) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if err := store.Cleanup(ctx, db, time.Now()); err != nil && ctx.Err() == nil {
			slog.Error("cleanup failed", "err", err.Error())
		}
		if err := srv.Credits().ExpireAllStaleHolds(ctx); err != nil && ctx.Err() == nil {
			slog.Error("expire holds failed", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func healthcheck() error {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8787"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/v1/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

type dbHandle = *sql.DB
