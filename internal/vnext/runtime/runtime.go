// Package runtime is the VNext composition root. It assembles the clean
// modular-monolith stack without importing or falling back to legacy Metapi
// runtime behavior.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/adminauth"
	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/capacityapi"
	"github.com/LuTianTian001/JieShan/internal/vnext/controlapi"
	"github.com/LuTianTian001/JieShan/internal/vnext/dataplane"
	"github.com/LuTianTian001/JieShan/internal/vnext/downstreamkeys"
	"github.com/LuTianTian001/JieShan/internal/vnext/gateway"
	"github.com/LuTianTian001/JieShan/internal/vnext/inventoryapi"
	"github.com/LuTianTian001/JieShan/internal/vnext/logsapi"
	"github.com/LuTianTian001/JieShan/internal/vnext/monitorapi"
	"github.com/LuTianTian001/JieShan/internal/vnext/outbound"
	"github.com/LuTianTian001/JieShan/internal/vnext/platformdetect"
	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	"github.com/LuTianTian001/JieShan/internal/vnext/pricingapi"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/anthropic"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/gemini"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/openai"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/retention"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	"github.com/LuTianTian001/JieShan/internal/vnext/routingapi"
	"github.com/LuTianTian001/JieShan/internal/vnext/runtimekey"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	"github.com/LuTianTian001/JieShan/internal/vnext/settings"
	"github.com/LuTianTian001/JieShan/internal/vnext/settingsapi"
	"github.com/LuTianTian001/JieShan/internal/vnext/siteadmin"
	"github.com/LuTianTian001/JieShan/internal/vnext/siteadminapi"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
	"github.com/LuTianTian001/JieShan/internal/vnext/systemlog"
	"github.com/LuTianTian001/JieShan/internal/vnext/systemlogapi"
)

const (
	DefaultDatabaseName        = "jieshan.sqlite"
	HealthPath                 = "/healthz"
	InventoryAdminPrefix       = "/api/vnext/inventory"
	DownstreamKeysAdminPrefix  = "/api/vnext/downstream-keys"
	RoutingProfilesAdminPrefix = routingapi.APIPrefix
	SiteAccountsAdminPrefix    = "/api/vnext/site-accounts"
	PricingAdminPrefix         = "/api/vnext/pricing"
	RequestLogsAdminPrefix     = "/api/vnext/request-logs"
	SettingsAdminPrefix        = settingsapi.APIPrefix
	MonitorAdminPrefix         = monitorapi.APIPrefix
	SystemLogsAdminPrefix      = systemlogapi.APIPrefix
	CapacityAdminPrefix        = capacityapi.APIPrefix
	AuthPrefix                 = adminauth.AuthAPIPrefix
)

// BackgroundService is the lifecycle boundary for bounded schedulers owned by
// Runtime.Run, including monitoring, account synchronization, and retention.
type BackgroundService interface {
	Run(context.Context) error
}

// MonitorDependencies are the already-composed VNext runtime capabilities a
// probe implementation needs. The factory prevents the runtime package from
// owning monitoring business logic or constructing a partially implemented
// scheduler.
type MonitorDependencies struct {
	Store             *vnextstore.Store
	Registry          *protocol.Registry
	Client            gateway.HTTPDoer
	Secrets           gateway.SecretProvider
	CredentialEffects gateway.CredentialEffectStore
	HealthPolicy      routing.HealthPolicy
	Settings          *settings.Service
}

type MonitorFactory interface {
	BuildMonitor(MonitorDependencies) (BackgroundService, error)
}

type MonitorFactoryFunc func(MonitorDependencies) (BackgroundService, error)

func (factory MonitorFactoryFunc) BuildMonitor(dependencies MonitorDependencies) (BackgroundService, error) {
	if factory == nil {
		return nil, errors.New("monitor factory is unavailable")
	}
	return factory(dependencies)
}

