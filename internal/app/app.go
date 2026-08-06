package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/LuTianTian001/JieShan/internal/accountsync"
	"github.com/LuTianTian001/JieShan/internal/auth"
	"github.com/LuTianTian001/JieShan/internal/billing"
	"github.com/LuTianTian001/JieShan/internal/config"
	"github.com/LuTianTian001/JieShan/internal/gateway"
	"github.com/LuTianTian001/JieShan/internal/httpapi"
	"github.com/LuTianTian001/JieShan/internal/monitor"
	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
	"github.com/LuTianTian001/JieShan/internal/upstream"
)

type App struct {
	cfg       config.Config
	store     *store.Store
	server    *http.Server
	monitor   *monitor.Scheduler
	accounts  *accountsync.Service
	logger    *slog.Logger
	bootstrap string
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	fail := func(err error) (*App, error) { database.Close(); return nil, err }
	recovered, err := database.RecoverRunningRequests(ctx, store.NowMS())
	if err != nil {
		return fail(fmt.Errorf("recover interrupted requests: %w", err))
	}
	if recovered > 0 {
		logger.Warn("recovered interrupted requests", "count", recovered)
	}
	cipher, err := secrets.LoadOrCreate(cfg.DataDir, cfg.SecretKey)
	if err != nil {
		return fail(fmt.Errorf("load encryption key: %w", err))
	}
	authService := auth.New(database, cfg.SessionTTL)
	bootstrap, err := authService.EnsureAdmin(ctx, cfg.AdminPassword)
	if err != nil {
		return fail(fmt.Errorf("initialize administrator: %w", err))
	}
	upstreamClient := upstream.NewClient(database, cipher, cfg.UpstreamTimeout, upstream.ClientOptions{
		AllowPrivateUpstreams: cfg.AllowPrivateUpstreams,
	})
	accountHTTP := upstream.NewHTTPClient(cfg.UpstreamTimeout, upstream.ClientOptions{
		AllowPrivateUpstreams: cfg.AllowPrivateUpstreams,
	})
	accountService := accountsync.New(database, cipher, accountHTTP, logger, accountsync.DefaultInterval)
	priceEngine, err := billing.NewBuiltin()
	if err != nil {
		return fail(fmt.Errorf("load official price catalog: %w", err))
	}
	gatewayService := gateway.New(database, upstreamClient, priceEngine)
	api := httpapi.New(cfg, database, authService, cipher, upstreamClient, gatewayService, accountService)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return &App{cfg: cfg, store: database, server: server, monitor: monitor.New(database, gatewayService, logger), accounts: accountService, logger: logger, bootstrap: bootstrap}, nil
}

func (a *App) Run(ctx context.Context) error {
	if a.bootstrap != "" {
		path := a.cfg.DataDir + string(os.PathSeparator) + "initial-admin-password.txt"
		if err := os.WriteFile(path, []byte(a.bootstrap), 0o600); err != nil {
			return fmt.Errorf("write initial administrator password: %w", err)
		}
		a.logger.Warn("generated initial administrator password", "username", "admin", "path", path)
	}
	a.logger.Info("JieShan starting", "listen", a.cfg.ListenAddr, "database", a.cfg.DatabasePath)
	go a.monitor.Run(ctx)
	go a.accounts.Run(ctx)
	go a.cleanupLoop(ctx)
	errCh := make(chan error, 1)
	go func() {
		err := a.server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = a.server.Shutdown(shutdownCtx)
		return a.store.Close()
	case err := <-errCh:
		_ = a.store.Close()
		return err
	}
}

func (a *App) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		settings, err := a.store.GetSettings(ctx)
		if err == nil {
			cutoff := time.Now().Add(-time.Duration(settings.LogRetentionDays) * 24 * time.Hour).UnixMilli()
			_ = a.store.DeleteOldLogs(ctx, cutoff)
			_ = a.store.DeleteOldAccountData(ctx, cutoff)
			_ = a.store.DeleteExpiredSessions(ctx, time.Now().UnixMilli())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func Logger(level string) *slog.Logger {
	var parsed slog.Level
	switch level {
	case "debug":
		parsed = slog.LevelDebug
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}
