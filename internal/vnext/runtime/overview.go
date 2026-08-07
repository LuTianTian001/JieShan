package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	"github.com/LuTianTian001/JieShan/internal/vnext/runtimeconfig"
	"github.com/LuTianTian001/JieShan/internal/vnext/settingsapi"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	runtimeOverviewHistoryLimit = 20
	meteringWarningWindow       = 24 * time.Hour
	backgroundHealthyAfter      = 5 * time.Second
)

type priceStateProvider interface {
	State(context.Context) (pricing.CatalogState, error)
}

type capacitySnapshotProvider interface {
	Snapshot() capacity.Snapshot
}

type appliedConfigStateProvider interface {
	appliedState() (int64, time.Time)
}

type runtimeOverviewProvider struct {
	startedAt  time.Time
	store      *vnextstore.Store
	capacity   capacitySnapshotProvider
	config     appliedConfigStateProvider
	prices     priceStateProvider
	background *backgroundTaskTracker
	now        func() time.Time
}

func newRuntimeOverviewProvider(
	startedAt time.Time,
	store *vnextstore.Store,
	capacity capacitySnapshotProvider,
	config appliedConfigStateProvider,
	prices priceStateProvider,
	background *backgroundTaskTracker,
) (*runtimeOverviewProvider, error) {
	if startedAt.IsZero() || store == nil || nilLike(capacity) || nilLike(config) || nilLike(prices) || background == nil {
		return nil, errors.New("runtime overview dependencies are required")
	}
	return &runtimeOverviewProvider{
		startedAt: startedAt.UTC(), store: store, capacity: capacity, config: config,
		prices: prices, background: background, now: time.Now,
	}, nil
}

func (provider *runtimeOverviewProvider) RuntimeOverview(ctx context.Context) (settingsapi.RuntimeOverview, error) {
	if provider == nil || provider.store == nil || provider.now == nil {
		return settingsapi.RuntimeOverview{}, errors.New("runtime overview provider is unavailable")
	}
	if ctx == nil {
		return settingsapi.RuntimeOverview{}, errors.New("runtime overview context is required")
	}
	now := provider.now().UTC()
	priceState, err := provider.prices.State(ctx)
	if err != nil {
		return settingsapi.RuntimeOverview{}, fmt.Errorf("load active price catalog: %w", err)
	}
	history, err := provider.store.ListConfigRevisions(ctx, runtimeOverviewHistoryLimit)
	if err != nil {
		return settingsapi.RuntimeOverview{}, fmt.Errorf("load configuration history: %w", err)
	}
	degradations, err := provider.store.SummarizeMeteringDegradation(
		ctx,
		now.Add(-meteringWarningWindow).UnixMilli(),
	)
	if err != nil {
		return settingsapi.RuntimeOverview{}, fmt.Errorf("load metering degradation: %w", err)
	}

	capacitySnapshot := provider.capacity.Snapshot()
	inflight := 0
	maximum := 0
	for _, site := range capacitySnapshot.Sites {
		inflight += site.InFlight
		if site.MaxInFlight > 0 {
			maximum += site.MaxInFlight
		}
	}
	activeRevision, loadedAt := provider.config.appliedState()
	if activeRevision <= 0 && len(history) > 0 {
		activeRevision = history[0].Revision
	}
	if loadedAt.IsZero() {
		loadedAt = provider.startedAt
	}
	warnings := presentMeteringWarnings(degradations)
	meteringMode := "normal"
	if len(warnings) > 0 {
		meteringMode = "degraded"
	}

	return settingsapi.RuntimeOverview{
		Runtime: settingsapi.GatewayRuntimeSnapshot{
			ProcessStartedAt: provider.startedAt.UnixMilli(), SnapshotAt: now.UnixMilli(),
			ConfigRevision: activeRevision, ConfigLoadedAt: loadedAt.UnixMilli(),
			ActivePriceCatalogVersion: priceState.ActiveVersion,
			InflightRequests:          inflight, MaxConcurrency: maximum,
			QueuedRequests: capacitySnapshot.Queued, MeteringMode: meteringMode,
		},
		MeteringWarnings: warnings,
		BackgroundTasks:  provider.background.snapshot(now),
		ConfigHistory:    presentConfigHistory(history, activeRevision, provider.startedAt),
	}, nil
}

func presentMeteringWarnings(items []vnextstore.MeteringDegradation) []settingsapi.MeteringWarning {
	result := make([]settingsapi.MeteringWarning, 0, len(items))
	for _, item := range items {
		warning := settingsapi.MeteringWarning{
			Code: item.Code, Severity: "warning", AffectedRequests: item.AffectedRequests,
			Since: item.Since, LastSeenAt: item.LastSeenAt,
		}
		switch item.Code {
		case "usage_unavailable":
			warning.Title = "上游未返回 Token 用量"
			warning.Message = "这些请求已正常返回，但缺少可信 usage 字段，因此没有按官方价格扣款。"
		case "pricing_settlement_failed":
			warning.Severity = "critical"
			warning.Title = "官方价格结算失败"
			warning.Message = "请求结果已返回，但价格目录或计量结算失败；额度未被错误扣除。"
		default:
			warning.Title = "部分请求无法完成官方计量"
			warning.Message = "系统保留了请求结果并标记计量降级，请在调用日志中按错误码排查。"
		}
		result = append(result, warning)
	}
	return result
}