type Options struct {
	DataDir              string
	DatabasePath         string
	Database             vnextstore.Options
	WebDir               string
	MasterKeyHex         string
	AdminAuth            adminauth.Options
	Outbound             outbound.Options
	Gateway              gateway.Options
	Capacity             capacity.Config
	CapacityPollInterval time.Duration
	ProbeInterval        time.Duration
	CredentialCooldown   time.Duration
	MonitorFactory       MonitorFactory
	Retention            retention.Options
	SiteAccountSync      siteadmin.SchedulerOptions
	Logger               *slog.Logger
	SystemLog            systemlog.Options
	BackgroundRestartMin time.Duration
	BackgroundRestartMax time.Duration
}

type namedBackgroundService struct {
	id       string
	name     string
	label    string
	schedule string
	service  BackgroundService
}

// Runtime owns one independent VNext database, its outbound transport, every
// concrete protocol adapter, and the complete HTTP surface built on top of
// those components.
type Runtime struct {
	handler              http.Handler
	store                *vnextstore.Store
	outbound             *outbound.Client
	background           []namedBackgroundService
	auth                 *adminauth.Service
	logger               *slog.Logger
	systemLogs           *systemlog.Recorder
	capacity             *capacity.Manager
	backgroundTracker    *backgroundTaskTracker
	backgroundRestartMin time.Duration
	backgroundRestartMax time.Duration

	bootstrapMu       sync.Mutex
	bootstrapPassword string

	closeOnce sync.Once
	closeErr  error
}

const (
	defaultBackgroundRestartMin = time.Second
	defaultBackgroundRestartMax = 30 * time.Second
	backgroundStableRun         = 5 * time.Minute
)

