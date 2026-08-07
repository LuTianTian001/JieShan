package settings

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/gateway"
	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

type Repository interface {
	GetRuntimeSettings(context.Context) (vnextstore.RuntimeSettings, error)
	InitializeRuntimeSettings(context.Context, vnextstore.RuntimeSettingsWrite, time.Time) (vnextstore.RuntimeSettings, error)
	UpdateRuntimeSettingsCAS(context.Context, int64, vnextstore.RuntimeSettingsWrite, time.Time) (vnextstore.RuntimeSettings, error)
}

type Options struct {
	Initial       vnextstore.RuntimeSettingsWrite
	HalfOpenLease time.Duration
	Now           func() time.Time
}

// Service owns the process-local publication of durable settings. Database
// CAS happens under updateMu, then one immutable record is atomically swapped
// so concurrent gateway requests never observe a partially updated policy.
type Service struct {
	repository    Repository
	halfOpenLease time.Duration
	now           func() time.Time
	updateMu      sync.Mutex
	current       atomic.Pointer[vnextstore.RuntimeSettings]
}

func NewService(ctx context.Context, repository Repository, options Options) (*Service, error) {
	if ctx == nil || nilLike(repository) {
		return nil, errors.New("runtime settings context and repository are required")
	}
	initial := withDefaults(options.Initial)
	if err := vnextstore.ValidateRuntimeSettingsWrite(initial); err != nil {
		return nil, err
	}
	halfOpenLease := options.HalfOpenLease
	if halfOpenLease == 0 {
		halfOpenLease = routing.DefaultHealthPolicy().HalfOpenLease
	}
	if halfOpenLease < time.Second || halfOpenLease > 10*time.Minute || halfOpenLease%time.Millisecond != 0 {
		return nil, errors.New("half-open lease must be a whole number of milliseconds between 1 second and 10 minutes")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	record, err := repository.InitializeRuntimeSettings(ctx, initial, options.Now().UTC())
	if err != nil {
		return nil, err
	}
	service := &Service{
		repository: repository, halfOpenLease: halfOpenLease, now: options.Now,
	}
	service.publish(record)
	return service, nil
}

func (service *Service) Current() vnextstore.RuntimeSettings {
	if service == nil {
		return vnextstore.RuntimeSettings{}
	}
	pointer := service.current.Load()
	if pointer == nil {
		return vnextstore.RuntimeSettings{}
	}
	return *pointer
}

func (service *Service) UpdateCAS(
	ctx context.Context,
	expectedRevision int64,
	input vnextstore.RuntimeSettingsWrite,
) (vnextstore.RuntimeSettings, error) {
	if service == nil || service.repository == nil {
		return vnextstore.RuntimeSettings{}, errors.New("runtime settings service is unavailable")
	}
	if expectedRevision <= 0 {
		return vnextstore.RuntimeSettings{}, errors.New("expected runtime settings revision must be positive")
	}
	if err := vnextstore.ValidateRuntimeSettingsWrite(input); err != nil {
		return vnextstore.RuntimeSettings{}, err
	}
	service.updateMu.Lock()
	defer service.updateMu.Unlock()
	record, err := service.repository.UpdateRuntimeSettingsCAS(ctx, expectedRevision, input, service.now().UTC())
	if err != nil {
		// A second process should not normally write this SQLite database, but
		// refreshing after a CAS conflict keeps the in-memory policy truthful.
		if errors.Is(err, vnextstore.ErrRevisionConflict) {
			if current, loadErr := service.repository.GetRuntimeSettings(ctx); loadErr == nil {
				service.publish(current)
			}
		}
		return vnextstore.RuntimeSettings{}, err
	}
	service.publish(record)
	return record, nil
}

func (service *Service) Snapshot() gateway.RuntimePolicy {
	if service == nil {
		return gateway.RuntimePolicy{}
	}
	return service.gatewayPolicy(service.Current())
}

func (service *Service) gatewayPolicy(record vnextstore.RuntimeSettings) gateway.RuntimePolicy {
	return gateway.RuntimePolicy{
		HealthPolicy: routing.HealthPolicy{
			FailureThreshold: record.FailureThreshold,
			FailureWindow:    record.FailureWindow,
			Cooldown:         record.Cooldown,
			HalfOpenLease:    service.halfOpenLease,
		},
		FirstOutputTimeout: record.FirstOutputTimeout,
		StreamIdleTimeout:  record.StreamIdleTimeout,
		RequestTimeout:     record.RequestTimeout,
		MaxAttempts:        record.MaxAttempts,
	}
}

func (service *Service) MonitoringSnapshot() monitoring.RuntimePolicy {
	if service == nil {
		return monitoring.RuntimePolicy{}
	}
	record := service.Current()
	policy := service.gatewayPolicy(record)
	return monitoring.RuntimePolicy{
		HealthPolicy: policy.HealthPolicy, ProbeInterval: record.ProbeInterval,
		FirstOutputTimeout: policy.FirstOutputTimeout,
	}
}

func (service *Service) publish(record vnextstore.RuntimeSettings) {
	copy := record
	service.current.Store(&copy)
}

func withDefaults(input vnextstore.RuntimeSettingsWrite) vnextstore.RuntimeSettingsWrite {
	defaults := vnextstore.DefaultRuntimeSettingsWrite()
	if input.FailureThreshold == 0 {
		input.FailureThreshold = defaults.FailureThreshold
	}
	if input.FailureWindow == 0 {
		input.FailureWindow = defaults.FailureWindow
	}
	if input.Cooldown == 0 {
		input.Cooldown = defaults.Cooldown
	}
	if input.ProbeInterval == 0 {
		input.ProbeInterval = defaults.ProbeInterval
	}
	if input.FirstOutputTimeout == 0 {
		input.FirstOutputTimeout = defaults.FirstOutputTimeout
	}
	if input.StreamIdleTimeout == 0 {
		input.StreamIdleTimeout = defaults.StreamIdleTimeout
	}
	if input.RequestTimeout == 0 {
		input.RequestTimeout = defaults.RequestTimeout
	}
	if input.MaxAttempts == 0 {
		input.MaxAttempts = defaults.MaxAttempts
	}
	if input.LogRetentionDays == 0 {
		input.LogRetentionDays = defaults.LogRetentionDays
	}
	return input
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

var (
	_ gateway.RuntimePolicyProvider    = (*Service)(nil)
	_ monitoring.RuntimePolicyProvider = (*Service)(nil)
	_ Repository                       = (*vnextstore.Store)(nil)
)
