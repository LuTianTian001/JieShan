package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/inferenceprotocol"
)

var (
	ErrLegacyMigrationBlocked                 = errors.New("legacy migration is blocked")
	ErrLegacyMigrationPlanFingerprintRequired = errors.New("legacy migration plan fingerprint is required")
	ErrLegacyMigrationPlanChanged             = errors.New("legacy migration plan changed")
)

type LegacyMigrationInventory struct {
	Upstreams           int `json:"upstreams"`
	Endpoints           int `json:"endpoints"`
	Credentials         int `json:"credentials"`
	Models              int `json:"models"`
	Routes              int `json:"routes"`
	RouteTargets        int `json:"routeTargets"`
	Accounts            int `json:"accounts"`
	AccountSnapshots    int `json:"accountSnapshots"`
	AccountUsageRecords int `json:"accountUsageRecords"`
}

type LegacyMigrationV3Inventory struct {
	Sites                    int `json:"sites"`
	Endpoints                int `json:"endpoints"`
	Credentials              int `json:"credentials"`
	SiteModels               int `json:"siteModels"`
	PublishedModels          int `json:"publishedModels"`
	RouteTargets             int `json:"routeTargets"`
	MappedUpstreams          int `json:"mappedUpstreams"`
	MappedRoutes             int `json:"mappedRoutes"`
	UnmanagedSites           int `json:"unmanagedSites"`
	UnmanagedPublishedModels int `json:"unmanagedPublishedModels"`
	Accounts                 int `json:"accounts"`
	AccountSnapshots         int `json:"accountSnapshots"`
	AccountUsageRecords      int `json:"accountUsageRecords"`
}

type LegacyMigrationPlan struct {
	Sites               int `json:"sites"`
	Endpoints           int `json:"endpoints"`
	Credentials         int `json:"credentials"`
	SiteModels          int `json:"siteModels"`
	PublishedModels     int `json:"publishedModels"`
	RouteTargets        int `json:"routeTargets"`
	SkippedRouteTargets int `json:"skippedRouteTargets"`
	Accounts            int `json:"accounts"`
	AccountSnapshots    int `json:"accountSnapshots"`
	AccountUsageRecords int `json:"accountUsageRecords"`
}

func (p LegacyMigrationPlan) empty() bool {
	return p.Sites == 0 && p.Endpoints == 0 && p.Credentials == 0 && p.SiteModels == 0 &&
		p.PublishedModels == 0 && p.RouteTargets == 0 && p.Accounts == 0 &&
		p.AccountSnapshots == 0 && p.AccountUsageRecords == 0
}

type LegacyMigrationConflict struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Count        int    `json:"count,omitempty"`
	Overrideable bool   `json:"overrideable"`
}

type LegacyMigrationPreview struct {
	PlanFingerprint string                     `json:"planFingerprint"`
	CanApply        bool                       `json:"canApply"`
	RequiresForce   bool                       `json:"requiresForce"`
	AlreadyApplied  bool                       `json:"alreadyApplied"`
	Legacy          LegacyMigrationInventory   `json:"legacy"`
	ExistingV3      LegacyMigrationV3Inventory `json:"existingV3"`
	Plan            LegacyMigrationPlan        `json:"plan"`
	Conflicts       []LegacyMigrationConflict  `json:"conflicts"`
	Warnings        []string                   `json:"warnings"`
}

type LegacyMigrationApplyResult struct {
	Applied   bool                   `json:"applied"`
	AppliedAt int64                  `json:"appliedAt,omitempty"`
	Created   LegacyMigrationPlan    `json:"created"`
	Preview   LegacyMigrationPreview `json:"preview"`
}

type LegacyMigrationBlockedError struct {
	Preview LegacyMigrationPreview
}

func (e *LegacyMigrationBlockedError) Error() string { return ErrLegacyMigrationBlocked.Error() }
func (e *LegacyMigrationBlockedError) Is(target error) bool {
	return target == ErrLegacyMigrationBlocked
}

type LegacyMigrationPlanChangedError struct {
	Expected string
	Preview  LegacyMigrationPreview
}

func (e *LegacyMigrationPlanChangedError) Error() string {
	return ErrLegacyMigrationPlanChanged.Error()
}
func (e *LegacyMigrationPlanChangedError) Is(target error) bool {
	return target == ErrLegacyMigrationPlanChanged
}

type legacyMigrationUpstream struct {
	ID           int64
	Name         string
	Kind         string
	DashboardURL string
	Enabled      bool
	Headers      []byte
}

type legacyMigrationEndpoint struct {
	ID         int64
	UpstreamID int64
	Name       string
	BaseURL    string
	Position   int
	Enabled    bool
}

type legacyMigrationCredential struct {
	ID           int64
	UpstreamID   int64
	Name         string
	SecretCipher []byte
	Position     int
	Enabled      bool
	RuntimeState string
}

type legacyMigrationModel struct {
	ID           int64
	UpstreamID   int64
	Name         string
	Enabled      bool
	Stale        bool
	MissingCount int
	LastSeenAt   *int64
}

type legacyMigrationRoute struct {
	ID                     int64
	PublicModel            string
	DisplayName            string
	Enabled                bool
	MonitorEnabled         bool
	MonitorIntervalSeconds int
	CooldownSeconds        int
	FailureThreshold       int
	FailureWindowSeconds   int
}

type legacyMigrationTarget struct {
	ID            int64
	RouteID       int64
	ModelID       int64
	EndpointID    int64
	CredentialID  int64
	ModelOverride string
	Position      int
	Enabled       bool
}

type legacyMigrationAccount struct {
	ID               int64
	UpstreamID       int64
	AdapterKind      string
	APIOrigin        string
	AuthCipher       []byte
	Enabled          bool
	Capabilities     []byte
	SyncState        string
	LastAttemptAt    *int64
	LastSuccessAt    *int64
	LastErrorCode    string
	LastErrorMessage string
	CreatedAt        int64
	UpdatedAt        int64
}

type legacyMigrationAccountSnapshot struct {
	ID         int64
	AccountID  int64
	Snapshot   []byte
	CapturedAt int64
}

type legacyMigrationAccountUsage struct {
	ID         int64
	AccountID  int64
	DedupeKey  string
	ExternalID string
	ModelName  string
	Amount     string
	Unit       string
	Raw        []byte
	OccurredAt *int64
	SyncedAt   int64
}

type legacyMigrationSnapshot struct {
	upstreams        []legacyMigrationUpstream
	endpoints        []legacyMigrationEndpoint
	credentials      []legacyMigrationCredential
	models           []legacyMigrationModel
	routes           []legacyMigrationRoute
	targets          []legacyMigrationTarget
	accounts         []legacyMigrationAccount
	accountSnapshots []legacyMigrationAccountSnapshot
	accountUsage     []legacyMigrationAccountUsage
	upstreamMappings map[int64]int64
	routeMappings    map[int64]int64
	v3               LegacyMigrationV3Inventory
	settings         Settings
}

type legacyMigrationSiteGroup struct {
	key        string
	origin     string
	name       string
	dashboard  string
	upstreams  []legacyMigrationUpstream
	mappedSite int64
}

type legacyMigrationAccountUnit struct {
	group             *legacyMigrationSiteGroup
	representative    legacyMigrationAccount
	existingAccountID int64
	capabilities      string
	snapshotsToCreate []legacyMigrationAccountSnapshot
	usageToCreate     []legacyMigrationAccountUsage
}

type legacyMigrationPlanState struct {
	preview         LegacyMigrationPreview
	snapshot        legacyMigrationSnapshot
	groups          []*legacyMigrationSiteGroup
	groupByUpstream map[int64]*legacyMigrationSiteGroup
	accountUnits    []legacyMigrationAccountUnit
	hardConflict    bool
}

type legacyMigrationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) PreviewLegacyMigration(ctx context.Context) (LegacyMigrationPreview, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LegacyMigrationPreview{}, err
	}
	defer tx.Rollback()
	state, err := buildLegacyMigrationPlanWithFingerprint(ctx, tx)
	if err != nil {
		return LegacyMigrationPreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return LegacyMigrationPreview{}, err
	}
	return state.preview, nil
}