func Open(ctx context.Context, options Options) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("JieShan runtime context is required")
	}
	startedAt := time.Now().UTC()
	if strings.TrimSpace(options.DataDir) == "" {
		return nil, errors.New("JieShan data directory is required")
	}
	if strings.TrimSpace(options.WebDir) == "" {
		return nil, errors.New("JieShan web distribution directory is required")
	}

	dataDir, err := filepath.Abs(strings.TrimSpace(options.DataDir))
	if err != nil {
		return nil, fmt.Errorf("resolve JieShan data directory: %w", err)
	}
	databasePath := strings.TrimSpace(options.DatabasePath)
	if databasePath == "" {
		databasePath = filepath.Join(dataDir, DefaultDatabaseName)
	} else if !filepath.IsAbs(databasePath) {
		databasePath = filepath.Join(dataDir, databasePath)
	}
	databasePath = filepath.Clean(databasePath)

	masterKey, err := runtimekey.LoadOrCreate(dataDir, options.MasterKeyHex)
	if err != nil {
		return nil, fmt.Errorf("load JieShan secret key: %w", err)
	}
	defer clear(masterKey)
	box, err := secretbox.New(masterKey)
	if err != nil {
		return nil, fmt.Errorf("create JieShan secret box: %w", err)
	}

	store, err := vnextstore.OpenWithOptions(ctx, databasePath, options.Database)
	if err != nil {
		return nil, fmt.Errorf("open JieShan database: %w", err)
	}
	if _, err := store.RecoverInterruptedRequests(ctx, time.Now().UTC().UnixMilli()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover interrupted JieShan requests: %w", err)
	}
	client := outbound.New(options.Outbound)
	systemLogOptions := options.SystemLog
	if strings.TrimSpace(systemLogOptions.Path) == "" {
		systemLogOptions.Path = filepath.Join(dataDir, "logs", "system.jsonl")
	}
	systemLogs, err := systemlog.New(systemLogOptions)
	if err != nil {
		client.CloseIdleConnections()
		_ = store.Close()
		return nil, fmt.Errorf("open JieShan system log: %w", err)
	}
	logger := slog.New(systemLogs)
	if options.Logger != nil {
		logger = slog.New(systemlog.Tee(options.Logger.Handler(), systemLogs))
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		client.CloseIdleConnections()
		_ = store.Close()
		_ = systemLogs.Close()
	}()
	capacityConfig := options.Capacity
	if capacityConfig == (capacity.Config{}) {
		capacityConfig = capacity.DefaultConfig()
	}
	capacityManager, err := capacity.New(capacityConfig)
	if err != nil {
		return nil, fmt.Errorf("create Site capacity manager: %w", err)
	}
	defer func() {
		if !committed {
			_ = capacityManager.Close()
		}
	}()
	capacityReloader, err := newCapacityReloadService(ctx, store, capacityManager, options.CapacityPollInterval)
	if err != nil {
		return nil, fmt.Errorf("load Site capacity configuration: %w", err)
	}

	authService, bootstrap, err := adminauth.NewService(ctx, store, options.AdminAuth)
	if err != nil {
		return nil, fmt.Errorf("create VNext administrator authentication: %w", err)
	}
	authHandler, err := adminauth.NewHandler(authService)
	if err != nil {
		return nil, fmt.Errorf("create VNext administrator authentication handler: %w", err)
	}

	registry, err := buildProtocolRegistry(client)
	if err != nil {
		return nil, err
	}
	siteRegistry, err := buildSiteAdminRegistry(client)
	if err != nil {
		return nil, err
	}
	platformDetector, err := platformdetect.New(client, siteRegistry)
	if err != nil {
		return nil, fmt.Errorf("create platform detector: %w", err)
	}
	priceService, err := pricing.NewRuntimeService(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("create runtime price service: %w", err)
	}
	keyService, err := downstreamkeys.New(store, box, authService)
	if err != nil {
		return nil, fmt.Errorf("create downstream key service: %w", err)
	}
	routeResolver, err := resolver.New(store, keyService, registry)
	if err != nil {
		return nil, fmt.Errorf("create route resolver: %w", err)
	}
	secrets, err := gateway.NewStoreSecretProvider(store, box)
	if err != nil {
		return nil, fmt.Errorf("create runtime secret provider: %w", err)
	}

	policy := normalizeHealthPolicy(options.Gateway.HealthPolicy)
	settingsService, err := settings.NewService(ctx, store, settings.Options{
		Initial: vnextstore.RuntimeSettingsWrite{
			FailureThreshold: policy.FailureThreshold, FailureWindow: policy.FailureWindow,
			Cooldown: policy.Cooldown, ProbeInterval: options.ProbeInterval,
			FirstOutputTimeout: options.Gateway.FirstOutputTimeout,
			StreamIdleTimeout:  options.Gateway.StreamIdleTimeout, RequestTimeout: options.Gateway.RequestTimeout,
			MaxAttempts: options.Gateway.MaxAttempts,
		},
		HalfOpenLease: policy.HalfOpenLease,
	})
	if err != nil {
		return nil, fmt.Errorf("create runtime settings service: %w", err)
	}
	policy = settingsService.Snapshot().HealthPolicy
	options.Gateway.HealthPolicy = policy
	options.Gateway.PolicyProvider = settingsService
	options.Gateway.Capacity = capacityManager
	retentionService, err := retention.New(store, settingsService, options.Retention)
	if err != nil {
		return nil, fmt.Errorf("create operational history retention service: %w", err)
	}
	credentialCooldown := options.CredentialCooldown
	if credentialCooldown <= 0 {
		credentialCooldown = policy.Cooldown
	}
	effects, err := gateway.NewStoreCredentialEffects(store, credentialCooldown)
	if err != nil {
		return nil, fmt.Errorf("create credential effect store: %w", err)
	}
	gatewayService, err := gateway.New(
		routeResolver,
		registry,
		store,
		secrets,
		effects,
		store,
		priceService,
		gateway.NewConservativeJSONReservationPlanner(),
		client,
		options.Gateway,
	)
	if err != nil {
		return nil, fmt.Errorf("create gateway service: %w", err)
	}
	dataHandler, err := dataplane.New(gatewayService, routeResolver)
	if err != nil {
		return nil, fmt.Errorf("create data-plane handler: %w", err)
	}
	inventoryHandler, err := inventoryapi.NewStoreHandlerWithPlatformDetector(store, box, registry, platformDetector)
	if err != nil {
		return nil, fmt.Errorf("create inventory handler: %w", err)
	}
	keyHandler, err := controlapi.NewStoreHandler(store, keyService)
	if err != nil {
		return nil, fmt.Errorf("create downstream key handler: %w", err)
	}
	routingHandler, err := routingapi.NewStoreHandler(store)
	if err != nil {
		return nil, fmt.Errorf("create routing profiles handler: %w", err)
	}
	siteAccountRepository, err := siteadminapi.NewStoreRepository(store, box)
	if err != nil {
		return nil, fmt.Errorf("create site account repository: %w", err)
	}
	siteAccountService, err := siteadmin.NewService(siteAccountRepository, siteRegistry)
	if err != nil {
		return nil, fmt.Errorf("create site account service: %w", err)
	}
	siteAccountHandler, err := siteadminapi.NewWithService(siteAccountRepository, siteRegistry, siteAccountService)
	if err != nil {
		return nil, fmt.Errorf("create site account handler: %w", err)
	}
	siteAccountScheduler, err := siteadmin.NewScheduler(siteAccountRepository, siteAccountService, options.SiteAccountSync)
	if err != nil {
		return nil, fmt.Errorf("create site account scheduler: %w", err)
	}
	pricingHandler, err := pricingapi.New(priceService)
	if err != nil {
		return nil, fmt.Errorf("create pricing handler: %w", err)
	}
	requestLogsHandler, err := logsapi.NewStoreHandler(store)
	if err != nil {
		return nil, fmt.Errorf("create request logs handler: %w", err)
	}
	systemLogsHandler, err := systemlogapi.New(systemLogs)
	if err != nil {
		return nil, fmt.Errorf("create system logs handler: %w", err)
	}
	capacityHandler, err := capacityapi.New(capacityManager)
	if err != nil {
		return nil, fmt.Errorf("create capacity snapshot handler: %w", err)
	}

	var monitor BackgroundService
	background := []namedBackgroundService{
		{
			id: "site_capacity_reload", name: "site capacity reload", label: "上游容量配置",
			schedule: fmt.Sprintf("配置变更后约 %s 内应用", capacityReloader.interval), service: capacityReloader,
		},
		{
			id: "operational_history_retention", name: "operational history retention", label: "运行记录清理",
			schedule: "按日志保留策略定期运行", service: retentionService,
		},
		{
			id: "site_account_synchronization", name: "site account synchronization", label: "上游账户同步",
			schedule: "按账户同步周期运行", service: siteAccountScheduler,
		},
	}
	if !nilLike(options.MonitorFactory) {
		monitor, err = options.MonitorFactory.BuildMonitor(MonitorDependencies{
			Store: store, Registry: registry, Client: client, Secrets: secrets,
			CredentialEffects: effects, HealthPolicy: policy, Settings: settingsService,
		})
		if err != nil {
			return nil, fmt.Errorf("build JieShan monitor: %w", err)
		}
		if nilLike(monitor) {
			return nil, errors.New("JieShan monitor factory returned no service")
		}
		background = append(background, namedBackgroundService{
			id: "model_monitoring", name: "model monitoring", label: "模型可用性监控",
			schedule: "按探针间隔运行", service: monitor,
		})
	}
	var modelProber monitorapi.ModelProber
	if candidate, ok := monitor.(monitorapi.ModelProber); ok && !nilLike(candidate) {
		modelProber = candidate
	}
	monitorHandler, err := monitorapi.NewStoreHandler(store, modelProber)
	if err != nil {
		return nil, fmt.Errorf("create monitor handler: %w", err)
	}
	backgroundTracker := newBackgroundTaskTracker(background)
	overviewProvider, err := newRuntimeOverviewProvider(
		startedAt, store, capacityManager, capacityReloader, priceService, backgroundTracker,
	)
	if err != nil {
		return nil, fmt.Errorf("create runtime overview provider: %w", err)
	}
	settingsHandler, err := settingsapi.NewServiceHandlerWithOverview(settingsService, overviewProvider)
	if err != nil {
		return nil, fmt.Errorf("create runtime settings handler: %w", err)
	}

	handler, err := composeHTTPHandler(
		authService, authHandler, options.WebDir, store, dataHandler, inventoryHandler, keyHandler,
		routingHandler, siteAccountHandler, pricingHandler, requestLogsHandler, systemLogsHandler,
		capacityHandler, monitorHandler, settingsHandler,
	)
	if err != nil {
		return nil, err
	}

	committed = true
	restartMin, restartMax := normalizeBackgroundRestart(options.BackgroundRestartMin, options.BackgroundRestartMax)
	logger.Info("JieShan runtime opened", "module", "runtime", "code", "runtime_opened")
	return &Runtime{
		handler: handler, store: store, outbound: client, background: background, auth: authService,
		bootstrapPassword: bootstrap.GeneratedPassword, logger: logger, systemLogs: systemLogs, capacity: capacityManager,
		backgroundTracker:    backgroundTracker,
		backgroundRestartMin: restartMin, backgroundRestartMax: restartMax,
	}, nil
}

