package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

// ResolverRouteSnapshot freezes the effective profile decision separately
// from the canonical published model revision used for accounting.
type ResolverRouteSnapshot struct {
	PublishedModelID       int64
	PublishedModelRevision int64
	RoutingProfileID       int64
	RoutingProfileName     string
	SourceProfileID        int64
	SourceProfileName      string
	Inherited              bool
	RouteRevision          int64
	PublicModel            string
	OfficialPriceSKU       string
	Targets                []ResolverRouteTarget
}

type ResolverRouteTarget struct {
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	TargetRevision               int64
	Position                     int
	SiteID                       int64
	SiteName                     string
	EndpointID                   int64
	EndpointName                 string
	BaseURL                      string
	WireProtocol                 string
	Surface                      string
	AuthScheme                   string
	AdapterKind                  string
	HeaderTemplate               json.RawMessage
	SecretHeadersConfigured      bool
	SecretHeadersCipherVersion   int64
	SourceModel                  string
	Credentials                  []ResolverCredential
}

type ResolverCredential struct {
	ID           int64
	Name         string
	Position     int
	RuntimeState string
	CoolingUntil *int64
}

func (s *Store) LoadResolverRoute(ctx context.Context, routingProfileID int64, publicModel string) (ResolverRouteSnapshot, error) {
	publicModel = strings.TrimSpace(publicModel)
	if routingProfileID <= 0 || publicModel == "" {
		return ResolverRouteSnapshot{}, sql.ErrNoRows
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ResolverRouteSnapshot{}, err
	}
	defer tx.Rollback()
	var modelID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM published_models WHERE public_name=? COLLATE BINARY`, publicModel).Scan(&modelID); err != nil {
		return ResolverRouteSnapshot{}, err
	}
	item, err := loadResolverRouteByIDTx(ctx, tx, routingProfileID, modelID)
	if err != nil {
		return ResolverRouteSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return ResolverRouteSnapshot{}, err
	}
	return item, nil
}

func (s *Store) ListResolverRoutes(ctx context.Context, routingProfileID int64) ([]ResolverRouteSnapshot, error) {
	if routingProfileID <= 0 {
		return nil, errors.New("routing profile ID must be positive")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM routing_profiles WHERE id=?`, routingProfileID).Scan(&exists); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM published_models ORDER BY public_name COLLATE BINARY,id`)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]ResolverRouteSnapshot, 0, len(ids))
	for _, modelID := range ids {
		item, err := loadResolverRouteByIDTx(ctx, tx, routingProfileID, modelID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func loadResolverRouteByIDTx(ctx context.Context, tx *sql.Tx, profileID, modelID int64) (ResolverRouteSnapshot, error) {
	effective, err := loadEffectiveRoutingProfileRouteTx(ctx, tx, profileID, modelID)
	if err != nil {
		return ResolverRouteSnapshot{}, err
	}
	if !effective.Enabled {
		return ResolverRouteSnapshot{}, sql.ErrNoRows
	}
	item := ResolverRouteSnapshot{
		PublishedModelID: effective.PublishedModelID, PublishedModelRevision: effective.PublishedModelRevision,
		RoutingProfileID: effective.RoutingProfileID, RoutingProfileName: effective.RoutingProfileName,
		SourceProfileID: effective.SourceProfileID, SourceProfileName: effective.SourceProfileName,
		Inherited: effective.Inherited, RouteRevision: effective.Revision, PublicModel: effective.PublicName,
		OfficialPriceSKU: effective.OfficialPriceSKU, Targets: make([]ResolverRouteTarget, 0, len(effective.Targets)),
	}
	for routePosition, canonical := range effective.Targets {
		var target ResolverRouteTarget
		var headerTemplate []byte
		var secretConfigured int
		err := tx.QueryRowContext(ctx, `SELECT t.id,t.revision,p.id,p.revision,p.site_id,s.name,p.endpoint_id,e.name,
