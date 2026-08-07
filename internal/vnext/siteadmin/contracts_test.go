package siteadmin

import (
	"context"
	"strings"
	"testing"
	"time"
)

type metadataOnlyAdapter struct{}

func (metadataOnlyAdapter) Kind() string               { return "metadata-only" }
func (metadataOnlyAdapter) Capabilities() Capabilities { return Capabilities{Usage: true} }

type usageAdapter struct{ metadataOnlyAdapter }

func (usageAdapter) ReadUsage(context.Context, Connection, UsageQuery) (UsagePage, *SessionUpdate, error) {
	return UsagePage{}, nil, nil
}

func TestValidateAdapterRejectsFalseCapabilityClaim(t *testing.T) {
	err := ValidateAdapter(metadataOnlyAdapter{})
	if err == nil || !strings.Contains(err.Error(), "advertises usage") {
		t.Fatalf("expected false usage capability to fail, got %v", err)
	}
	if err := ValidateAdapter(usageAdapter{}); err != nil {
		t.Fatalf("expected implemented usage capability to pass: %v", err)
	}
}

func TestAmountRequiresExactDecimalAndUnit(t *testing.T) {
	for _, amount := range []Amount{
		{Value: "1e-3", Unit: "USD"},
		{Value: "12.50", Unit: ""},
		{Value: "", Unit: "USD"},
	} {
		if err := amount.Validate(); err == nil {
			t.Fatalf("expected invalid amount %#v", amount)
		}
	}
	if err := (Amount{Value: "-0.1250", Unit: "CNY"}).Validate(); err != nil {
		t.Fatalf("expected exact decimal amount to pass: %v", err)
	}
}

func TestUsageRecordDedupPrefersRemoteID(t *testing.T) {
	record := UsageRecord{RemoteID: " abc "}
	if got := record.DedupKey(); got != "remote:abc" {
		t.Fatalf("unexpected remote dedup key %q", got)
	}
}

func TestUsageRecordFingerprintUsesOnlyNormalizedMetadata(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	input := int64(12)
	output := int64(3)
	first := UsageRecord{
		OccurredAt: now,
		RequestID:  " request-1 ",
		Model:      " model-a ",
		Tokens:     TokenUsage{Input: &input, Output: &output},
		Charge:     &Amount{Value: "0.001", Unit: "USD"},
	}
	second := first
	second.OccurredAt = now.UTC()
	second.RequestID = "request-1"
	second.Model = "model-a"
	if first.DedupKey() != second.DedupKey() {
		t.Fatal("expected equivalent metadata to produce the same fingerprint")
	}
	second.Tokens.Output = &input
	if first.DedupKey() == second.DedupKey() {
		t.Fatal("expected token changes to alter the fingerprint")
	}
}

func TestUsageQueryBounds(t *testing.T) {
	from := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	if err := (UsageQuery{Limit: 500, From: from, To: from.Add(time.Hour)}).Validate(); err != nil {
		t.Fatalf("expected bounded query to pass: %v", err)
	}
	if err := (UsageQuery{Limit: 501}).Validate(); err == nil {
		t.Fatal("expected oversized page to fail")
	}
	if err := (UsageQuery{Limit: 100, From: from, To: from.Add(-time.Second)}).Validate(); err == nil {
		t.Fatal("expected inverted time range to fail")
	}
}