func (runtime *Runtime) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if runtime == nil || runtime.handler == nil {
		http.Error(writer, "JieShan runtime is unavailable", http.StatusServiceUnavailable)
		return
	}
	runtime.handler.ServeHTTP(writer, request)
}

// Run owns every bounded background service and periodic administrator-session
// cleanup. A failed or panicking background service is restarted with bounded
// exponential backoff; it cannot take down the gateway data plane.
func (runtime *Runtime) Run(ctx context.Context) error {
	if runtime == nil {
		return errors.New("JieShan runtime is unavailable")
	}
	if ctx == nil {
		return errors.New("JieShan runtime context is required")
	}
	var background sync.WaitGroup
	for _, item := range runtime.background {
		if strings.TrimSpace(item.name) == "" || nilLike(item.service) {
			continue
		}
		background.Add(1)
		go func(item namedBackgroundService) {
			defer background.Done()
			runtime.superviseBackground(ctx, item)
		}(item)
	}
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			background.Wait()
			return nil
		case <-cleanup.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = runtime.auth.CleanupExpiredSessions(cleanupCtx)
			cancel()
		}
	}
}

func (runtime *Runtime) superviseBackground(ctx context.Context, item namedBackgroundService) {
	restartMin, restartMax := normalizeBackgroundRestart(runtime.backgroundRestartMin, runtime.backgroundRestartMax)
	logger := runtime.logger
	if logger == nil {
		logger = slog.Default()
	}
	delay := restartMin
	for ctx.Err() == nil {
		startedAt := time.Now().UTC()
		if runtime.backgroundTracker != nil {
			runtime.backgroundTracker.started(item, startedAt)
		}
		err := runBackgroundAttempt(ctx, item.service)
		finishedAt := time.Now().UTC()
		if ctx.Err() != nil {
			if runtime.backgroundTracker != nil {
				runtime.backgroundTracker.stopped(item, startedAt, finishedAt, nil, time.Time{})
			}
			return
		}
		logger.Error("JieShan background service stopped; restarting",
			"service", item.name, "error", err, "restart_after", delay)
		if time.Since(startedAt) >= backgroundStableRun {
			delay = restartMin
		}
		if runtime.backgroundTracker != nil {
			runtime.backgroundTracker.stopped(item, startedAt, finishedAt, err, finishedAt.Add(delay))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		if delay < restartMax {
			delay *= 2
			if delay > restartMax {
				delay = restartMax
			}
		}
	}
}

func runBackgroundAttempt(ctx context.Context, service BackgroundService) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("background service panic: %v", recovered)
		}
	}()
	err = service.Run(ctx)
	if err == nil && ctx.Err() == nil {
		return errors.New("background service stopped unexpectedly")
	}
	return err
}

