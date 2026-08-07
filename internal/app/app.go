package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/config"
	"github.com/LuTianTian001/JieShan/internal/vnext/adminauth"
	"github.com/LuTianTian001/JieShan/internal/vnext/gateway"
	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	"github.com/LuTianTian001/JieShan/internal/vnext/outbound"
	"github.com/LuTianTian001/JieShan/internal/vnext/probeexec"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextRuntime "github.com/LuTianTian001/JieShan/internal/vnext/runtime"
	vnextStore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const bootstrapPasswordFile = "initial-admin-password.txt"

type App struct {
	cfg                   config.Config
	runtime               *vnextRuntime.Runtime
	server                *http.Server
	logger                *slog.Logger
	bootstrapPasswordPath string
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if ctx == nil {
		return nil, errors.New("application context is required")
	}
	if logger == nil {
		logger = Logger(cfg.LogLevel)
	}
	bootstrap, err := prepareBootstrapPasswordDelivery(cfg.DataDir, cfg.AdminPassword)
	if err != nil {
		return nil, err
	}
	healthPolicy := routing.HealthPolicy{
		FailureThreshold: cfg.FailureThreshold,
		FailureWindow:    cfg.FailureWindow,
		Cooldown:         cfg.Cooldown,
		HalfOpenLease:    cfg.HalfOpenLease,
	}
	monitorFactory := vnextRuntime.MonitorFactoryFunc(func(dependencies vnextRuntime.MonitorDependencies) (vnextRuntime.BackgroundService, error) {
		executor, err := probeexec.New(
			dependencies.Registry,
			dependencies.Client,
			dependencies.Secrets,
			dependencies.CredentialEffects,
			probeexec.Options{},
		)
		if err != nil {
			return nil, fmt.Errorf("create active probe executor: %w", err)
		}
		scheduler, err := monitoring.NewScheduler(
			dependencies.Store,
			dependencies.Store,
			executor,
			monitoring.Options{
				HealthPolicy:         dependencies.HealthPolicy,
				PolicyProvider:       dependencies.Settings,
				PollInterval:         cfg.ProbePollInterval,
				LeaseDuration:        cfg.ProbeLeaseDuration,
				ProbeTimeout:         cfg.ProbeTimeout,
				MaxConcurrentModels:  cfg.ProbeMaxConcurrentModels,
				MaxConcurrentTargets: cfg.ProbeMaxConcurrentTargets,
				Owner:                "jieshan-monitor",
			},
		)
		if err != nil {
			return nil, fmt.Errorf("create model monitor scheduler: %w", err)
		}
		return scheduler, nil
	})

	runtime, err := vnextRuntime.Open(ctx, vnextRuntime.Options{
		DataDir:      cfg.DataDir,
		DatabasePath: cfg.DatabasePath,
		Database: vnextStore.Options{
			MaxOpenConns: cfg.DatabaseMaxOpenConns,
			MaxIdleConns: cfg.DatabaseMaxIdleConns,
		},
		WebDir:       cfg.WebDir,
		MasterKeyHex: cfg.MasterKeyHex,
		AdminAuth: adminauth.Options{
			InitialPassword:          bootstrap.password,
			PersistGeneratedPassword: bootstrap.persist,
			SessionTTL:               cfg.SessionTTL,
			AllowedOrigins:           cfg.AllowedOrigins,
			TrustProxyHeaders:        cfg.TrustProxy,
			SecureCookies:            cfg.SecureCookies,
		},
		Outbound: outbound.Options{
			AllowPrivate:          cfg.AllowPrivateUpstreams,
			DialTimeout:           cfg.UpstreamDialTimeout,
			ResponseHeaderTimeout: cfg.UpstreamResponseHeaderTimeout,
			MaxConnsPerHost:       cfg.UpstreamMaxConnsPerHost,
		},
		Gateway: gateway.Options{
			HealthPolicy:       healthPolicy,
			FirstOutputTimeout: cfg.FirstOutputTimeout,
			StreamIdleTimeout:  cfg.StreamIdleTimeout,
			RequestTimeout:     cfg.RequestTimeout,
			MaxAttempts:        cfg.MaxAttempts,
		},
		ProbeInterval:      cfg.ProbeInterval,
		CredentialCooldown: cfg.CredentialCooldown,
		MonitorFactory:     monitorFactory,
	})
	if err != nil {
		return nil, fmt.Errorf("open JieShan runtime: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           runtime,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	bootstrapPath := ""
	if bootstrap.path != "" {
		if info, statErr := os.Lstat(bootstrap.path); statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			bootstrapPath = bootstrap.path
		}
	}
	return &App{
		cfg: cfg, runtime: runtime, server: server, logger: logger,
		bootstrapPasswordPath: bootstrapPath,
	}, nil
}

func (app *App) Run(ctx context.Context) error {
	if app == nil || app.runtime == nil || app.server == nil {
		return errors.New("JieShan application is unavailable")
	}
	if ctx == nil {
		return errors.New("application context is required")
	}
	if app.bootstrapPasswordPath != "" {
		app.logger.Warn("initial administrator password is available",
			"username", adminauth.AdminUsername, "path", app.bootstrapPasswordPath)
	}

	app.logger.Info("JieShan starting", "listen", app.cfg.ListenAddr, "database", app.cfg.DatabasePath)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	backgroundErrors := make(chan error, 1)
	serverErrors := make(chan error, 1)
	go func() { backgroundErrors <- app.runtime.Run(runCtx) }()
	go func() {
		err := app.server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErrors <- err
	}()

	var cause error
	backgroundDone := false
	serverDone := false
	select {
	case <-ctx.Done():
	case err := <-backgroundErrors:
		backgroundDone = true
		if err != nil {
			cause = fmt.Errorf("JieShan background service stopped: %w", err)
		} else if ctx.Err() == nil {
			cause = errors.New("JieShan background service stopped unexpectedly")
		}
	case err := <-serverErrors:
		serverDone = true
		if err != nil {
			cause = fmt.Errorf("JieShan HTTP server stopped: %w", err)
		} else if ctx.Err() == nil {
			cause = errors.New("JieShan HTTP server stopped unexpectedly")
		}
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	shutdownErr := app.server.Shutdown(shutdownCtx)
	shutdownCancel()
	if errors.Is(shutdownErr, http.ErrServerClosed) {
		shutdownErr = nil
	}
	if shutdownErr != nil {
		forceCloseErr := app.server.Close()
		if errors.Is(forceCloseErr, http.ErrServerClosed) {
			forceCloseErr = nil
		}
		shutdownErr = errors.Join(shutdownErr, forceCloseErr)
	}

	wait := time.NewTimer(15 * time.Second)
	defer wait.Stop()
	for !backgroundDone || !serverDone {
		select {
		case err := <-backgroundErrors:
			backgroundDone = true
			if cause == nil && err != nil && !errors.Is(err, context.Canceled) {
				cause = fmt.Errorf("JieShan background service stopped: %w", err)
			}
		case err := <-serverErrors:
			serverDone = true
			if cause == nil && err != nil {
				cause = fmt.Errorf("JieShan HTTP server stopped: %w", err)
			}
		case <-wait.C:
			cause = errors.Join(cause, errors.New("JieShan shutdown timed out"))
			backgroundDone = true
			serverDone = true
		}
	}

	closeErr := app.runtime.Close()
	return errors.Join(cause, shutdownErr, closeErr)
}

func (app *App) Close() error {
	if app == nil {
		return nil
	}
	var serverErr error
	if app.server != nil {
		serverErr = app.server.Close()
		if errors.Is(serverErr, http.ErrServerClosed) {
			serverErr = nil
		}
	}
	var runtimeErr error
	if app.runtime != nil {
		runtimeErr = app.runtime.Close()
	}
	return errors.Join(serverErr, runtimeErr)
}

type bootstrapPasswordDelivery struct {
	password string
	path     string
	persist  func(string) error
}

func prepareBootstrapPasswordDelivery(dataDir, configured string) (bootstrapPasswordDelivery, error) {
	if password := strings.TrimSpace(configured); password != "" {
		return bootstrapPasswordDelivery{password: password}, nil
	}
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return bootstrapPasswordDelivery{}, errors.New("JieShan data directory is required")
	}
	path := filepath.Join(dataDir, bootstrapPasswordFile)
	password, exists, err := readBootstrapPassword(path)
	if err != nil {
		return bootstrapPasswordDelivery{}, err
	}
	if exists {
		return bootstrapPasswordDelivery{password: password, path: path}, nil
	}
	return bootstrapPasswordDelivery{
		path: path,
		persist: func(password string) error {
			return createBootstrapPassword(path, password)
		},
	}, nil
}

func readBootstrapPassword(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect initial administrator password: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false, errors.New("initial administrator password path must be a regular file")
	}
	if info.Size() <= 0 || info.Size() > 4<<10 {
		return "", false, errors.New("initial administrator password file has an invalid size")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read initial administrator password: %w", err)
	}
	password := strings.TrimSpace(string(content))
	clear(content)
	if password == "" {
		return "", false, errors.New("initial administrator password file is empty")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", false, fmt.Errorf("secure initial administrator password: %w", err)
	}
	return password, true, nil
}

func createBootstrapPassword(path, password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("generated administrator password is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create JieShan data directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("initial administrator password file was created concurrently; retry startup")
	}
	if err != nil {
		return fmt.Errorf("create initial administrator password: %w", err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	payload := password + "\n"
	written, err := io.WriteString(file, payload)
	if err != nil {
		return fmt.Errorf("write initial administrator password: %w", err)
	}
	if written != len(payload) {
		return fmt.Errorf("write initial administrator password: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync initial administrator password: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close initial administrator password: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure initial administrator password: %w", err)
	}
	complete = true
	return nil
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