func (s *Store) ApplyLegacyMigration(ctx context.Context, planFingerprint string, force bool) (LegacyMigrationApplyResult, error) {
	planFingerprint = strings.TrimSpace(planFingerprint)
	if planFingerprint == "" {
		return LegacyMigrationApplyResult{}, ErrLegacyMigrationPlanFingerprintRequired
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return LegacyMigrationApplyResult{}, err
	}
	defer tx.Rollback()
	state, err := buildLegacyMigrationPlanWithFingerprint(ctx, tx)
	if err != nil {
		return LegacyMigrationApplyResult{}, err
	}
	if planFingerprint != state.preview.PlanFingerprint {
		return LegacyMigrationApplyResult{}, &LegacyMigrationPlanChangedError{Expected: planFingerprint, Preview: state.preview}
	}
	if state.hardConflict || (state.preview.RequiresForce && !force) {
		return LegacyMigrationApplyResult{}, &LegacyMigrationBlockedError{Preview: state.preview}
	}
	if state.preview.AlreadyApplied || state.preview.Plan.empty() {
		if err := tx.Commit(); err != nil {
			return LegacyMigrationApplyResult{}, err
		}
		return LegacyMigrationApplyResult{Applied: false, Created: LegacyMigrationPlan{}, Preview: state.preview}, nil
	}
	created := state.preview.Plan
	appliedAt := NowMS()
	if err := applyLegacyMigrationPlan(ctx, tx, state, appliedAt); err != nil {
		return LegacyMigrationApplyResult{}, err
	}
	after, err := buildLegacyMigrationPlanWithFingerprint(ctx, tx)
	if err != nil {
		return LegacyMigrationApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return LegacyMigrationApplyResult{}, err
	}
	return LegacyMigrationApplyResult{Applied: true, AppliedAt: appliedAt, Created: created, Preview: after.preview}, nil
}

func buildLegacyMigrationPlanWithFingerprint(ctx context.Context, q legacyMigrationQueryer) (legacyMigrationPlanState, error) {
	state, err := buildLegacyMigrationPlan(ctx, q)
	if err != nil {
		return legacyMigrationPlanState{}, err
	}
	fingerprint, err := legacyMigrationPlanFingerprint(ctx, q, state)
	if err != nil {
		return legacyMigrationPlanState{}, err
	}
	state.preview.PlanFingerprint = fingerprint
	return state, nil
}

const legacyMigrationFingerprintVersion = "jieshan-legacy-migration-plan-v2"

var legacyMigrationFingerprintTables = []string{
	"app_settings",
	"upstreams",
	"upstream_endpoints",
	"upstream_credentials",
	"upstream_models",
	"routes",
	"route_targets",
	"target_health",
	"upstream_accounts",
	"upstream_account_snapshots",
	"upstream_account_usage_records",
	"sites",
	"inference_endpoints",
	"inference_credentials",
	"site_models",
	"published_models",
	"route_site_targets",
	"site_accounts",
	"site_account_snapshots",
	"site_account_usage_records",
	"legacy_upstream_site_mappings",
	"legacy_route_published_mappings",
}

type legacyMigrationSiteGroupManifest struct {
	Position     int     `json:"position"`
	Key          string  `json:"key"`
	Origin       string  `json:"origin"`
	Name         string  `json:"name"`
	Dashboard    string  `json:"dashboard"`
	UpstreamIDs  []int64 `json:"upstreamIds"`
	MappedSiteID int64   `json:"mappedSiteId"`
}

type legacyMigrationUpstreamGroupManifest struct {
	UpstreamID    int64  `json:"upstreamId"`
	GroupPosition int    `json:"groupPosition"`
	GroupKey      string `json:"groupKey"`
	MappedSiteID  int64  `json:"mappedSiteId"`
}

type legacyMigrationAccountUnitManifest struct {
	GroupPosition     int                              `json:"groupPosition"`
	GroupKey          string                           `json:"groupKey"`
	MappedSiteID      int64                            `json:"mappedSiteId"`
	Representative    legacyMigrationAccount           `json:"representative"`
	ExistingAccountID int64                            `json:"existingAccountId"`
	Capabilities      string                           `json:"capabilities"`
	Snapshots         []legacyMigrationAccountSnapshot `json:"snapshots"`
	Usage             []legacyMigrationAccountUsage    `json:"usage"`
}

type legacyMigrationPlanManifest struct {
	Version        string                                 `json:"version"`
	Preview        LegacyMigrationPreview                 `json:"preview"`
	Settings       Settings                               `json:"settings"`
	SiteGroups     []legacyMigrationSiteGroupManifest     `json:"siteGroups"`
	UpstreamGroups []legacyMigrationUpstreamGroupManifest `json:"upstreamGroups"`
	AccountUnits   []legacyMigrationAccountUnitManifest   `json:"accountUnits"`
	HardConflict   bool                                   `json:"hardConflict"`
}

