package vnextmigration

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	vnextpricing "github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	vnextsecretbox "github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

type OpenAISurfacePolicy string

const (
	OpenAISurfaceChat      OpenAISurfacePolicy = "chat"
	OpenAISurfaceResponses OpenAISurfacePolicy = "responses"
	OpenAISurfaceBoth      OpenAISurfacePolicy = "both"
)

type MigrationOptions struct {
	MasterKey         []byte
	OpenAISurface     OpenAISurfacePolicy
	PriceSKUOverrides map[string]string
	NowMS             int64
}

type MigrationResult struct {
	SourcePath                  string              `json:"sourcePath"`
	DestinationPath             string              `json:"destinationPath"`
	OpenAISurfacePolicy         OpenAISurfacePolicy `json:"openAiSurfacePolicy"`
	PriceCatalogVersion         string              `json:"priceCatalogVersion"`
	Sites                       int                 `json:"sites"`
	Endpoints                   int                 `json:"endpoints"`
	Credentials                 int                 `json:"credentials"`
	ProviderModelTargets        int                 `json:"providerModelTargets"`
	PublishedModels             int                 `json:"publishedModels"`
	PublishedModelTargets       int                 `json:"publishedModelTargets"`
	RoutingProfiles             int                 `json:"routingProfiles"`
	DownstreamKeys              int                 `json:"downstreamKeys"`
	NonRevealableDownstreamKeys int                 `json:"nonRevealableDownstreamKeys"`
	SiteAccountConnections      int                 `json:"siteAccountConnections"`
	BalanceSnapshots            int                 `json:"balanceSnapshots"`
	SiteUsageRecords            int                 `json:"siteUsageRecords"`
	Warnings                    []string            `json:"warnings,omitempty"`
}

func ParseMasterKeyHex(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("master key must be exactly 64 hexadecimal characters")
	}
	return decoded, nil
}

func MigrateSQLiteFile(
	ctx context.Context,
	sourcePath string,
	destinationPath string,
	options MigrationOptions,
) (MigrationResult, error) {
	if err := validateMigrationOptions(options); err != nil {
		return MigrationResult{}, err
	}
	sourceAbsolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("resolve source database path: %w", err)
	}
	destinationAbsolute, err := filepath.Abs(destinationPath)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("resolve destination database path: %w", err)
	}
	if samePath(sourceAbsolute, destinationAbsolute) {
		return MigrationResult{}, errors.New("source and destination database paths must be different")
	}
	if err := validateSourceFile(sourceAbsolute); err != nil {
		return MigrationResult{}, err
	}
	if err := requireEmptyDestination(destinationAbsolute); err != nil {
		return MigrationResult{}, err
	}

	sourceDB, err := openReadOnlySQLite(ctx, sourceAbsolute)
	if err != nil {
		return MigrationResult{}, err
	}
	defer sourceDB.Close()
	sourceTx, err := sourceDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MigrationResult{}, fmt.Errorf("begin source snapshot: %w", err)
	}
	defer sourceTx.Rollback()
	schema, err := inspectSchema(ctx, sourceTx)
	if err != nil {
		return MigrationResult{}, err
	}
	if !schema.hasTable("downstream_keys") {
		return MigrationResult{}, errors.New("source database does not contain downstream_keys")
	}
	source, err := loadMigrationSource(ctx, sourceTx, schema)
	if err != nil {
		return MigrationResult{}, err
	}
	if err := sourceTx.Commit(); err != nil {
		return MigrationResult{}, fmt.Errorf("finish source snapshot: %w", err)
	}

	legacyCipher, err := newLegacyCipher(options.MasterKey)
	if err != nil {
		return MigrationResult{}, err
	}
	box, err := vnextsecretbox.New(options.MasterKey)
	if err != nil {
		return MigrationResult{}, err
	}
	priceSKUs, err := resolveMigrationPriceSKUs(source, options.PriceSKUOverrides)
	if err != nil {
		return MigrationResult{}, err
	}

	stagePath, cleanupStage, err := createMigrationStage(destinationAbsolute)
	if err != nil {
		return MigrationResult{}, err
	}
	stageCommitted := false
	defer func() {
		if !stageCommitted {
			cleanupStage()
		}
	}()
	storage, err := vnextstore.Open(ctx, stagePath)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("initialize VNext destination: %w", err)
	}
	result, applyErr := applyMigration(ctx, storage.DB, source, legacyCipher, box, priceSKUs, options)
	if applyErr == nil {
		_, applyErr = storage.DB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	}
	closeErr := storage.Close()
	if applyErr != nil {
		return MigrationResult{}, applyErr
	}
	if closeErr != nil {
		return MigrationResult{}, fmt.Errorf("close VNext destination: %w", closeErr)
	}
	if err := requireEmptyDestination(destinationAbsolute); err != nil {
		return MigrationResult{}, fmt.Errorf("destination changed during migration: %w", err)
	}
	if err := installMigrationStage(stagePath, destinationAbsolute); err != nil {
		return MigrationResult{}, err
	}
	stageCommitted = true
	result.SourcePath = sourceAbsolute
	result.DestinationPath = destinationAbsolute
	result.OpenAISurfacePolicy = options.OpenAISurface
	result.PriceCatalogVersion = vnextpricing.BuiltinOfficialCatalogVersion
	return result, nil
}

