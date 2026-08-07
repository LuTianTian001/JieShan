package siteadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Capability string

const (
	CapabilitySessionRefresh Capability = "session_refresh"
	CapabilityBalance        Capability = "balance"
	CapabilityUsage          Capability = "usage"
)

var (
	ErrUnsupportedCapability = errors.New("site administration capability is not supported")
	decimalPattern           = regexp.MustCompile(`^[+-]?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
)

type Capabilities struct {
	SessionRefresh bool
	Balance        bool
	Usage          bool
}

func (c Capabilities) Supports(capability Capability) bool {
	switch capability {
	case CapabilitySessionRefresh:
		return c.SessionRefresh
	case CapabilityBalance:
		return c.Balance
	case CapabilityUsage:
		return c.Usage
	default:
		return false
	}
}

// Secrets are encrypted by the store. Adapters only receive decrypted values
// for the duration of one operation.
type Secrets struct {
	Authorization string
	AccessToken   string
	RefreshToken  string
	Cookie        string
	ExpiresAt     time.Time
}

type Connection struct {
	Origin  string
	Secrets Secrets
}

// Amount preserves the upstream value exactly. Unit may be a currency code,
// points, quota, or another upstream-defined accounting unit.
type Amount struct {
	Value string
	Unit  string
}

func (a Amount) Validate() error {
	if !decimalPattern.MatchString(strings.TrimSpace(a.Value)) {
		return fmt.Errorf("amount value must be an exact decimal: %q", a.Value)
	}
	if strings.TrimSpace(a.Unit) == "" {
		return errors.New("amount unit is required")
	}
	return nil
}

type BalanceSnapshot struct {
	AccountID   string
	AccountName string
	Available   Amount
	Used        *Amount
	CapturedAt  time.Time
}

func (s BalanceSnapshot) Validate() error {
	if err := s.Available.Validate(); err != nil {
		return fmt.Errorf("available balance: %w", err)
	}
	if s.Used != nil {
		if err := s.Used.Validate(); err != nil {
			return fmt.Errorf("used balance: %w", err)
		}
	}
	if s.CapturedAt.IsZero() {
		return errors.New("balance capture time is required")
	}
	return nil
}

type TokenUsage struct {
	Input      *int64
	Output     *int64
	CacheRead  *int64
	CacheWrite *int64
	Reasoning  *int64
	Total      *int64
}

type UsageRecord struct {
	RemoteID          string
	RequestID         string
	UpstreamRequestID string
	OccurredAt        time.Time
	Model             string
	UpstreamModel     string
	Status            string
	HTTPStatus        *int
	Tokens            TokenUsage
	Charge            *Amount
	DurationMS        *int64
	APIKeyName        string
}

func (r UsageRecord) Validate() error {
	if r.OccurredAt.IsZero() {
		return errors.New("usage occurrence time is required")
	}
	if r.Charge != nil {
		if err := r.Charge.Validate(); err != nil {
			return fmt.Errorf("usage charge: %w", err)
		}
	}
	for name, value := range map[string]*int64{
		"input tokens":       r.Tokens.Input,
		"output tokens":      r.Tokens.Output,
		"cache read tokens":  r.Tokens.CacheRead,
		"cache write tokens": r.Tokens.CacheWrite,
		"reasoning tokens":   r.Tokens.Reasoning,
		"total tokens":       r.Tokens.Total,
		"duration ms":        r.DurationMS,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%s cannot be negative", name)
		}
	}
	return nil
}

// DedupKey is scoped by the caller to one site connection. Remote IDs are
// preferred; otherwise a stable metadata fingerprint is used. Prompt and
// response bodies are deliberately absent from UsageRecord.
func (r UsageRecord) DedupKey() string {
	if id := strings.TrimSpace(r.RemoteID); id != "" {
		return "remote:" + id
	}
	fields := []string{
		r.OccurredAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(r.RequestID),
		strings.TrimSpace(r.UpstreamRequestID),
		strings.TrimSpace(r.Model),
		strings.TrimSpace(r.UpstreamModel),
		strings.TrimSpace(r.Status),
		optionalInt(r.HTTPStatus),
		optionalInt64(r.Tokens.Input),
		optionalInt64(r.Tokens.Output),
		optionalInt64(r.Tokens.CacheRead),
		optionalInt64(r.Tokens.CacheWrite),
		optionalInt64(r.Tokens.Reasoning),
		optionalInt64(r.Tokens.Total),
		optionalAmount(r.Charge),
		optionalInt64(r.DurationMS),
		strings.TrimSpace(r.APIKeyName),
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x1f")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type UsageQuery struct {
	Cursor    string
	From      time.Time
	To        time.Time
	Limit     int
	Model     string
	Status    string
	APIKey    string
	RequestID string
}

func (q UsageQuery) Validate() error {
	if q.Limit < 1 || q.Limit > 500 {
		return errors.New("usage page limit must be between 1 and 500")
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.To.Before(q.From) {
		return errors.New("usage end time cannot be before start time")
	}
	return nil
}

type UsagePage struct {
	Records    []UsageRecord
	NextCursor string
	HasMore    bool
	FetchedAt  time.Time
}

type SessionUpdate struct {
	Secrets     Secrets
	Changed     bool
	RefreshedAt time.Time
}

type Adapter interface {
	Kind() string
	Capabilities() Capabilities
}

type SessionRefresher interface {
	Adapter
	RefreshSession(context.Context, Connection) (SessionUpdate, error)
}

type BalanceReader interface {
	Adapter
	ReadBalance(context.Context, Connection) (BalanceSnapshot, *SessionUpdate, error)
}

type UsageReader interface {
	Adapter
	ReadUsage(context.Context, Connection, UsageQuery) (UsagePage, *SessionUpdate, error)
}

// ValidateAdapter prevents an adapter from advertising a capability that its
// concrete implementation cannot execute.
func ValidateAdapter(adapter Adapter) error {
	if adapter == nil {
		return errors.New("site administration adapter is required")
	}
	if strings.TrimSpace(adapter.Kind()) == "" {
		return errors.New("site administration adapter kind is required")
	}
	capabilities := adapter.Capabilities()
	if capabilities.SessionRefresh {
		if _, ok := adapter.(SessionRefresher); !ok {
			return errors.New("adapter advertises session refresh without implementing it")
		}
	}
	if capabilities.Balance {
		if _, ok := adapter.(BalanceReader); !ok {
			return errors.New("adapter advertises balance without implementing it")
		}
	}
	if capabilities.Usage {
		if _, ok := adapter.(UsageReader); !ok {
			return errors.New("adapter advertises usage without implementing it")
		}
	}
	return nil
}

func optionalInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func optionalInt64(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func optionalAmount(value *Amount) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.Value) + "\x1e" + strings.TrimSpace(value.Unit)
}