func legacyMigrationPlanFingerprint(ctx context.Context, q legacyMigrationQueryer, state legacyMigrationPlanState) (string, error) {
	encoder := legacyMigrationFingerprintEncoder{digest: sha256.New()}
	encoder.writeFrame(legacyMigrationFingerprintVersionFrame, []byte(legacyMigrationFingerprintVersion))
	for _, table := range legacyMigrationFingerprintTables {
		rows, err := q.QueryContext(ctx, `SELECT * FROM "`+table+`" ORDER BY rowid`)
		if err != nil {
			return "", fmt.Errorf("fingerprint %s: %w", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", fmt.Errorf("fingerprint %s columns: %w", table, err)
		}
		encoder.writeFrame(legacyMigrationFingerprintTableFrame, []byte(table))
		encoder.writeUint64Frame(legacyMigrationFingerprintColumnCountFrame, uint64(len(columns)))
		for _, column := range columns {
			encoder.writeFrame(legacyMigrationFingerprintColumnFrame, []byte(column))
		}
		for rows.Next() {
			encoder.writeFrame(legacyMigrationFingerprintRowFrame, nil)
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				return "", fmt.Errorf("fingerprint %s row: %w", table, err)
			}
			for _, value := range values {
				if err := encoder.writeSQLiteValue(value); err != nil {
					rows.Close()
					return "", fmt.Errorf("fingerprint %s value: %w", table, err)
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", fmt.Errorf("fingerprint %s rows: %w", table, err)
		}
		if err := rows.Close(); err != nil {
			return "", fmt.Errorf("fingerprint %s close: %w", table, err)
		}
		encoder.writeFrame(legacyMigrationFingerprintTableEndFrame, nil)
	}
	manifest, err := json.Marshal(legacyMigrationFingerprintManifest(state))
	if err != nil {
		return "", fmt.Errorf("fingerprint plan manifest: %w", err)
	}
	encoder.writeFrame(legacyMigrationFingerprintManifestFrame, manifest)
	return "sha256:" + hex.EncodeToString(encoder.digest.Sum(nil)), nil
}

func legacyMigrationFingerprintManifest(state legacyMigrationPlanState) legacyMigrationPlanManifest {
	preview := state.preview
	preview.PlanFingerprint = ""
	groupPositions := make(map[*legacyMigrationSiteGroup]int, len(state.groups))
	groups := make([]legacyMigrationSiteGroupManifest, 0, len(state.groups))
	for position, group := range state.groups {
		groupPositions[group] = position
		upstreamIDs := make([]int64, len(group.upstreams))
		for index, upstream := range group.upstreams {
			upstreamIDs[index] = upstream.ID
		}
		groups = append(groups, legacyMigrationSiteGroupManifest{
			Position: position, Key: group.key, Origin: group.origin, Name: group.name, Dashboard: group.dashboard,
			UpstreamIDs: upstreamIDs, MappedSiteID: group.mappedSite,
		})
	}
	upstreamIDs := make([]int64, 0, len(state.groupByUpstream))
	for upstreamID := range state.groupByUpstream {
		upstreamIDs = append(upstreamIDs, upstreamID)
	}
	sort.Slice(upstreamIDs, func(i, j int) bool { return upstreamIDs[i] < upstreamIDs[j] })
	upstreamGroups := make([]legacyMigrationUpstreamGroupManifest, 0, len(upstreamIDs))
	for _, upstreamID := range upstreamIDs {
		group := state.groupByUpstream[upstreamID]
		upstreamGroups = append(upstreamGroups, legacyMigrationUpstreamGroupManifest{
			UpstreamID: upstreamID, GroupPosition: groupPositions[group], GroupKey: group.key, MappedSiteID: group.mappedSite,
		})
	}
	accountUnits := make([]legacyMigrationAccountUnitManifest, 0, len(state.accountUnits))
	for _, unit := range state.accountUnits {
		accountUnits = append(accountUnits, legacyMigrationAccountUnitManifest{
			GroupPosition: groupPositions[unit.group], GroupKey: unit.group.key, MappedSiteID: unit.group.mappedSite,
			Representative: unit.representative, ExistingAccountID: unit.existingAccountID, Capabilities: unit.capabilities,
			Snapshots: unit.snapshotsToCreate, Usage: unit.usageToCreate,
		})
	}
	return legacyMigrationPlanManifest{
		Version: legacyMigrationFingerprintVersion, Preview: preview, Settings: state.snapshot.settings,
		SiteGroups: groups, UpstreamGroups: upstreamGroups, AccountUnits: accountUnits, HardConflict: state.hardConflict,
	}
}

const (
	legacyMigrationFingerprintVersionFrame byte = iota + 1
	legacyMigrationFingerprintTableFrame
	legacyMigrationFingerprintColumnCountFrame
	legacyMigrationFingerprintColumnFrame
	legacyMigrationFingerprintRowFrame
	legacyMigrationFingerprintTableEndFrame
	legacyMigrationFingerprintManifestFrame
	legacyMigrationFingerprintSQLiteNull
	legacyMigrationFingerprintSQLiteInteger
	legacyMigrationFingerprintSQLiteReal
	legacyMigrationFingerprintSQLiteText
	legacyMigrationFingerprintSQLiteBlob
)

type legacyMigrationFingerprintEncoder struct {
	digest hash.Hash
}

func (e legacyMigrationFingerprintEncoder) writeFrame(kind byte, value []byte) {
	_, _ = e.digest.Write([]byte{kind})
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = e.digest.Write(length[:])
	_, _ = e.digest.Write(value)
}

func (e legacyMigrationFingerprintEncoder) writeUint64Frame(kind byte, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	e.writeFrame(kind, encoded[:])
}

func (e legacyMigrationFingerprintEncoder) writeSQLiteValue(value any) error {
	switch typed := value.(type) {
	case nil:
		e.writeFrame(legacyMigrationFingerprintSQLiteNull, nil)
	case int64:
		e.writeUint64Frame(legacyMigrationFingerprintSQLiteInteger, uint64(typed))
	case float64:
		e.writeUint64Frame(legacyMigrationFingerprintSQLiteReal, math.Float64bits(typed))
	case string:
		e.writeFrame(legacyMigrationFingerprintSQLiteText, []byte(typed))
	case []byte:
		e.writeFrame(legacyMigrationFingerprintSQLiteBlob, typed)
	case bool:
		var integer uint64
		if typed {
			integer = 1
		}
		e.writeUint64Frame(legacyMigrationFingerprintSQLiteInteger, integer)
	default:
		return fmt.Errorf("unsupported SQLite value type %T", value)
	}
	return nil
}

func buildLegacyMigrationPlan(ctx context.Context, q legacyMigrationQueryer) (legacyMigrationPlanState, error) {
	snapshot, err := loadLegacyMigrationSnapshot(ctx, q)
	if err != nil {
		return legacyMigrationPlanState{}, err
	}
	state := legacyMigrationPlanState{
		snapshot: snapshot,
		preview: LegacyMigrationPreview{
			Legacy: LegacyMigrationInventory{
				Upstreams: len(snapshot.upstreams), Endpoints: len(snapshot.endpoints), Credentials: len(snapshot.credentials),
				Models: len(snapshot.models), Routes: len(snapshot.routes), RouteTargets: len(snapshot.targets),
				Accounts: len(snapshot.accounts), AccountSnapshots: len(snapshot.accountSnapshots), AccountUsageRecords: len(snapshot.accountUsage),
			},
			ExistingV3: snapshot.v3, Conflicts: []LegacyMigrationConflict{}, Warnings: []string{},
		},
		groupByUpstream: make(map[int64]*legacyMigrationSiteGroup, len(snapshot.upstreams)),
	}
	endpointsByUpstream := groupLegacyEndpoints(snapshot.endpoints)
	groupsByKey := map[string]*legacyMigrationSiteGroup{}
	for _, upstream := range snapshot.upstreams {
		origin := legacyMigrationOrigin(upstream.DashboardURL, endpointsByUpstream[upstream.ID])
		key := origin
		if key == "" {
			key = fmt.Sprintf("legacy-upstream:%d", upstream.ID)
			state.preview.Warnings = append(state.preview.Warnings, fmt.Sprintf("upstream %q has no valid HTTP origin and will remain a separate site", upstream.Name))
		}
		group := groupsByKey[key]
		if group == nil {
			group = &legacyMigrationSiteGroup{key: key, origin: origin, name: upstream.Name, dashboard: strings.TrimSpace(upstream.DashboardURL)}
			groupsByKey[key] = group
			state.groups = append(state.groups, group)
		}
		group.upstreams = append(group.upstreams, upstream)
		state.groupByUpstream[upstream.ID] = group
		if mapped := snapshot.upstreamMappings[upstream.ID]; mapped > 0 {
			if group.mappedSite != 0 && group.mappedSite != mapped {
				state.addConflict("inconsistent_site_mapping", "legacy upstreams with the same origin map to different V3 sites", 1, false)
			} else {
				group.mappedSite = mapped
			}
		}
	}
	sort.Slice(state.groups, func(i, j int) bool { return state.groups[i].upstreams[0].ID < state.groups[j].upstreams[0].ID })

	pendingUpstreams := 0
	for _, upstream := range snapshot.upstreams {
		if snapshot.upstreamMappings[upstream.ID] == 0 {
			pendingUpstreams++
		}
	}
	pendingRoutes := 0
	for _, route := range snapshot.routes {
		if snapshot.routeMappings[route.ID] == 0 {
			pendingRoutes++
		}
	}

	if pendingUpstreams+pendingRoutes > 0 && snapshot.v3.UnmanagedSites > 0 {
		state.addConflict("unmanaged_v3_sites", "V3 contains sites that were not created by this migration", snapshot.v3.UnmanagedSites, true)
	}
	if pendingUpstreams+pendingRoutes > 0 && snapshot.v3.UnmanagedPublishedModels > 0 {
		state.addConflict("unmanaged_v3_published_models", "V3 contains published models that were not created by this migration", snapshot.v3.UnmanagedPublishedModels, true)
	}
	if err := planLegacyAccountMigration(ctx, q, &state); err != nil {
		return legacyMigrationPlanState{}, err
	}

	for _, group := range state.groups {
		if group.mappedSite == 0 {
			state.preview.Plan.Sites++
		}
		seenEndpoints := map[string]struct{}{}
		seenModels := map[string]struct{}{}
		for _, upstream := range group.upstreams {
			if snapshot.upstreamMappings[upstream.ID] != 0 {
				continue
			}
			if !json.Valid(upstream.Headers) {
				state.preview.Warnings = append(state.preview.Warnings, fmt.Sprintf("upstream %q has invalid custom headers; the migration will use an empty object", upstream.Name))
			}
			protocol := legacyMigrationProtocol(upstream.Kind)
			for _, endpoint := range endpointsByUpstream[upstream.ID] {
				endpointKey := strings.TrimRight(strings.TrimSpace(endpoint.BaseURL), "/") + "\x00" + protocol
				if _, ok := seenEndpoints[endpointKey]; !ok {
					seenEndpoints[endpointKey] = struct{}{}
					state.preview.Plan.Endpoints++
				}
				for _, model := range snapshot.models {
					if model.UpstreamID != upstream.ID {
						continue
					}
					modelKey := endpointKey + "\x00" + model.Name
					if _, ok := seenModels[modelKey]; !ok {
						seenModels[modelKey] = struct{}{}
						state.preview.Plan.SiteModels++
					}
				}
			}
			for _, credential := range snapshot.credentials {
				if credential.UpstreamID != upstream.ID {
					continue
				}
				if len(credential.SecretCipher) == 0 {
					state.addConflict("empty_legacy_credential", fmt.Sprintf("legacy credential %d has no encrypted API key", credential.ID), 1, false)
				}
				state.preview.Plan.Credentials++
			}
		}
	}

	existingPublishedNames, err := legacyMigrationPublishedNames(ctx, q)
	if err != nil {
		return legacyMigrationPlanState{}, err
	}
	modelsByID := make(map[int64]legacyMigrationModel, len(snapshot.models))
	endpointsByID := make(map[int64]legacyMigrationEndpoint, len(snapshot.endpoints))
	for _, model := range snapshot.models {
		modelsByID[model.ID] = model
	}
	for _, endpoint := range snapshot.endpoints {
		endpointsByID[endpoint.ID] = endpoint
	}
	targetsByRoute := groupLegacyTargets(snapshot.targets)
	for _, route := range snapshot.routes {
		if snapshot.routeMappings[route.ID] != 0 {
			continue
		}
		state.preview.Plan.PublishedModels++
		if _, exists := existingPublishedNames[strings.ToLower(strings.TrimSpace(route.PublicModel))]; exists {
			state.addConflict("public_model_collision", fmt.Sprintf("published model %q already exists outside the migration", route.PublicModel), 1, false)
		}
		seenSites := map[string]struct{}{}
		for _, target := range targetsByRoute[route.ID] {
			model, modelOK := modelsByID[target.ModelID]
			endpoint, endpointOK := endpointsByID[target.EndpointID]
			if !modelOK || !endpointOK || model.UpstreamID != endpoint.UpstreamID {
				state.addConflict("invalid_legacy_target", fmt.Sprintf("route %q contains a target whose model and endpoint do not belong to the same upstream", route.PublicModel), 1, false)
				continue
			}
			group := state.groupByUpstream[model.UpstreamID]
			if group == nil {
				state.addConflict("invalid_legacy_target", fmt.Sprintf("route %q references a missing upstream", route.PublicModel), 1, false)
				continue
			}
			if _, exists := seenSites[group.key]; exists {
				continue
			}
			protocol := legacyMigrationProtocol(findLegacyUpstream(snapshot.upstreams, model.UpstreamID).Kind)
			if !inferenceprotocol.For(protocol).RouteEligible {
				state.preview.Plan.SkippedRouteTargets++
				state.preview.Warnings = append(state.preview.Warnings, fmt.Sprintf("route %q target %d uses native %s; its Endpoint and models will migrate, but this discovery-only target will be skipped", route.PublicModel, target.ID, protocol))
				continue
			}
			seenSites[group.key] = struct{}{}
			state.preview.Plan.RouteTargets++
		}
	}

	state.preview.AlreadyApplied = !state.hardConflict && len(snapshot.upstreams) == snapshot.v3.MappedUpstreams &&
		len(snapshot.routes) == snapshot.v3.MappedRoutes && state.preview.Plan.empty()
	state.preview.CanApply = !state.hardConflict && !state.preview.RequiresForce
	return state, nil
}

func (s *legacyMigrationPlanState) addConflict(code, message string, count int, overrideable bool) {
	s.preview.Conflicts = append(s.preview.Conflicts, LegacyMigrationConflict{Code: code, Message: message, Count: count, Overrideable: overrideable})
	if overrideable {
		s.preview.RequiresForce = true
	} else {
		s.hardConflict = true
	}
}

func planLegacyAccountMigration(ctx context.Context, q legacyMigrationQueryer, state *legacyMigrationPlanState) error {
	accountsByUpstream := make(map[int64][]legacyMigrationAccount)
	snapshotsByAccount := make(map[int64][]legacyMigrationAccountSnapshot)
	usageByAccount := make(map[int64][]legacyMigrationAccountUsage)
	for _, item := range state.snapshot.accounts {
		accountsByUpstream[item.UpstreamID] = append(accountsByUpstream[item.UpstreamID], item)
	}
	for _, item := range state.snapshot.accountSnapshots {
		snapshotsByAccount[item.AccountID] = append(snapshotsByAccount[item.AccountID], item)
	}
	for _, item := range state.snapshot.accountUsage {
		usageByAccount[item.AccountID] = append(usageByAccount[item.AccountID], item)
	}

	for _, group := range state.groups {
		accounts := make([]legacyMigrationAccount, 0, len(group.upstreams))
		for _, upstream := range group.upstreams {
			accounts = append(accounts, accountsByUpstream[upstream.ID]...)
		}
		if len(accounts) == 0 {
			continue
		}
		sort.Slice(accounts, func(i, j int) bool {
			if accounts[i].UpdatedAt == accounts[j].UpdatedAt {
				return accounts[i].ID > accounts[j].ID
			}
			return accounts[i].UpdatedAt > accounts[j].UpdatedAt
		})
		representative := accounts[0]
		capabilities, err := canonicalLegacyMigrationJSON(representative.Capabilities)
		if err != nil {
			state.addConflict("invalid_account_capabilities", fmt.Sprintf("site group %q contains invalid account capabilities", group.name), 1, false)
			continue
		}
		conflicted := false
		if len(representative.AuthCipher) == 0 {
			state.addConflict("empty_account_auth", fmt.Sprintf("site group %q contains an account without encrypted authentication", group.name), 1, false)
			conflicted = true
		}
		for _, account := range accounts[1:] {
			code, message, err := compareLegacyAccountConfiguration(representative, account)
			if err != nil {
				return err
			}
			if code != "" {
				state.addConflict(code, fmt.Sprintf("site group %q: %s", group.name, message), 1, false)
				conflicted = true
				break
			}
		}
		if conflicted {
			continue
		}

		unit := legacyMigrationAccountUnit{group: group, representative: representative, capabilities: capabilities}
		if group.mappedSite > 0 {
			existing, found, err := loadExistingSiteAccount(ctx, q, group.mappedSite)
			if err != nil {
				return err
			}
			if found {
				code, message, err := compareLegacyAccountConfiguration(representative, existing)
				if err != nil {
					return err
				}
				if code != "" {
					state.addConflict("site_account_collision", fmt.Sprintf("site group %q already has a different V3 Site Account: %s", group.name, message), 1, false)
					continue
				}
				unit.existingAccountID = existing.ID
			}
		}

		snapshotKeys := map[string]struct{}{}
		for _, account := range accounts {
			for _, item := range snapshotsByAccount[account.ID] {
				canonical, err := canonicalLegacyMigrationJSON(item.Snapshot)
				if err != nil {
					state.addConflict("invalid_account_snapshot", fmt.Sprintf("site group %q contains an invalid account snapshot", group.name), 1, false)
					conflicted = true
					break
				}
				item.Snapshot = []byte(canonical)
				key := fmt.Sprintf("%d\x00%s", item.CapturedAt, canonical)
				if _, exists := snapshotKeys[key]; exists {
					continue
				}
				snapshotKeys[key] = struct{}{}
				unit.snapshotsToCreate = append(unit.snapshotsToCreate, item)
			}
			if conflicted {
				break
			}
		}
		if conflicted {
			continue
		}

		usageByKey := map[string]legacyMigrationAccountUsage{}
		for _, account := range accounts {
			for _, item := range usageByAccount[account.ID] {
				canonical, err := canonicalLegacyMigrationJSON(item.Raw)
				if err != nil {
					state.addConflict("invalid_account_usage", fmt.Sprintf("site group %q contains invalid account usage JSON", group.name), 1, false)
					conflicted = true
					break
				}
				item.Raw = []byte(canonical)
				if existing, exists := usageByKey[item.DedupeKey]; exists {
					if !legacyAccountUsageEqual(existing, item) {
						state.addConflict("account_usage_conflict", fmt.Sprintf("site group %q contains different usage rows with dedupe key %q", group.name, item.DedupeKey), 1, false)
						conflicted = true
						break
					}
					continue
				}
				usageByKey[item.DedupeKey] = item
			}
			if conflicted {
				break
			}
		}
		if conflicted {
			continue
		}
		for _, item := range usageByKey {
			unit.usageToCreate = append(unit.usageToCreate, item)
		}
		sort.Slice(unit.snapshotsToCreate, func(i, j int) bool {
			if unit.snapshotsToCreate[i].CapturedAt == unit.snapshotsToCreate[j].CapturedAt {
				return unit.snapshotsToCreate[i].ID < unit.snapshotsToCreate[j].ID
			}
			return unit.snapshotsToCreate[i].CapturedAt < unit.snapshotsToCreate[j].CapturedAt
		})
		sort.Slice(unit.usageToCreate, func(i, j int) bool { return unit.usageToCreate[i].DedupeKey < unit.usageToCreate[j].DedupeKey })

		if unit.existingAccountID > 0 {
			existingSnapshotKeys, err := loadExistingSiteAccountSnapshotKeys(ctx, q, unit.existingAccountID)
			if err != nil {
				return err
			}
			filtered := unit.snapshotsToCreate[:0]
			for _, item := range unit.snapshotsToCreate {
				key := fmt.Sprintf("%d\x00%s", item.CapturedAt, item.Snapshot)
				if _, exists := existingSnapshotKeys[key]; !exists {
					filtered = append(filtered, item)
				}
			}
			unit.snapshotsToCreate = filtered

			existingUsage, err := loadExistingSiteAccountUsage(ctx, q, unit.existingAccountID)
			if err != nil {
				return err
			}
			filteredUsage := unit.usageToCreate[:0]
			for _, item := range unit.usageToCreate {
				if existing, exists := existingUsage[item.DedupeKey]; exists {
					if !legacyAccountUsageEqual(existing, item) {
						state.addConflict("site_account_usage_collision", fmt.Sprintf("site group %q already has different V3 usage for dedupe key %q", group.name, item.DedupeKey), 1, false)
						conflicted = true
						break
					}
					continue
				}
				filteredUsage = append(filteredUsage, item)
			}
			unit.usageToCreate = filteredUsage
		}
		if conflicted {
			continue
		}
		if unit.existingAccountID == 0 {
			state.preview.Plan.Accounts++
		}
		state.preview.Plan.AccountSnapshots += len(unit.snapshotsToCreate)
		state.preview.Plan.AccountUsageRecords += len(unit.usageToCreate)
		state.accountUnits = append(state.accountUnits, unit)
	}
	return nil
}

func compareLegacyAccountConfiguration(left, right legacyMigrationAccount) (string, string, error) {
	if strings.ToLower(strings.TrimSpace(left.AdapterKind)) != strings.ToLower(strings.TrimSpace(right.AdapterKind)) {
		return "account_adapter_conflict", "legacy account adapters differ", nil
	}
	if strings.TrimRight(strings.TrimSpace(left.APIOrigin), "/") != strings.TrimRight(strings.TrimSpace(right.APIOrigin), "/") {
		return "account_origin_conflict", "legacy account API origins differ", nil
	}
	if !bytes.Equal(left.AuthCipher, right.AuthCipher) {
		return "account_auth_conflict", "encrypted authentication payloads differ, so auth kind and credential equality cannot be proven safely", nil
	}
	if left.Enabled != right.Enabled {
		return "account_configuration_conflict", "legacy account enabled states differ", nil
	}
	leftCapabilities, err := canonicalLegacyMigrationJSON(left.Capabilities)
	if err != nil {
		return "", "", err
	}
	rightCapabilities, err := canonicalLegacyMigrationJSON(right.Capabilities)
	if err != nil {
		return "", "", err
	}
	if leftCapabilities != rightCapabilities {
		return "account_configuration_conflict", "legacy account capabilities differ", nil
	}
	return "", "", nil
}

func canonicalLegacyMigrationJSON(value []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(decoded)
	return string(encoded), err
}

func legacyAccountUsageEqual(left, right legacyMigrationAccountUsage) bool {
	return left.DedupeKey == right.DedupeKey && left.ExternalID == right.ExternalID && left.ModelName == right.ModelName &&
		left.Amount == right.Amount && left.Unit == right.Unit && bytes.Equal(left.Raw, right.Raw) &&
		equalOptionalInt64(left.OccurredAt, right.OccurredAt) && left.SyncedAt == right.SyncedAt
}

func equalOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func loadExistingSiteAccount(ctx context.Context, q legacyMigrationQueryer, siteID int64) (legacyMigrationAccount, bool, error) {
	var item legacyMigrationAccount
	var enabled int
	var lastAttempt, lastSuccess sql.NullInt64
	var errorCode, errorMessage sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,site_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,
last_attempt_at,last_success_at,last_error_code,last_error_message,created_at,updated_at FROM site_accounts WHERE site_id=?`, siteID).Scan(
		&item.ID, &item.UpstreamID, &item.AdapterKind, &item.APIOrigin, &item.AuthCipher, &enabled, &item.Capabilities,
		&item.SyncState, &lastAttempt, &lastSuccess, &errorCode, &errorMessage, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return legacyMigrationAccount{}, false, nil
	}
	if err != nil {
		return legacyMigrationAccount{}, false, err
	}
	item.Enabled = enabled == 1
	item.LastAttemptAt, item.LastSuccessAt = int64Ptr(lastAttempt), int64Ptr(lastSuccess)
	item.LastErrorCode, item.LastErrorMessage = errorCode.String, errorMessage.String
	return item, true, nil
}

func loadExistingSiteAccountSnapshotKeys(ctx context.Context, q legacyMigrationQueryer, accountID int64) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, `SELECT snapshot_json,captured_at FROM site_account_snapshots WHERE site_account_id=?`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var raw []byte
		var capturedAt int64
		if err := rows.Scan(&raw, &capturedAt); err != nil {
			return nil, err
		}
		canonical, err := canonicalLegacyMigrationJSON(raw)
		if err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%d\x00%s", capturedAt, canonical)] = struct{}{}
	}
	return out, rows.Err()
}

func loadExistingSiteAccountUsage(ctx context.Context, q legacyMigrationQueryer, accountID int64) (map[string]legacyMigrationAccountUsage, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,site_account_id,dedupe_key,external_id,model_name,amount_text,unit,raw_json,occurred_at,synced_at
FROM site_account_usage_records WHERE site_account_id=?`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]legacyMigrationAccountUsage{}
	for rows.Next() {
		var item legacyMigrationAccountUsage
		var externalID, modelName, amount, unit sql.NullString
		var occurredAt sql.NullInt64
		if err := rows.Scan(&item.ID, &item.AccountID, &item.DedupeKey, &externalID, &modelName, &amount, &unit, &item.Raw, &occurredAt, &item.SyncedAt); err != nil {
			return nil, err
		}
		item.ExternalID, item.ModelName, item.Amount, item.Unit = externalID.String, modelName.String, amount.String, unit.String
		item.OccurredAt = int64Ptr(occurredAt)
		canonical, err := canonicalLegacyMigrationJSON(item.Raw)
		if err != nil {
			return nil, err
		}
		item.Raw = []byte(canonical)
		out[item.DedupeKey] = item
	}
	return out, rows.Err()
}

func loadLegacyMigrationSnapshot(ctx context.Context, q legacyMigrationQueryer) (legacyMigrationSnapshot, error) {
	var out legacyMigrationSnapshot
	out.upstreamMappings = map[int64]int64{}
	out.routeMappings = map[int64]int64{}
	rows, err := q.QueryContext(ctx, `SELECT id,name,kind,COALESCE(dashboard_url,''),enabled,custom_headers_json FROM upstreams ORDER BY id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item legacyMigrationUpstream
		var enabled int
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.DashboardURL, &enabled, &item.Headers); err != nil {
			rows.Close()
			return out, err
		}
		item.Enabled = enabled == 1
		out.upstreams = append(out.upstreams, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id,upstream_id,name,base_url,position,enabled FROM upstream_endpoints ORDER BY upstream_id,position,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item legacyMigrationEndpoint
		var enabled int
		if err := rows.Scan(&item.ID, &item.UpstreamID, &item.Name, &item.BaseURL, &item.Position, &enabled); err != nil {
			rows.Close()
			return out, err
		}
		item.Enabled = enabled == 1
		out.endpoints = append(out.endpoints, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id,upstream_id,name,secret_cipher,enabled,runtime_state FROM upstream_credentials ORDER BY upstream_id,id`)
	if err != nil {
		return out, err
	}
	positionByUpstream := map[int64]int{}
	for rows.Next() {
		var item legacyMigrationCredential
		var enabled int
		if err := rows.Scan(&item.ID, &item.UpstreamID, &item.Name, &item.SecretCipher, &enabled, &item.RuntimeState); err != nil {
			rows.Close()
			return out, err
		}
		item.Enabled = enabled == 1
		item.Position = positionByUpstream[item.UpstreamID]
		positionByUpstream[item.UpstreamID]++
		out.credentials = append(out.credentials, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id,upstream_id,model_name,enabled,stale,missing_count,last_seen_at FROM upstream_models ORDER BY upstream_id,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item legacyMigrationModel
		var enabled, stale int
		var lastSeen sql.NullInt64
		if err := rows.Scan(&item.ID, &item.UpstreamID, &item.Name, &enabled, &stale, &item.MissingCount, &lastSeen); err != nil {
			rows.Close()
			return out, err
		}
		item.Enabled, item.Stale, item.LastSeenAt = enabled == 1, stale == 1, int64Ptr(lastSeen)
		out.models = append(out.models, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id,public_model,COALESCE(display_name,''),enabled,monitor_enabled,monitor_interval_seconds,cooldown_seconds,failure_threshold,failure_window_seconds FROM routes ORDER BY id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item legacyMigrationRoute
		var enabled, monitor int
		if err := rows.Scan(&item.ID, &item.PublicModel, &item.DisplayName, &enabled, &monitor, &item.MonitorIntervalSeconds, &item.CooldownSeconds, &item.FailureThreshold, &item.FailureWindowSeconds); err != nil {
			rows.Close()
			return out, err
		}
		item.Enabled, item.MonitorEnabled = enabled == 1, monitor == 1
		out.routes = append(out.routes, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id,route_id,upstream_model_id,endpoint_id,credential_id,COALESCE(upstream_model_override,''),position,enabled FROM route_targets ORDER BY route_id,position,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item legacyMigrationTarget
		var enabled int
		if err := rows.Scan(&item.ID, &item.RouteID, &item.ModelID, &item.EndpointID, &item.CredentialID, &item.ModelOverride, &item.Position, &enabled); err != nil {
			rows.Close()
			return out, err
		}
		item.Enabled = enabled == 1
		out.targets = append(out.targets, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id,upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,
last_attempt_at,last_success_at,last_error_code,last_error_message,created_at,updated_at FROM upstream_accounts ORDER BY upstream_id,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item legacyMigrationAccount
		var enabled int
		var lastAttempt, lastSuccess sql.NullInt64
		var errorCode, errorMessage sql.NullString
		if err := rows.Scan(&item.ID, &item.UpstreamID, &item.AdapterKind, &item.APIOrigin, &item.AuthCipher, &enabled, &item.Capabilities,
			&item.SyncState, &lastAttempt, &lastSuccess, &errorCode, &errorMessage, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return out, err
		}
		item.Enabled = enabled == 1
		item.LastAttemptAt, item.LastSuccessAt = int64Ptr(lastAttempt), int64Ptr(lastSuccess)
		item.LastErrorCode, item.LastErrorMessage = errorCode.String, errorMessage.String
		out.accounts = append(out.accounts, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id,upstream_account_id,snapshot_json,captured_at FROM upstream_account_snapshots ORDER BY upstream_account_id,captured_at,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item legacyMigrationAccountSnapshot
		if err := rows.Scan(&item.ID, &item.AccountID, &item.Snapshot, &item.CapturedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.accountSnapshots = append(out.accountSnapshots, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id,upstream_account_id,dedupe_key,external_id,model_name,amount_text,unit,raw_json,occurred_at,synced_at
FROM upstream_account_usage_records ORDER BY upstream_account_id,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item legacyMigrationAccountUsage
		var externalID, modelName, amount, unit sql.NullString
		var occurredAt sql.NullInt64
		if err := rows.Scan(&item.ID, &item.AccountID, &item.DedupeKey, &externalID, &modelName, &amount, &unit, &item.Raw, &occurredAt, &item.SyncedAt); err != nil {
			rows.Close()
			return out, err
		}
		item.ExternalID, item.ModelName, item.Amount, item.Unit = externalID.String, modelName.String, amount.String, unit.String
		item.OccurredAt = int64Ptr(occurredAt)
		out.accountUsage = append(out.accountUsage, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, `SELECT upstream_id,site_id FROM legacy_upstream_site_mappings`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var oldID, newID int64
		if err := rows.Scan(&oldID, &newID); err != nil {
			rows.Close()
			return out, err
		}
		out.upstreamMappings[oldID] = newID
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, `SELECT route_id,published_model_id FROM legacy_route_published_mappings`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var oldID, newID int64
		if err := rows.Scan(&oldID, &newID); err != nil {
			rows.Close()
			return out, err
		}
		out.routeMappings[oldID] = newID
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	counts := []struct {
		query  string
		target *int
	}{
		{`SELECT COUNT(*) FROM sites`, &out.v3.Sites}, {`SELECT COUNT(*) FROM inference_endpoints`, &out.v3.Endpoints},
		{`SELECT COUNT(*) FROM inference_credentials`, &out.v3.Credentials}, {`SELECT COUNT(*) FROM site_models`, &out.v3.SiteModels},
		{`SELECT COUNT(*) FROM published_models`, &out.v3.PublishedModels}, {`SELECT COUNT(*) FROM route_site_targets`, &out.v3.RouteTargets},
		{`SELECT COUNT(*) FROM legacy_upstream_site_mappings`, &out.v3.MappedUpstreams}, {`SELECT COUNT(*) FROM legacy_route_published_mappings`, &out.v3.MappedRoutes},
		{`SELECT COUNT(*) FROM sites s WHERE NOT EXISTS (SELECT 1 FROM legacy_upstream_site_mappings m WHERE m.site_id=s.id)`, &out.v3.UnmanagedSites},
		{`SELECT COUNT(*) FROM published_models p WHERE NOT EXISTS (SELECT 1 FROM legacy_route_published_mappings m WHERE m.published_model_id=p.id)`, &out.v3.UnmanagedPublishedModels},
		{`SELECT COUNT(*) FROM site_accounts`, &out.v3.Accounts},
		{`SELECT COUNT(*) FROM site_account_snapshots`, &out.v3.AccountSnapshots},
		{`SELECT COUNT(*) FROM site_account_usage_records`, &out.v3.AccountUsageRecords},
	}
	for _, count := range counts {
		if err := q.QueryRowContext(ctx, count.query).Scan(count.target); err != nil {
			return out, err
		}
	}
	if err := q.QueryRowContext(ctx, `SELECT default_cooldown_seconds,failure_threshold,failure_window_seconds,probe_interval_seconds,
first_output_timeout_seconds,stream_idle_timeout_seconds,request_deadline_seconds,max_attempts,log_retention_days,updated_at FROM app_settings WHERE id=1`).Scan(
		&out.settings.DefaultCooldownSeconds, &out.settings.FailureThreshold, &out.settings.FailureWindowSeconds,
		&out.settings.ProbeIntervalSeconds, &out.settings.FirstOutputTimeoutSeconds, &out.settings.StreamIdleTimeoutSeconds,
		&out.settings.RequestDeadlineSeconds, &out.settings.MaxAttempts, &out.settings.LogRetentionDays, &out.settings.UpdatedAt); err != nil {
		return out, err
	}
	return out, nil
}

func applyLegacyMigrationPlan(ctx context.Context, tx *sql.Tx, state legacyMigrationPlanState, now int64) error {
	snapshot := state.snapshot
	endpointsByUpstream := groupLegacyEndpoints(snapshot.endpoints)
	modelsByUpstream := map[int64][]legacyMigrationModel{}
	credentialsByUpstream := map[int64][]legacyMigrationCredential{}
	modelsByID := map[int64]legacyMigrationModel{}
	endpointsByID := map[int64]legacyMigrationEndpoint{}
	for _, item := range snapshot.models {
		modelsByUpstream[item.UpstreamID] = append(modelsByUpstream[item.UpstreamID], item)
		modelsByID[item.ID] = item
	}
	for _, item := range snapshot.credentials {
		credentialsByUpstream[item.UpstreamID] = append(credentialsByUpstream[item.UpstreamID], item)
	}
	for _, item := range snapshot.endpoints {
		endpointsByID[item.ID] = item
	}

	siteNames, err := legacyMigrationSiteNames(ctx, tx)
	if err != nil {
		return err
	}
	siteByUpstream := map[int64]int64{}
	endpointIDs := map[int64]int64{}
	credentialIDs := map[int64]int64{}
	modelIDs := map[string]int64{}
	for _, group := range state.groups {
		siteID := group.mappedSite
		if siteID == 0 {
			name := uniqueLegacyMigrationName(group.name, group.upstreams[0].ID, siteNames)
			dashboard := group.dashboard
			if dashboard == "" {
				dashboard = group.origin
			}
			enabled := false
			for _, upstream := range group.upstreams {
				enabled = enabled || upstream.Enabled
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO sites(name,dashboard_url,enabled,revision,created_at,updated_at) VALUES (?,?,?,1,?,?)`, name, nullableString(dashboard), boolInt(enabled), now, now)
			if err != nil {
				return err
			}
			siteID, err = result.LastInsertId()
			if err != nil {
				return err
			}
		}
		usedCredentialNames, err := legacyMigrationCredentialNames(ctx, tx, siteID)
		if err != nil {
			return err
		}
		for _, upstream := range group.upstreams {
			siteByUpstream[upstream.ID] = siteID
			mapped := snapshot.upstreamMappings[upstream.ID] != 0
			protocol := legacyMigrationProtocol(upstream.Kind)
			headers := normalizedJSON(upstream.Headers)
			for _, endpoint := range endpointsByUpstream[upstream.ID] {
				baseURL := strings.TrimRight(strings.TrimSpace(endpoint.BaseURL), "/")
				var endpointID int64
				err := tx.QueryRowContext(ctx, `SELECT id FROM inference_endpoints WHERE site_id=? AND base_url=? AND wire_protocol=?`, siteID, baseURL, protocol).Scan(&endpointID)
				if errors.Is(err, sql.ErrNoRows) && !mapped {
					result, insertErr := tx.ExecContext(ctx, `INSERT INTO inference_endpoints(site_id,name,base_url,wire_protocol,compatibility_profile,auth_scheme,custom_headers_json,position,enabled,revision,created_at,updated_at)
SELECT ?,?,?,?,?,?,?,COALESCE(MAX(position)+1,0),?,1,?,? FROM inference_endpoints WHERE site_id=?`, siteID, firstLegacyName(endpoint.Name, "Primary"), baseURL, protocol,
						legacyMigrationCompatibilityProfile(upstream.Kind), legacyMigrationAuthScheme(protocol), headers, boolInt(upstream.Enabled && endpoint.Enabled), now, now, siteID)
					if insertErr != nil {
						return insertErr
					}
					endpointID, err = result.LastInsertId()
				}
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if endpointID == 0 {
					return fmt.Errorf("mapped upstream %d is missing endpoint %d", upstream.ID, endpoint.ID)
				}
				endpointIDs[endpoint.ID] = endpointID
				if !mapped {
					for _, model := range modelsByUpstream[upstream.ID] {
						id, err := insertLegacySiteModel(ctx, tx, siteID, endpointID, model.Name, upstream.Enabled && model.Enabled, model.Stale, model.MissingCount, model.LastSeenAt, now)
						if err != nil {
							return err
						}
						modelIDs[legacyMigrationModelKey(endpoint.ID, model.Name)] = id
					}
				}
			}
			if !mapped {
				for _, credential := range credentialsByUpstream[upstream.ID] {
					name := uniqueLegacyMigrationCredentialName(credential.Name, upstream.Name, credential.ID, usedCredentialNames)
					result, err := tx.ExecContext(ctx, `INSERT INTO inference_credentials(site_id,name,secret_cipher,position,enabled,runtime_state,revision,created_at,updated_at)
SELECT ?,?,?,COALESCE(MAX(position)+1,0),?,?,1,?,? FROM inference_credentials WHERE site_id=?`, siteID, name, credential.SecretCipher,
						boolInt(upstream.Enabled && credential.Enabled), legacyMigrationCredentialState(credential.RuntimeState), now, now, siteID)
					if err != nil {
						return err
					}
					credentialIDs[credential.ID], err = result.LastInsertId()
					if err != nil {
						return err
					}
				}
			}
			primaryEndpoint := int64(0)
			if len(endpointsByUpstream[upstream.ID]) > 0 {
				primaryEndpoint = endpointIDs[endpointsByUpstream[upstream.ID][0].ID]
			}
			primaryCredential := int64(0)
			if len(credentialsByUpstream[upstream.ID]) > 0 {
				primaryCredential = credentialIDs[credentialsByUpstream[upstream.ID][0].ID]
				if mapped {
					_ = tx.QueryRowContext(ctx, `SELECT credential_id FROM legacy_upstream_site_mappings WHERE upstream_id=?`, upstream.ID).Scan(&primaryCredential)
				}
			}
			if !mapped {
				_, err := tx.ExecContext(ctx, `INSERT INTO legacy_upstream_site_mappings(upstream_id,site_id,endpoint_id,credential_id,migrated_at) VALUES (?,?,?,?,?)`,
					upstream.ID, siteID, nullablePositiveInt64(primaryEndpoint), nullablePositiveInt64(primaryCredential), now)
				if err != nil {
					return err
				}
			}
		}
	}
	if err := applyLegacyMigrationAccounts(ctx, tx, state.accountUnits, siteByUpstream, now); err != nil {
		return err
	}

	targetsByRoute := groupLegacyTargets(snapshot.targets)
	for _, route := range snapshot.routes {
		if snapshot.routeMappings[route.ID] != 0 {
			continue
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO published_models(public_name,display_name,official_price_sku,enabled,monitor_enabled,monitor_interval_seconds,cooldown_seconds,
failure_threshold,failure_window_seconds,first_output_timeout_seconds,stream_idle_timeout_seconds,request_deadline_seconds,max_attempts,revision,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, strings.TrimSpace(route.PublicModel), nullableString(route.DisplayName), nullableString(strings.TrimSpace(route.PublicModel)),
			boolInt(route.Enabled), boolInt(route.MonitorEnabled), clamp(route.MonitorIntervalSeconds, 30, 86400), clamp(route.CooldownSeconds, 1, 86400),
			clamp(route.FailureThreshold, 2, 10), clamp(route.FailureWindowSeconds, 1, 86400), clamp(snapshot.settings.FirstOutputTimeoutSeconds, 1, 600),
			clamp(snapshot.settings.StreamIdleTimeoutSeconds, 1, 3600), clamp(snapshot.settings.RequestDeadlineSeconds, 1, 3600), clamp(snapshot.settings.MaxAttempts, 1, 20), now, now)
		if err != nil {
			return err
		}
		publishedID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		seenSites := map[int64]struct{}{}
		position := 0
		for _, target := range targetsByRoute[route.ID] {
			model := modelsByID[target.ModelID]
			endpoint := endpointsByID[target.EndpointID]
			siteID := siteByUpstream[model.UpstreamID]
			protocol := legacyMigrationProtocol(findLegacyUpstream(snapshot.upstreams, model.UpstreamID).Kind)
			if !inferenceprotocol.For(protocol).RouteEligible {
				continue
			}
			if _, exists := seenSites[siteID]; exists {
				continue
			}
			seenSites[siteID] = struct{}{}
			endpointID := endpointIDs[target.EndpointID]
			if endpointID == 0 {
				if err := tx.QueryRowContext(ctx, `SELECT id FROM inference_endpoints WHERE site_id=? AND base_url=? AND wire_protocol=?`, siteID,
					strings.TrimRight(strings.TrimSpace(endpoint.BaseURL), "/"), protocol).Scan(&endpointID); err != nil {
					return err
				}
			}
			sourceModel := strings.TrimSpace(target.ModelOverride)
			if sourceModel == "" {
				sourceModel = model.Name
			}
			siteModelID := modelIDs[legacyMigrationModelKey(target.EndpointID, sourceModel)]
			if siteModelID == 0 {
				siteModelID, err = insertLegacySiteModel(ctx, tx, siteID, endpointID, sourceModel, model.Enabled, model.Stale, model.MissingCount, model.LastSeenAt, now)
				if err != nil {
					return err
				}
			}
			targetResult, err := tx.ExecContext(ctx, `INSERT INTO route_site_targets(published_model_id,site_id,endpoint_id,site_model_id,position,enabled,revision,created_at,updated_at) VALUES (?,?,?,?,?,?,1,?,?)`,
				publishedID, siteID, endpointID, siteModelID, position, boolInt(target.Enabled), now, now)
			if err != nil {
				return err
			}
			newTargetID, err := targetResult.LastInsertId()
			if err != nil {
				return err
			}
			if err := copyLegacyTargetHealth(ctx, tx, target.ID, newTargetID, now); err != nil {
				return err
			}
			position++
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_route_published_mappings(route_id,published_model_id,migrated_at) VALUES (?,?,?)`, route.ID, publishedID, now); err != nil {
			return err
		}
	}
	return nil
}

func applyLegacyMigrationAccounts(ctx context.Context, tx *sql.Tx, units []legacyMigrationAccountUnit, siteByUpstream map[int64]int64, now int64) error {
	for _, unit := range units {
		siteID := unit.group.mappedSite
		if siteID == 0 && len(unit.group.upstreams) > 0 {
			siteID = siteByUpstream[unit.group.upstreams[0].ID]
		}
		if siteID <= 0 {
			return fmt.Errorf("cannot resolve V3 site for legacy account group %q", unit.group.name)
		}
		accountID := unit.existingAccountID
		if accountID == 0 {
			representative := unit.representative
			createdAt := representative.CreatedAt
			if createdAt <= 0 {
				createdAt = now
			}
			updatedAt := representative.UpdatedAt
			if updatedAt <= 0 {
				updatedAt = createdAt
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO site_accounts(site_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,
sync_state,last_attempt_at,last_success_at,last_error_code,last_error_message,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, siteID, strings.ToLower(strings.TrimSpace(representative.AdapterKind)),
				strings.TrimRight(strings.TrimSpace(representative.APIOrigin), "/"), representative.AuthCipher, boolInt(representative.Enabled), unit.capabilities,
				firstLegacyName(representative.SyncState, "pending"), nullableInt64Pointer(representative.LastAttemptAt), nullableInt64Pointer(representative.LastSuccessAt),
				nullableString(representative.LastErrorCode), nullableString(representative.LastErrorMessage), createdAt, updatedAt)
			if err != nil {
				return err
			}
			accountID, err = result.LastInsertId()
			if err != nil {
				return err
			}
		}
		for _, item := range unit.snapshotsToCreate {
			if _, err := tx.ExecContext(ctx, `INSERT INTO site_account_snapshots(site_account_id,snapshot_json,captured_at) VALUES (?,?,?)`, accountID, item.Snapshot, item.CapturedAt); err != nil {
				return err
			}
		}
		for _, item := range unit.usageToCreate {
			if _, err := tx.ExecContext(ctx, `INSERT INTO site_account_usage_records(site_account_id,dedupe_key,external_id,model_name,amount_text,unit,raw_json,occurred_at,synced_at)
VALUES (?,?,?,?,?,?,?,?,?)`, accountID, item.DedupeKey, nullableString(item.ExternalID), nullableString(item.ModelName), nullableString(item.Amount),
				nullableString(item.Unit), item.Raw, nullableInt64Pointer(item.OccurredAt), item.SyncedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertLegacySiteModel(ctx context.Context, tx *sql.Tx, siteID, endpointID int64, name string, enabled, stale bool, missing int, lastSeen *int64, now int64) (int64, error) {
	name = strings.TrimSpace(name)
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM site_models WHERE endpoint_id=? AND model_name=?`, endpointID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO site_models(site_id,endpoint_id,model_name,display_name,enabled,stale,missing_count,last_seen_at,revision,created_at,updated_at)
VALUES (?,?,?,NULL,?,?,?,?,1,?,?)`, siteID, endpointID, name, boolInt(enabled), boolInt(stale), maxInt(missing, 0), lastSeen, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func copyLegacyTargetHealth(ctx context.Context, tx *sql.Tx, oldTargetID, newTargetID, now int64) error {
	var phase, capability string
	var consecutive int
	var lastFailure, lastSuccess, cooldown, lease sql.NullInt64
	var errorClass, errorMessage, incident sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT circuit_phase,consecutive_failures,last_failure_at,last_success_at,cooldown_until,half_open_lease_until,
capability_state,last_error_class,last_error_message,last_incident_id FROM target_health WHERE target_id=?`, oldTargetID).Scan(
		&phase, &consecutive, &lastFailure, &lastSuccess, &cooldown, &lease, &capability, &errorClass, &errorMessage, &incident)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if phase != "closed" && phase != "open" && phase != "half_open" {
		phase = "closed"
	}
	if capability != "unknown" && capability != "supported" && capability != "unsupported" {
		capability = "unknown"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO route_site_target_health(target_id,circuit_phase,consecutive_failures,last_failure_at,last_success_at,cooldown_until,half_open_lease_until,
capability_state,last_error_class,last_error_message,last_incident_id,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, newTargetID, phase, maxInt(consecutive, 0),
		nullableNullInt64(lastFailure), nullableNullInt64(lastSuccess), nullableNullInt64(cooldown), nullableNullInt64(lease), capability,
		nullableNullString(errorClass), nullableNullString(errorMessage), nullableNullString(incident), now)
	return err
}

func legacyMigrationOrigin(dashboard string, endpoints []legacyMigrationEndpoint) string {
	if origin := canonicalLegacyOrigin(dashboard); origin != "" {
		return origin
	}
	for _, endpoint := range endpoints {
		if origin := canonicalLegacyOrigin(endpoint.BaseURL); origin != "" {
			return origin
		}
	}
	return ""
}

func canonicalLegacyOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" && !((scheme == "http" && port == "80") || (scheme == "https" && port == "443")) {
		host += ":" + port
	}
	return scheme + "://" + host
}

func legacyMigrationProtocol(kind string) string {
	normalized, err := NormalizeKind(kind)
	if err != nil {
		return "compatible"
	}
	return normalized
}

func legacyMigrationCompatibilityProfile(kind string) string {
	protocol := legacyMigrationProtocol(kind)
	if protocol == "compatible" {
		return "generic"
	}
	return protocol
}

func legacyMigrationAuthScheme(protocol string) string {
	return inferenceprotocol.DefaultAuthScheme(protocol)
}

func legacyMigrationCredentialState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active", "healthy":
		return "active"
	case "invalid":
		return "invalid"
	case "exhausted":
		return "exhausted"
	case "rate_limited":
		return "rate_limited"
	default:
		return "active"
	}
}

func groupLegacyEndpoints(items []legacyMigrationEndpoint) map[int64][]legacyMigrationEndpoint {
	out := map[int64][]legacyMigrationEndpoint{}
	for _, item := range items {
		out[item.UpstreamID] = append(out[item.UpstreamID], item)
	}
	return out
}

func groupLegacyTargets(items []legacyMigrationTarget) map[int64][]legacyMigrationTarget {
	out := map[int64][]legacyMigrationTarget{}
	for _, item := range items {
		out[item.RouteID] = append(out[item.RouteID], item)
	}
	return out
}

func legacyMigrationPublishedNames(ctx context.Context, q legacyMigrationQueryer) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, `SELECT public_name FROM published_models`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	return out, rows.Err()
}

func legacyMigrationSiteNames(ctx context.Context, q legacyMigrationQueryer) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM sites`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	return out, rows.Err()
}

func legacyMigrationCredentialNames(ctx context.Context, q legacyMigrationQueryer, siteID int64) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM inference_credentials WHERE site_id=?`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	return out, rows.Err()
}

func uniqueLegacyMigrationName(base string, legacyID int64, used map[string]struct{}) string {
	base = firstLegacyName(base, fmt.Sprintf("Legacy Site %d", legacyID))
	if _, exists := used[strings.ToLower(base)]; !exists {
		used[strings.ToLower(base)] = struct{}{}
		return base
	}
	for suffix := 0; ; suffix++ {
		candidate := fmt.Sprintf("%s (Legacy %d)", base, legacyID)
		if suffix > 0 {
			candidate = fmt.Sprintf("%s (Legacy %d-%d)", base, legacyID, suffix+1)
		}
		key := strings.ToLower(candidate)
		if _, exists := used[key]; !exists {
			used[key] = struct{}{}
			return candidate
		}
	}
}

func uniqueLegacyMigrationCredentialName(base, upstream string, legacyID int64, used map[string]struct{}) string {
	base = firstLegacyName(base, "Default")
	candidates := []string{base, base + " / " + firstLegacyName(upstream, "Legacy"), fmt.Sprintf("%s #%d", base, legacyID)}
	for _, candidate := range candidates {
		key := strings.ToLower(candidate)
		if _, exists := used[key]; !exists {
			used[key] = struct{}{}
			return candidate
		}
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s #%d-%d", base, legacyID, suffix)
		key := strings.ToLower(candidate)
		if _, exists := used[key]; !exists {
			used[key] = struct{}{}
			return candidate
		}
	}
}

func firstLegacyName(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}
func legacyMigrationModelKey(endpointID int64, name string) string {
	return fmt.Sprintf("%d\x00%s", endpointID, strings.TrimSpace(name))
}
func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
func nullableInt64Pointer(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableNullInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
func nullableNullString(value sql.NullString) any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return value.String
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func findLegacyUpstream(items []legacyMigrationUpstream, id int64) legacyMigrationUpstream {
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	return legacyMigrationUpstream{Kind: "compatible"}
}