func WriteMigrationResultJSON(result MigrationResult) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}

func validateMigrationOptions(options MigrationOptions) error {
	if len(options.MasterKey) != 32 {
		return errors.New("migration requires the existing 32-byte JIESHAN_SECRET_KEY")
	}
	switch options.OpenAISurface {
	case OpenAISurfaceChat, OpenAISurfaceResponses, OpenAISurfaceBoth:
	default:
		return errors.New("an explicit OpenAI surface policy is required: chat, responses, or both")
	}
	for model, sku := range options.PriceSKUOverrides {
		if strings.TrimSpace(model) == "" || strings.TrimSpace(sku) == "" {
			return errors.New("price SKU overrides require non-empty model and SKU names")
		}
	}
	return nil
}

func resolveMigrationPriceSKUs(source migrationSource, overrides map[string]string) (map[string]string, error) {
	catalog := vnextpricing.BuiltinOfficialUSDCatalog()
	available := make(map[string]string, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		available[strings.ToLower(strings.TrimSpace(entry.SKU))] = entry.SKU
	}
	overrideByModel := make(map[string]string, len(overrides))
	for model, sku := range overrides {
		overrideByModel[strings.ToLower(strings.TrimSpace(model))] = strings.TrimSpace(sku)
	}
	result := make(map[string]string)
	missing := make([]string, 0)
	for _, model := range source.canonicalModels() {
		candidate := strings.TrimSpace(model.priceSKU)
		if candidate == "" {
			candidate = model.name
		}
		if override := overrideByModel[strings.ToLower(model.name)]; override != "" {
			candidate = override
		}
		canonical, ok := available[strings.ToLower(candidate)]
		if !ok {
			missing = append(missing, fmt.Sprintf("%s=%s", model.name, candidate))
			continue
		}
		result[model.name] = canonical
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("official price SKU mapping required for %s; provide explicit model=SKU overrides", strings.Join(missing, ", "))
	}
	return result, nil
}

func validateSourceFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat source database: %w", err)
	}
	if info.IsDir() {
		return errors.New("source database path is a directory")
	}
	return nil
}

func requireEmptyDestination(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat destination database: %w", err)
	}
	if info.IsDir() {
		return errors.New("destination database path is a directory")
	}
	if info.Size() != 0 {
		return fmt.Errorf("destination database must be empty: %s contains %d bytes", path, info.Size())
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if sidecar, sidecarErr := os.Stat(path + suffix); sidecarErr == nil && sidecar.Size() > 0 {
			return fmt.Errorf("destination database sidecar must be empty: %s", path+suffix)
		} else if sidecarErr != nil && !errors.Is(sidecarErr, os.ErrNotExist) {
			return fmt.Errorf("stat destination database sidecar: %w", sidecarErr)
		}
	}
	return nil
}

func openReadOnlySQLite(ctx context.Context, path string) (*sql.DB, error) {
	slashPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	dsnURL := url.URL{Scheme: "file", Path: slashPath}
	db, err := sql.Open("sqlite", dsnURL.String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open source database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping source database read-only: %w", err)
	}
	return db, nil
}

func createMigrationStage(destination string) (string, func(), error) {
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", nil, fmt.Errorf("create destination directory: %w", err)
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(destination)+".migration-*")
	if err != nil {
		return "", nil, fmt.Errorf("create migration staging database: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", nil, fmt.Errorf("close migration staging database: %w", err)
	}
	cleanup := func() {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(path + suffix)
		}
	}
	return path, cleanup, nil
}

func installMigrationStage(stage, destination string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(stage + suffix); err == nil && info.Size() > 0 {
			return fmt.Errorf("staging database still has a non-empty %s sidecar", suffix)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat staging database sidecar: %w", err)
		}
		_ = os.Remove(stage + suffix)
	}
	if info, err := os.Stat(destination); err == nil {
		if info.Size() != 0 {
			return errors.New("destination database became non-empty before installation")
		}
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("remove empty destination placeholder: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat destination before installation: %w", err)
	}
	if err := os.Rename(stage, destination); err != nil {
		return fmt.Errorf("install migrated database: %w", err)
	}
	return nil
}

func samePath(left, right string) bool {
	if strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func migrationNowMS(value int64) int64 {
	if value > 0 {
		return value
	}
	return time.Now().UTC().UnixMilli()
}
