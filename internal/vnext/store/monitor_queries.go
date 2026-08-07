package store

import (
	"context"
	"errors"
	"time"
)

type MonitorRouteView struct {
	Setting                ModelMonitorSetting
	PublicModel            string
	OfficialPriceSKU       string
	PublishedModelEnabled  bool
	PublishedModelRevision int64
	Targets                []MonitorTargetView
}

type MonitorTargetView struct {
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	ProviderModelTargetRevision  int64
	Position                     int
	SiteID                       int64
	SiteName                     string
	SiteEnabled                  bool
	EndpointID                   int64
	EndpointName                 string
	EndpointEnabled              bool
	SourceModel                  string
	ProviderTargetEnabled        bool
	WireProtocol                 string
	Surface                      string
	UsableCredentialCount        int
	Health                       *TargetHealthSnapshot
}

func (s *Store) CreateModelMonitorSetting(
	ctx context.Context,
	publishedModelID int64,
	input ModelMonitorSettingWrite,
	now time.Time,
) (ModelMonitorSetting, error) {
	if publishedModelID <= 0 || now.IsZero() {
		return ModelMonitorSetting{}, errors.New("published model ID and monitor setting time are required")
	}
	historyLimit, err := normalizeMonitorHistoryLimit(input)
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	nowMS := now.UTC().UnixMilli()
	result, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO model_monitor_settings(
published_model_id,enabled,interval_ms,history_limit,next_probe_at,
last_probe_started_at,last_probe_finished_at,lease_owner,lease_until,revision,created_at,updated_at)
SELECT id,?,(SELECT probe_interval_ms FROM runtime_settings WHERE singleton_id=1),?,?,NULL,NULL,NULL,NULL,1,?,?
FROM published_models WHERE id=?`, boolInt(input.Enabled), historyLimit,
		nowMS, nowMS, nowMS, publishedModelID)
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	if changed != 1 {
		if _, lookupErr := s.GetModelMonitorSetting(ctx, publishedModelID); lookupErr == nil {
			return ModelMonitorSetting{}, ErrConflict
		}
		return ModelMonitorSetting{}, ErrModelMonitorNotFound
	}
	return s.GetModelMonitorSetting(ctx, publishedModelID)
}

func (s *Store) UpdateModelMonitorSettingCAS(
	ctx context.Context,
	publishedModelID, expectedRevision int64,
	input ModelMonitorSettingWrite,
	now time.Time,
) (ModelMonitorSetting, error) {
	if publishedModelID <= 0 || expectedRevision <= 0 || now.IsZero() {
		return ModelMonitorSetting{}, errors.New("published model ID, expected revision, and monitor setting time are required")
	}
	historyLimit, err := normalizeMonitorHistoryLimit(input)
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	nowMS := now.UTC().UnixMilli()
	result, err := s.DB.ExecContext(ctx, `UPDATE model_monitor_settings SET
enabled=?,interval_ms=(SELECT probe_interval_ms FROM runtime_settings WHERE singleton_id=1),history_limit=?,
next_probe_at=CASE WHEN ?=1 AND (enabled=0 OR interval_ms<>(SELECT probe_interval_ms FROM runtime_settings WHERE singleton_id=1)) THEN ? ELSE next_probe_at END,
lease_owner=CASE WHEN ?=0 THEN NULL ELSE lease_owner END,
lease_until=CASE WHEN ?=0 THEN NULL ELSE lease_until END,
revision=revision+1,updated_at=? WHERE published_model_id=? AND revision=?`,
		boolInt(input.Enabled), historyLimit, boolInt(input.Enabled), nowMS,
		boolInt(input.Enabled), boolInt(input.Enabled), nowMS, publishedModelID, expectedRevision)
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	if changed != 1 {
		if _, lookupErr := s.GetModelMonitorSetting(ctx, publishedModelID); lookupErr == nil {
			return ModelMonitorSetting{}, ErrRevisionConflict
		}
		return ModelMonitorSetting{}, ErrModelMonitorNotFound
	}
	return s.GetModelMonitorSetting(ctx, publishedModelID)
}

func (s *Store) ListMonitorRouteViews(ctx context.Context) ([]MonitorRouteView, error) {
	settings, err := s.ListModelMonitorSettings(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]MonitorRouteView, 0, len(settings))
	for _, setting := range settings {
		model, err := s.GetPublishedModel(ctx, setting.PublishedModelID)
		if err != nil {
			return nil, err
		}
		view := MonitorRouteView{
			Setting: setting, PublicModel: model.PublicName, OfficialPriceSKU: model.OfficialPriceSKU,
			PublishedModelEnabled: model.Enabled, PublishedModelRevision: model.Revision,
		}
		rows, err := s.DB.QueryContext(ctx, `SELECT t.id,t.revision,p.id,p.revision,t.position,
s.id,s.name,s.enabled,e.id,e.name,e.enabled,p.source_model,p.enabled,e.wire_protocol,e.surface,
(SELECT COUNT(*) FROM credential_endpoint_bindings b
 JOIN site_credentials c ON c.id=b.credential_id AND c.site_id=b.site_id
 JOIN credential_runtime_state rs ON rs.credential_id=c.id
 LEFT JOIN credential_target_access a ON a.credential_id=c.id AND a.provider_model_target_id=p.id
 WHERE b.endpoint_id=e.id AND b.site_id=e.site_id AND b.enabled=1 AND c.enabled=1
   AND (rs.state='active' OR (rs.state='cooling' AND rs.cooling_until<=?))
   AND (a.availability IS NULL OR a.availability IN ('unknown','supported')))
FROM published_model_targets t
JOIN provider_model_targets p ON p.id=t.provider_model_target_id
JOIN sites s ON s.id=p.site_id
JOIN site_endpoints e ON e.id=p.endpoint_id AND e.site_id=p.site_id
WHERE t.published_model_id=?
ORDER BY t.position,t.id`, NowMS(), setting.PublishedModelID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var target MonitorTargetView
			var siteEnabled, endpointEnabled, providerEnabled int
			if err := rows.Scan(&target.PublishedModelTargetID, &target.PublishedModelTargetRevision,
				&target.ProviderModelTargetID, &target.ProviderModelTargetRevision, &target.Position,
				&target.SiteID, &target.SiteName, &siteEnabled, &target.EndpointID, &target.EndpointName,
				&endpointEnabled, &target.SourceModel, &providerEnabled,
				&target.WireProtocol, &target.Surface, &target.UsableCredentialCount); err != nil {
				rows.Close()
				return nil, err
			}
			target.SiteEnabled = siteEnabled == 1
			target.EndpointEnabled = endpointEnabled == 1
			target.ProviderTargetEnabled = providerEnabled == 1
			health, healthErr := s.GetTargetHealth(ctx, target.ProviderModelTargetID)
			if healthErr == nil {
				target.Health = &health
			} else if !errors.Is(healthErr, ErrTargetHealthNotFound) {
				rows.Close()
				return nil, healthErr
			}
			view.Targets = append(view.Targets, target)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}