func normalizeBackgroundRestart(minimum, maximum time.Duration) (time.Duration, time.Duration) {
	if minimum <= 0 {
		minimum = defaultBackgroundRestartMin
	}
	if maximum <= 0 {
		maximum = defaultBackgroundRestartMax
	}
	if maximum < minimum {
		maximum = minimum
	}
	return minimum, maximum
}

// TakeBootstrapPassword returns a generated first-run password once, then
// clears the runtime copy. An explicitly configured initial password is never
// returned by this method.
func (runtime *Runtime) TakeBootstrapPassword() string {
	if runtime == nil {
		return ""
	}
	runtime.bootstrapMu.Lock()
	defer runtime.bootstrapMu.Unlock()
	value := runtime.bootstrapPassword
	runtime.bootstrapPassword = ""
	return value
}

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.closeOnce.Do(func() {
		if runtime.outbound != nil {
			runtime.outbound.CloseIdleConnections()
		}
		var capacityErr, storeErr, logErr error
		if runtime.capacity != nil {
			capacityErr = runtime.capacity.Close()
		}
		if runtime.store != nil {
			storeErr = runtime.store.Close()
		}
		if runtime.systemLogs != nil {
			logErr = runtime.systemLogs.Close()
		}
		runtime.closeErr = errors.Join(capacityErr, storeErr, logErr)
	})
	return runtime.closeErr
}

