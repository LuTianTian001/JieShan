package accountadapter

import "context"

// Kind identifies a supported upstream account API contract.
type Kind string

const (
	KindCiii   Kind = "ciii"
	KindNewAPI Kind = "new_api"
	KindOneAPI Kind = "one_api"
)

// Credentials contains only the management credentials needed by an adapter.
// Callers are responsible for encrypting these values at rest.
type Credentials struct {
	Authorization string `json:"authorization,omitempty"`
	AccessToken   string `json:"access_token,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

// Connection describes one upstream account endpoint.
type Connection struct {
	Origin      string      `json:"origin"`
	Credentials Credentials `json:"credentials"`
}

// Snapshot is a normalized view of the upstream account balance.
// Monetary values are strings so JSON decoding never introduces float drift.
type Snapshot struct {
	AccountID    string `json:"account_id,omitempty"`
	Username     string `json:"username,omitempty"`
	Status       string `json:"status,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Balance      string `json:"balance,omitempty"`
	Used         string `json:"used,omitempty"`
	Frozen       string `json:"frozen,omitempty"`
	QuotaPerUnit string `json:"quota_per_unit,omitempty"`
	RequestCount int64  `json:"request_count,omitempty"`
}

// UsageWindow describes one subscription quota window.
type UsageWindow struct {
	Used     string `json:"used,omitempty"`
	Limit    string `json:"limit,omitempty"`
	StartsAt string `json:"starts_at,omitempty"`
	EndsAt   string `json:"ends_at,omitempty"`
	Label    string `json:"label,omitempty"`
}

// Subscription is the common subset exposed by subscription-based upstreams.
type Subscription struct {
	ID              string      `json:"id"`
	Name            string      `json:"name,omitempty"`
	Status          string      `json:"status,omitempty"`
	Currency        string      `json:"currency,omitempty"`
	QuotaPerUnit    string      `json:"quota_per_unit,omitempty"`
	StartsAt        string      `json:"starts_at,omitempty"`
	ExpiresAt       string      `json:"expires_at,omitempty"`
	NextResetAt     string      `json:"next_reset_at,omitempty"`
	AmountTotal     string      `json:"amount_total,omitempty"`
	AmountUsed      string      `json:"amount_used,omitempty"`
	AmountRemaining string      `json:"amount_remaining,omitempty"`
	GroupID         string      `json:"group_id,omitempty"`
	GroupName       string      `json:"group_name,omitempty"`
	Platform        string      `json:"platform,omitempty"`
	RateMultiplier  string      `json:"rate_multiplier,omitempty"`
	Daily           UsageWindow `json:"daily,omitempty"`
	Weekly          UsageWindow `json:"weekly,omitempty"`
	Monthly         UsageWindow `json:"monthly,omitempty"`
}

// UsageQuery is normalized as one-based pagination. Adapters translate it to
// the upstream convention (One API, for example, uses a zero-based p value).
type UsageQuery struct {
	Page              int    `json:"page,omitempty"`
	PageSize          int    `json:"page_size,omitempty"`
	StartUnix         int64  `json:"start_unix,omitempty"`
	EndUnix           int64  `json:"end_unix,omitempty"`
	StartDate         string `json:"start_date,omitempty"`
	EndDate           string `json:"end_date,omitempty"`
	Type              int    `json:"type,omitempty"`
	Model             string `json:"model,omitempty"`
	TokenName         string `json:"token_name,omitempty"`
	Group             string `json:"group,omitempty"`
	APIKeyID          string `json:"api_key_id,omitempty"`
	RequestID         string `json:"request_id,omitempty"`
	UpstreamRequestID string `json:"upstream_request_id,omitempty"`
	RequestType       string `json:"request_type,omitempty"`
	BillingType       *int   `json:"billing_type,omitempty"`
	BillingMode       string `json:"billing_mode,omitempty"`
	SortBy            string `json:"sort_by,omitempty"`
	SortOrder         string `json:"sort_order,omitempty"`
}

// UsagePage contains normalized upstream usage records.
type UsagePage struct {
	Items        []UsageItem `json:"items"`
	Total        int64       `json:"total,omitempty"`
	Page         int         `json:"page"`
	PageSize     int         `json:"page_size"`
	Pages        int         `json:"pages,omitempty"`
	HasMore      bool        `json:"has_more,omitempty"`
	Unit         string      `json:"unit,omitempty"`
	QuotaPerUnit string      `json:"quota_per_unit,omitempty"`
}

// UsageItem preserves the fields needed by the monitoring and log UI without
// leaking an upstream-specific response shape into the rest of JieShan.
type UsageItem struct {
	ID                  string `json:"id,omitempty"`
	RequestID           string `json:"request_id,omitempty"`
	UpstreamRequestID   string `json:"upstream_request_id,omitempty"`
	Model               string `json:"model,omitempty"`
	UpstreamModel       string `json:"upstream_model,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	PromptTokens        int64  `json:"prompt_tokens,omitempty"`
	CompletionTokens    int64  `json:"completion_tokens,omitempty"`
	CacheReadTokens     int64  `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64  `json:"cache_creation_tokens,omitempty"`
	TotalTokens         int64  `json:"total_tokens,omitempty"`
	Quota               string `json:"quota,omitempty"`
	TotalCost           string `json:"total_cost,omitempty"`
	ActualCost          string `json:"actual_cost,omitempty"`
	RateMultiplier      string `json:"rate_multiplier,omitempty"`
	ModelMultiplier     string `json:"model_multiplier,omitempty"`
	GroupMultiplier     string `json:"group_multiplier,omitempty"`
	Type                string `json:"type,omitempty"`
	BillingType         string `json:"billing_type,omitempty"`
	BillingMode         string `json:"billing_mode,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	IPAddress           string `json:"ip_address,omitempty"`
	APIKeyID            string `json:"api_key_id,omitempty"`
	APIKeyName          string `json:"api_key_name,omitempty"`
	GroupID             string `json:"group_id,omitempty"`
	GroupName           string `json:"group_name,omitempty"`
	DurationMS          int64  `json:"duration_ms,omitempty"`
	FirstTokenMS        int64  `json:"first_token_ms,omitempty"`
	StatusCode          int    `json:"status_code,omitempty"`
	Stream              bool   `json:"stream,omitempty"`
	CreatedAt           string `json:"created_at,omitempty"`
	Content             string `json:"content,omitempty"`
}

// Adapter normalizes one upstream account API contract.
type Adapter interface {
	Kind() Kind
	Snapshot(context.Context, Connection) (Snapshot, *Credentials, error)
	Subscriptions(context.Context, Connection) ([]Subscription, *Credentials, error)
	Usage(context.Context, Connection, UsageQuery) (UsagePage, *Credentials, error)
}
