package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/gateway"
	"github.com/LuTianTian001/JieShan/internal/store"
)

type Scheduler struct {
	store   *store.Store
	gateway *gateway.Gateway
	logger  *slog.Logger
}

func New(s *store.Store, g *gateway.Gateway, logger *slog.Logger) *Scheduler {
	return &Scheduler{store: s, gateway: g, logger: logger}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.runDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDue(ctx)
		}
	}
}

func (s *Scheduler) runDue(ctx context.Context) {
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		s.logger.Warn("monitor scheduler could not load routes", "error", err)
		return
	}
	jobs := dueProbeJobs(routes, time.Now())
	if len(jobs) == 0 {
		return
	}
	workers := 3
	if len(jobs) < workers {
		workers = len(jobs)
	}
	queue := make(chan probeJob)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for job := range queue {
				probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				_, err := s.gateway.ProbeRoute(probeCtx, job.routeID, &job.targetID)
				cancel()
				if err != nil {
					s.logger.Warn("target probe failed", "route_id", job.routeID, "target_id", job.targetID, "error", err)
				}
			}
		}()
	}
	for _, job := range jobs {
		select {
		case queue <- job:
		case <-ctx.Done():
			close(queue)
			group.Wait()
			return
		}
	}
	close(queue)
	group.Wait()
}

type probeJob struct {
	routeID  int64
	targetID int64
}

func dueProbeJobs(routes []store.Route, now time.Time) []probeJob {
	jobs := make([]probeJob, 0)
	for _, route := range routes {
		if !route.Enabled || !route.MonitorEnabled {
			continue
		}
		for _, target := range route.Targets {
			if !target.Enabled || target.CredentialState == "invalid" || target.CredentialState == "revoked" {
				continue
			}
			if target.LastProbeAt != nil {
				jitter := time.Duration((route.ID*17+target.ID*13)%30) * time.Second
				dueAt := time.UnixMilli(*target.LastProbeAt).Add(targetProbeInterval(route, target) + jitter)
				if now.Before(dueAt) {
					continue
				}
			}
			jobs = append(jobs, probeJob{routeID: route.ID, targetID: target.ID})
		}
	}
	return jobs
}

func targetProbeInterval(route store.Route, target store.RouteTarget) time.Duration {
	interval := time.Duration(route.MonitorIntervalSeconds) * time.Second
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	if target.CapabilityState != "unsupported" {
		return interval
	}
	interval *= 12
	if interval < time.Hour {
		return time.Hour
	}
	if interval > 24*time.Hour {
		return 24 * time.Hour
	}
	return interval
}