func buildProtocolRegistry(client gateway.HTTPDoer) (*protocol.Registry, error) {
	registry := protocol.NewRegistry()
	chat, err := openai.NewChatCompletionsAdapter(client)
	if err != nil {
		return nil, fmt.Errorf("create OpenAI Chat Completions adapter: %w", err)
	}
	responses, err := openai.NewResponsesAdapter(client)
	if err != nil {
		return nil, fmt.Errorf("create OpenAI Responses adapter: %w", err)
	}
	messages, err := anthropic.NewMessagesAdapter(client)
	if err != nil {
		return nil, fmt.Errorf("create Anthropic Messages adapter: %w", err)
	}
	generate, err := gemini.NewGenerateContentAdapter(client)
	if err != nil {
		return nil, fmt.Errorf("create Gemini GenerateContent adapter: %w", err)
	}

	registrations := []struct {
		protocol protocol.Protocol
		surface  protocol.Surface
		adapter  any
	}{
		{protocol.OpenAI, protocol.OpenAIChatCompletions, chat},
		{protocol.OpenAI, protocol.OpenAIResponses, responses},
		{protocol.Anthropic, protocol.AnthropicMessages, messages},
		{protocol.Gemini, protocol.GeminiGenerateContent, generate},
	}
	for _, registration := range registrations {
		if _, err := registry.Register(registration.protocol, registration.surface, registration.adapter); err != nil {
			return nil, fmt.Errorf("register %s/%s adapter: %w", registration.protocol, registration.surface, err)
		}
	}
	return registry, nil
}

func buildSiteAdminRegistry(client gateway.HTTPDoer) (*siteadmin.Registry, error) {
	registry := siteadmin.NewRegistry()
	ciii, err := siteadmin.NewCiiiAdapter(client)
	if err != nil {
		return nil, fmt.Errorf("create Ciii site administration adapter: %w", err)
	}
	if err := registry.Register(ciii); err != nil {
		return nil, fmt.Errorf("register Ciii site administration adapter: %w", err)
	}
	newAPI, err := siteadmin.NewNewAPIAdapter(client)
	if err != nil {
		return nil, fmt.Errorf("create New API site administration adapter: %w", err)
	}
	if err := registry.Register(newAPI); err != nil {
		return nil, fmt.Errorf("register New API site administration adapter: %w", err)
	}
	oneAPI, err := siteadmin.NewOneAPIAdapter(client)
	if err != nil {
		return nil, fmt.Errorf("create One API site administration adapter: %w", err)
	}
	if err := registry.Register(oneAPI); err != nil {
		return nil, fmt.Errorf("register One API site administration adapter: %w", err)
	}
	sub2API, err := siteadmin.NewSub2APIAdapter(client)
	if err != nil {
		return nil, fmt.Errorf("create Sub2API site administration adapter: %w", err)
	}
	if err := registry.Register(sub2API); err != nil {
		return nil, fmt.Errorf("register Sub2API site administration adapter: %w", err)
	}
	return registry, nil
}

func normalizeHealthPolicy(policy routing.HealthPolicy) routing.HealthPolicy {
	defaults := routing.DefaultHealthPolicy()
	if policy.FailureThreshold < 2 {
		policy.FailureThreshold = defaults.FailureThreshold
	}
	if policy.FailureWindow <= 0 {
		policy.FailureWindow = defaults.FailureWindow
	}
	if policy.Cooldown <= 0 {
		policy.Cooldown = defaults.Cooldown
	}
	if policy.HalfOpenLease <= 0 {
		policy.HalfOpenLease = defaults.HalfOpenLease
	}
	return policy
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