func presentConfigHistory(
	items []runtimeconfig.RevisionEvent,
	activeRevision int64,
	startedAt time.Time,
) []settingsapi.ConfigHistoryEntry {
	result := make([]settingsapi.ConfigHistoryEntry, 0, len(items))
	for _, item := range items {
		createdAt := item.CreatedAt
		if createdAt.IsZero() || createdAt.UnixMilli() == 0 {
			createdAt = startedAt
		}
		actor, summary, fields := describeConfigReason(item.Reason)
		status := "superseded"
		if item.Revision == activeRevision {
			status = "applied"
		}
		result = append(result, settingsapi.ConfigHistoryEntry{
			ID: fmt.Sprintf("config-%d", item.Revision), Revision: item.Revision,
			Actor: actor, Summary: summary, ChangedFields: fields, Status: status,
			CreatedAt: createdAt.UTC().UnixMilli(),
		})
	}
	return result
}

func describeConfigReason(reason string) (string, string, []string) {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "schema_bootstrap":
		return "系统", "初始运行配置载入", []string{"数据库结构", "默认策略"}
	case "runtime_settings_updated":
		return "管理员", "运行策略已更新", []string{"健康策略", "超时策略", "日志保留"}
	case "site_created":
		return "管理员", "新增上游站点", []string{"上游站点", "容量配置"}
	case "site_updated":
		return "管理员", "上游站点配置已更新", []string{"上游站点", "容量配置"}
	case "token_json_imported":
		return "管理员", "Token JSON 已导入", []string{"上游密钥", "模型清单"}
	default:
		return "系统", "运行配置已更新", []string{"运行配置"}
	}
}

type backgroundTaskTracker struct {
	mu    sync.RWMutex
	order []string
	tasks map[string]*backgroundTaskRecord
}

type backgroundTaskRecord struct {
	id             string
	label          string
	schedule       string
	active         bool
	lastStartedAt  time.Time
	lastFinishedAt time.Time
	nextRunAt      time.Time
	lastDuration   time.Duration
	lastErrorCode  string
}

func newBackgroundTaskTracker(items []namedBackgroundService) *backgroundTaskTracker {
	tracker := &backgroundTaskTracker{tasks: make(map[string]*backgroundTaskRecord, len(items))}
	for _, item := range items {
		tracker.ensure(item)
	}
	return tracker
}

func (tracker *backgroundTaskTracker) started(item namedBackgroundService, at time.Time) {
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	record := tracker.ensureLocked(item)
	record.active = true
	record.lastStartedAt = at.UTC()
	record.nextRunAt = time.Time{}
	tracker.mu.Unlock()
}

func (tracker *backgroundTaskTracker) stopped(
	item namedBackgroundService,
	startedAt time.Time,
	finishedAt time.Time,
	err error,
	nextRunAt time.Time,
) {
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	record := tracker.ensureLocked(item)
	record.active = false
	record.lastFinishedAt = finishedAt.UTC()
	if !startedAt.IsZero() && finishedAt.After(startedAt) {
		record.lastDuration = finishedAt.Sub(startedAt)
	}
	record.nextRunAt = nextRunAt.UTC()
	record.lastErrorCode = backgroundErrorCode(err)
	tracker.mu.Unlock()
}

func (tracker *backgroundTaskTracker) snapshot(now time.Time) []settingsapi.BackgroundTaskHealth {
	if tracker == nil {
		return []settingsapi.BackgroundTaskHealth{}
	}
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	result := make([]settingsapi.BackgroundTaskHealth, 0, len(tracker.order))
	for _, id := range tracker.order {
		record := tracker.tasks[id]
		if record == nil {
			continue
		}
		state := "disabled"
		if record.active {
			state = "running"
			if !record.lastStartedAt.IsZero() && now.Sub(record.lastStartedAt) >= backgroundHealthyAfter {
				state = "healthy"
			}
		} else if !record.nextRunAt.IsZero() && record.nextRunAt.After(now) {
			state = "failed"
		} else if !record.lastStartedAt.IsZero() {
			state = "delayed"
		}
		result = append(result, settingsapi.BackgroundTaskHealth{
			ID: record.id, Label: record.label, State: state, Schedule: record.schedule,
			LastStartedAt:  timeMillisPointer(record.lastStartedAt),
			LastFinishedAt: timeMillisPointer(record.lastFinishedAt),
			NextRunAt:      timeMillisPointer(record.nextRunAt),
			LastDurationMS: durationMillisPointer(record.lastDuration),
			LastErrorCode:  record.lastErrorCode,
		})
	}
	return result
}

func (tracker *backgroundTaskTracker) ensure(item namedBackgroundService) {
	tracker.mu.Lock()
	tracker.ensureLocked(item)
	tracker.mu.Unlock()
}

func (tracker *backgroundTaskTracker) ensureLocked(item namedBackgroundService) *backgroundTaskRecord {
	id := strings.TrimSpace(item.id)
	if id == "" {
		id = backgroundTaskID(item.name)
	}
	if record := tracker.tasks[id]; record != nil {
		return record
	}
	label := strings.TrimSpace(item.label)
	if label == "" {
		label = strings.TrimSpace(item.name)
	}
	if label == "" {
		label = id
	}
	schedule := strings.TrimSpace(item.schedule)
	if schedule == "" {
		schedule = "持续运行"
	}
	record := &backgroundTaskRecord{id: id, label: label, schedule: schedule}
	tracker.tasks[id] = record
	tracker.order = append(tracker.order, id)
	return record
}

func backgroundTaskID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
		default:
			if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "_") {
				builder.WriteByte('_')
			}
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "background_task"
	}
	return result
}

func backgroundErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(err.Error()), "panic") {
		return "background_service_panic"
	}
	return "background_service_failed"
}

func timeMillisPointer(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	millis := value.UTC().UnixMilli()
	return &millis
}

func durationMillisPointer(value time.Duration) *int64 {
	if value <= 0 {
		return nil
	}
	millis := value.Milliseconds()
	return &millis
}

var _ settingsapi.RuntimeOverviewProvider = (*runtimeOverviewProvider)(nil)
