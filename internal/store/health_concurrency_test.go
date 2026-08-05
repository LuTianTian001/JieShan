package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/health"
)

func TestRecordTargetFailureCountsConcurrentIncidentsAtomically(t *testing.T) {
	s, target := newHealthTestTarget(t, 100)
	defer s.Close()
	decision := health.Decision{Class: health.ClassUpstreamTransient, PenalizeTarget: true}
	now := time.Now().UnixMilli()

	const failures = 20
	var group sync.WaitGroup
	errorsCh := make(chan error, failures)
	for i := 0; i < failures; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errorsCh <- s.RecordTargetFailure(context.Background(), target, decision, fmt.Sprintf("incident-%d", index), "temporary failure", now, 0)
		}(i)
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("record failure: %v", err)
		}
	}
	var count int
	if err := s.DB.QueryRow("SELECT consecutive_failures FROM target_health WHERE target_id=?", target.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != failures {
		t.Fatalf("consecutive failures = %d, want %d", count, failures)
	}
}

func TestRecordTargetFailureDeduplicatesAndRedacts(t *testing.T) {
	s, target := newHealthTestTarget(t, 3)
	defer s.Close()
	decision := health.Decision{Class: health.ClassUpstreamTransient, PenalizeTarget: true}
	now := time.Now().UnixMilli()
	message := `Get "https://example.com/v1/models?key=sk-secretabcdefgh": Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payloadpayload.signaturevalue`
	for i := 0; i < 2; i++ {
		if err := s.RecordTargetFailure(context.Background(), target, decision, "same-incident", message, now, 0); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var stored string
	if err := s.DB.QueryRow("SELECT consecutive_failures,last_error_message FROM target_health WHERE target_id=?", target.ID).Scan(&count, &stored); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate incident counted %d times", count)
	}
	for _, secret := range []string{"sk-secret", "Bearer eyJ", "?key="} {
		if strings.Contains(stored, secret) {
			t.Fatalf("stored health error contains %q: %s", secret, stored)
		}
	}
}

func newHealthTestTarget(t *testing.T, threshold int) (*Store, RouteTarget) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	upstreamID, err := s.CreateUpstream(ctx, UpstreamWrite{Name: "test", Kind: "compatible", BaseURL: "https://example.com", Enabled: true, CustomHeaders: []byte(`{}`), SecretCipher: []byte("cipher")})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if _, err := s.ApplyDiscoveredModels(ctx, upstreamID, []string{"test-model"}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	models, err := s.ListUpstreamModels(ctx, upstreamID)
	if err != nil || len(models) != 1 {
		s.Close()
		t.Fatalf("models=%v err=%v", models, err)
	}
	routeID, err := s.CreateRoute(ctx, RouteWrite{PublicModel: "public-model", Enabled: true, MonitorEnabled: true, MonitorIntervalSeconds: 300, CooldownSeconds: 300, FailureThreshold: threshold, FailureWindowSeconds: 300, TargetModelIDs: []int64{models[0].ID}})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	route, err := s.GetRoute(ctx, routeID)
	if err != nil || len(route.Targets) != 1 {
		s.Close()
		t.Fatalf("route=%v err=%v", route, err)
	}
	return s, route.Targets[0]
}
