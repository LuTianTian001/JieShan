package store

import "context"

type Dashboard struct {
	Upstreams         int64 `json:"upstreams"`
	EnabledRoutes     int64 `json:"enabledRoutes"`
	HealthyTargets    int64 `json:"healthyTargets"`
	CoolingTargets    int64 `json:"coolingTargets"`
	RequestsToday     int64 `json:"requestsToday"`
	FailuresToday     int64 `json:"failuresToday"`
	CostTodayMicroUSD int64 `json:"costTodayMicroUsd"`
}

func (s *Store) GetDashboard(ctx context.Context, dayStartMS int64) (Dashboard, error) {
	var item Dashboard
	err := s.DB.QueryRowContext(ctx, `SELECT
(SELECT COUNT(*) FROM upstreams),
(SELECT COUNT(*) FROM routes WHERE enabled=1),
(SELECT COUNT(*) FROM route_targets t
 JOIN upstream_credentials c ON c.id=t.credential_id
 LEFT JOIN target_health h ON h.target_id=t.id
 WHERE t.enabled=1 AND c.runtime_state='active' AND COALESCE(h.capability_state,'unknown')!='unsupported'
 AND COALESCE(h.circuit_phase,'closed')='closed'),
(SELECT COUNT(*) FROM target_health WHERE circuit_phase='open' AND cooldown_until>?),
(SELECT COUNT(*) FROM request_logs WHERE started_at>=?),
(SELECT COUNT(*) FROM request_logs WHERE started_at>=? AND status='failed'),
COALESCE((SELECT SUM(cost_micro_usd) FROM request_logs WHERE started_at>=?),0)`,
		NowMS(), dayStartMS, dayStartMS, dayStartMS).Scan(&item.Upstreams, &item.EnabledRoutes,
		&item.HealthyTargets, &item.CoolingTargets, &item.RequestsToday, &item.FailuresToday, &item.CostTodayMicroUSD)
	return item, err
}

func (s *Store) MonitorMatrix(ctx context.Context) ([]MonitorRow, error) {
	routes, err := s.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]MonitorRow, 0)
	for _, route := range routes {
		if !route.MonitorEnabled {
			continue
		}
		row := MonitorRow{RouteID: route.ID, PublicModel: route.PublicModel, Enabled: route.Enabled, Cells: []MonitorCell{}}
		for _, target := range route.Targets {
			status := target.LastProbeStatus
			if status == "" {
				status = "unknown"
			}
			if target.CircuitPhase == "open" {
				status = "cooldown"
			}
			if target.CapabilityState == "unsupported" {
				status = "unsupported"
			}
			if target.CredentialState == "invalid" || target.CredentialState == "revoked" {
				status = "credential_invalid"
			}
			row.Cells = append(row.Cells, MonitorCell{
				TargetID: target.ID, UpstreamID: target.UpstreamID, UpstreamName: target.UpstreamName,
				Status: status, LatencyMS: target.LastProbeLatency, CheckedAt: target.LastProbeAt,
				CooldownUntil: target.CooldownUntil,
			})
		}
		rows = append(rows, row)
	}
	return rows, nil
}