e.base_url,e.wire_protocol,e.surface,e.auth_scheme,e.adapter_kind,e.header_template_json,
CASE WHEN e.secret_headers_cipher IS NOT NULL THEN 1 ELSE 0 END,e.cipher_version,p.source_model
FROM published_model_targets t
JOIN provider_model_targets p ON p.id=t.provider_model_target_id
JOIN sites s ON s.id=p.site_id
JOIN site_endpoints e ON e.id=p.endpoint_id AND e.site_id=p.site_id
WHERE t.id=? AND t.published_model_id=? AND p.enabled=1 AND s.enabled=1 AND e.enabled=1`, canonical.ID, modelID).Scan(
			&target.PublishedModelTargetID, &target.PublishedModelTargetRevision,
			&target.ProviderModelTargetID, &target.TargetRevision, &target.SiteID, &target.SiteName,
			&target.EndpointID, &target.EndpointName, &target.BaseURL, &target.WireProtocol, &target.Surface,
			&target.AuthScheme, &target.AdapterKind, &headerTemplate, &secretConfigured,
			&target.SecretHeadersCipherVersion, &target.SourceModel)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return ResolverRouteSnapshot{}, err
		}
		target.Position = routePosition
		target.HeaderTemplate = append(json.RawMessage(nil), headerTemplate...)
		target.SecretHeadersConfigured = secretConfigured == 1
		credentialRows, err := tx.QueryContext(ctx, `SELECT b.credential_id,c.name,b.position,rs.state,rs.cooling_until
FROM credential_endpoint_bindings b
JOIN site_credentials c ON c.id=b.credential_id AND c.site_id=b.site_id
JOIN credential_runtime_state rs ON rs.credential_id=c.id
LEFT JOIN credential_target_access a ON a.credential_id=b.credential_id
 AND a.provider_model_target_id=? AND a.endpoint_id=b.endpoint_id AND a.site_id=b.site_id
WHERE b.site_id=? AND b.endpoint_id=? AND b.enabled=1 AND c.enabled=1
 AND (a.availability IS NULL OR a.availability IN ('unknown','supported'))
ORDER BY b.position,b.credential_id`, target.ProviderModelTargetID, target.SiteID, target.EndpointID)
		if err != nil {
			return ResolverRouteSnapshot{}, err
		}
		for credentialRows.Next() {
			var credential ResolverCredential
			var coolingUntil sql.NullInt64
			if err := credentialRows.Scan(&credential.ID, &credential.Name, &credential.Position, &credential.RuntimeState, &coolingUntil); err != nil {
				credentialRows.Close()
				return ResolverRouteSnapshot{}, err
			}
			if coolingUntil.Valid {
				value := coolingUntil.Int64
				credential.CoolingUntil = &value
			}
			target.Credentials = append(target.Credentials, credential)
		}
		if err := credentialRows.Close(); err != nil {
			return ResolverRouteSnapshot{}, err
		}
		if err := credentialRows.Err(); err != nil {
			return ResolverRouteSnapshot{}, err
		}
		item.Targets = append(item.Targets, target)
	}
	return item, nil
}

func (s *Store) LoadResolverTargetHealth(ctx context.Context, targetIDs []int64) (map[int64]routing.HealthState, error) {
	result := make(map[int64]routing.HealthState, len(targetIDs))
	if len(targetIDs) == 0 {
		return result, nil
	}
	seen := make(map[int64]struct{}, len(targetIDs))
	args := make([]any, 0, len(targetIDs))
	placeholders := make([]string, 0, len(targetIDs))
	for _, targetID := range targetIDs {
		if targetID <= 0 {
			return nil, fmt.Errorf("provider model target ID must be positive: %d", targetID)
		}
		if _, duplicate := seen[targetID]; duplicate {
			continue
		}
		seen[targetID] = struct{}{}
		args = append(args, targetID)
		placeholders = append(placeholders, "?")
	}
	rows, err := s.DB.QueryContext(ctx, targetHealthSelect+`
WHERE provider_model_target_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		snapshot, err := scanTargetHealth(rows)
		if err != nil {
			return nil, err
		}
		result[snapshot.ProviderModelTargetID] = snapshot.State
	}
	return result, rows.Err()
}
