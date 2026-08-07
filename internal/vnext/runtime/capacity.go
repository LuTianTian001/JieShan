package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/runtimeconfig"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const defaultCapacityPollInterval = 500 * time.Millisecond

type capacityReloadService struct {
	store      *vnextstore.Store
	controller capacity.Controller
	poller     *runtimeconfig.Poller
	interval   time.Duration

	stateMu        sync.RWMutex
	activeRevision int64
	loadedAt       time.Time
}

func newCapacityReloadService(
	ctx context.Context,
	store *vnextstore.Store,
	controller capacity.Controller,
	interval time.Duration,
) (*capacityReloadService, error) {
	if store == nil || controller == nil {
		return nil, errors.New("capacity reload store and controller are required")
	}
	latest, err := store.LatestConfigRevision(ctx)
	if err != nil {
		return nil, err
	}
	poller, err := runtimeconfig.NewPoller(runtimeconfig.PollerOptions{Repository: store, Cursor: latest.Cursor})
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = defaultCapacityPollInterval
	}
	service := &capacityReloadService{store: store, controller: controller, poller: poller, interval: interval}
	if err := service.reload(ctx); err != nil {
		return nil, err
	}
	service.recordApplied(latest.Revision, time.Now().UTC())
	return service, nil
}

func (service *capacityReloadService) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("capacity reload context is required")
	}
	ticker := time.NewTicker(service.interval)
	defer ticker.Stop()
	for {
		if _, err := service.poller.Poll(ctx, func(ctx context.Context, event runtimeconfig.RevisionEvent) error {
			if err := service.reload(ctx); err != nil {
				return err
			}
			service.recordApplied(event.Revision, time.Now().UTC())
			return nil
		}); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (service *capacityReloadService) reload(ctx context.Context) error {
	sites, err := service.store.ListSites(ctx)
	if err != nil {
		return err
	}
	configs := make([]capacity.SiteConfig, 0, len(sites))
	for _, site := range sites {
		if !site.Enabled {
			continue
		}
		configs = append(configs, capacity.SiteConfig{
			SiteID: capacity.SiteID(site.ID), MaxInFlight: site.MaxInFlight,
		})
	}
	return service.controller.ReplaceSites(configs)
}

func (service *capacityReloadService) appliedState() (int64, time.Time) {
	if service == nil {
		return 0, time.Time{}
	}
	service.stateMu.RLock()
	defer service.stateMu.RUnlock()
	return service.activeRevision, service.loadedAt
}

func (service *capacityReloadService) recordApplied(revision int64, loadedAt time.Time) {
	if service == nil || revision <= 0 || loadedAt.IsZero() {
		return
	}
	service.stateMu.Lock()
	if revision >= service.activeRevision {
		service.activeRevision = revision
		service.loadedAt = loadedAt.UTC()
	}
	service.stateMu.Unlock()
}
