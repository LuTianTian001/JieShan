package monitor

import (
	"reflect"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestDueProbeJobsSchedulesEachTargetIndependently(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	recent := now.Add(-time.Minute).UnixMilli()
	overdue := now.Add(-10 * time.Minute).UnixMilli()
	routes := []store.Route{
		{
			ID: 1, Enabled: true, MonitorEnabled: true, MonitorIntervalSeconds: 300,
			Targets: []store.RouteTarget{
				{ID: 11, Enabled: true, CredentialState: "active", LastProbeAt: &recent},
				{ID: 12, Enabled: true, CredentialState: "active"},
				{ID: 13, Enabled: true, CredentialState: "active", LastProbeAt: &overdue},
				{ID: 14, Enabled: true, CredentialState: "active", CapabilityState: "unsupported", LastProbeAt: &overdue},
				{ID: 15, Enabled: true, CredentialState: "active", CapabilityState: "unsupported"},
				{ID: 16, Enabled: true, CredentialState: "invalid"},
			},
		},
		{ID: 2, Enabled: true, MonitorEnabled: false, Targets: []store.RouteTarget{{ID: 21, Enabled: true, CredentialState: "active"}}},
	}

	jobs := dueProbeJobs(routes, now)
	ids := make([]int64, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.targetID)
	}
	if want := []int64{12, 13, 15}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("due target ids = %v, want %v", ids, want)
	}
}

func TestUnsupportedTargetsUseSlowReprobeInterval(t *testing.T) {
	route := store.Route{MonitorIntervalSeconds: 300}
	regular := targetProbeInterval(route, store.RouteTarget{})
	unsupported := targetProbeInterval(route, store.RouteTarget{CapabilityState: "unsupported"})
	if regular != 5*time.Minute || unsupported != time.Hour {
		t.Fatalf("regular=%s unsupported=%s", regular, unsupported)
	}
}
