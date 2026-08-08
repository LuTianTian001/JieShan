package monitorapi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const APIPrefix = "/api/vnext/monitor"

type Repository interface {
	ListMonitorRouteViews(context.Context) ([]vnextstore.MonitorRouteView, error)
	GetModelMonitorSetting(context.Context, int64) (vnextstore.ModelMonitorSetting, error)
	CreateModelMonitorSetting(context.Context, int64, vnextstore.ModelMonitorSettingWrite, time.Time) (vnextstore.ModelMonitorSetting, error)
	UpdateModelMonitorSettingCAS(context.Context, int64, int64, vnextstore.ModelMonitorSettingWrite, time.Time) (vnextstore.ModelMonitorSetting, error)
	ListModelProbeTargetResults(context.Context, int64, int64, int) ([]vnextstore.ModelProbeTargetResult, error)
	ListMonitorTrafficObservations(context.Context, int64, int64, time.Time, time.Time, int) ([]vnextstore.MonitorTrafficObservation, error)
	GetRuntimeSettings(context.Context) (vnextstore.RuntimeSettings, error)
}

// ModelProber is intentionally the scheduler's narrow manual-probe surface.
// Runtime composition may inject a monitoring.Scheduler directly without the
// HTTP package owning scheduler lifecycle or probe execution.
type ModelProber interface {
	ProbeModel(context.Context, int64) (monitoring.ModelRun, error)
}

type TargetProber interface {
	ProbeTarget(context.Context, int64, int64) (monitoring.ModelRun, error)
}

type TargetsProber interface {
	ProbeTargets(context.Context, int64, []int64) (monitoring.ModelRun, error)
}

type ProbeModelFunc func(context.Context, int64) (monitoring.ModelRun, error)

func (function ProbeModelFunc) ProbeModel(ctx context.Context, publishedModelID int64) (monitoring.ModelRun, error) {
	if function == nil {
		return monitoring.ModelRun{}, errors.New("monitor probe function is unavailable")
	}
	return function(ctx, publishedModelID)
}

type Handler struct {
	repository Repository
	prober     ModelProber
	target     TargetProber
	targets    TargetsProber
	now        func() time.Time
}

// New accepts a nil prober so configuration and durable history remain
// available before a runtime installs protocol-specific probe execution.
func New(repository Repository, prober ModelProber) (*Handler, error) {
	if nilLike(repository) {
		return nil, errors.New("monitor repository is required")
	}
	if nilLike(prober) {
		prober = nil
	}
	handler := &Handler{repository: repository, prober: prober, now: time.Now}
	if target, ok := prober.(TargetProber); ok && !nilLike(target) {
		handler.target = target
	}
	if targets, ok := prober.(TargetsProber); ok && !nilLike(targets) {
		handler.targets = targets
	}
	return handler, nil
}

func NewStoreHandler(store *vnextstore.Store, prober ModelProber) (*Handler, error) {
	if store == nil {
		return nil, errors.New("VNext store is required")
	}
	return New(store, prober)
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
	_ http.Handler  = (*Handler)(nil)
	_ Repository    = (*vnextstore.Store)(nil)
	_ ModelProber   = (*monitoring.Scheduler)(nil)
	_ TargetProber  = (*monitoring.Scheduler)(nil)
	_ TargetsProber = (*monitoring.Scheduler)(nil)
)
