package store

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

func TestTargetAttemptSequencesAreConcurrentMonotonicAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vnext.db")
	s := openTestStoreAt(t, path)
	targetID := createHealthTestTarget(t, s)
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	const attempts = 48
	results := make(chan TargetAttemptPermit, attempts)
	errorsFound := make(chan error, attempts)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			permit, err := s.AcquireTargetAttempt(
				context.Background(), targetID, 1, routing.DefaultHealthPolicy(), now,
			)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- permit
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("AcquireTargetAttempt: %v", err)
	}
	if t.Failed() {
		return
	}

	sequences := make([]int, 0, attempts)
	for result := range results {
		if !result.Permit.Allowed || result.Permit.Mode != routing.PermitNormal {
			t.Fatalf("closed target permit = %+v", result.Permit)
		}
		if result.Sequence != result.Permit.Sequence {
			t.Fatalf("allocated sequence %d differs from permit sequence %d", result.Sequence, result.Permit.Sequence)
		}
		sequences = append(sequences, int(result.Sequence))
	}
	if len(sequences) != attempts {
		t.Fatalf("allocated %d sequences, want %d", len(sequences), attempts)
	}
	sort.Ints(sequences)
	for index, sequence := range sequences {
		if sequence != index+1 {
			t.Fatalf("sequence[%d] = %d, want %d", index, sequence, index+1)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	permit, err := reopened.AcquireTargetAttempt(
		context.Background(), targetID, 1, routing.DefaultHealthPolicy(), now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if permit.Sequence != attempts+1 {
		t.Fatalf("sequence after restart = %d, want %d", permit.Sequence, attempts+1)
	}
}

func TestFailureWindowAndSingleHalfOpenLeaseSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vnext.db")
	s := openTestStoreAt(t, path)
	targetID := createHealthTestTarget(t, s)
	policy := routing.DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	first, err := s.AcquireTargetAttempt(context.Background(), targetID, 1, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, applied, err := s.ApplyTargetHealthEvent(context.Background(), targetID, policy, routing.HealthEvent{
		Revision: 1, Sequence: first.Sequence, OccurredAt: now,
		Outcome: routing.HealthFailure, IncidentID: "request-one",
		Failure: routing.Failure{Kind: routing.FailureTransport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Counted || snapshot.State.Phase != routing.CircuitSuspect ||
		!snapshot.State.FailureWindowStartedAt.Equal(now) {
		t.Fatalf("first durable failure = result=%+v state=%+v", applied, snapshot.State)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	secondAt := now.Add(time.Minute)
	second, err := s.AcquireTargetAttempt(context.Background(), targetID, 1, policy, secondAt)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, applied, err = s.ApplyTargetHealthEvent(context.Background(), targetID, policy, routing.HealthEvent{
		Revision: 1, Sequence: second.Sequence, OccurredAt: secondAt,
		Outcome: routing.HealthFailure, IncidentID: "request-two",
		Failure: routing.Failure{Kind: routing.FailureUpstreamTransient},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Counted || snapshot.State.Phase != routing.CircuitOpen ||
		!snapshot.State.FailureWindowStartedAt.Equal(now) {
		t.Fatalf("second durable failure = result=%+v state=%+v", applied, snapshot.State)
	}
	cooldownUntil := snapshot.State.CooldownUntil

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const contenders = 24
	results := make(chan TargetAttemptPermit, contenders)
	errorsFound := make(chan error, contenders)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			permit, acquireErr := s.AcquireTargetAttempt(
				context.Background(), targetID, 1, policy, cooldownUntil,
			)
			if acquireErr != nil {
				errorsFound <- acquireErr
				return
			}
			results <- permit
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsFound)
	for acquireErr := range errorsFound {
		t.Errorf("half-open acquire: %v", acquireErr)
	}
	if t.Failed() {
		return
	}

	granted := 0
	var grantedSequence uint64
	for result := range results {
		if result.Permit.Allowed {
			granted++
			grantedSequence = result.Sequence
			if result.Permit.Mode != routing.PermitHalfOpen {
				t.Fatalf("half-open winner mode = %q", result.Permit.Mode)
			}
		} else if result.Permit.Reason != routing.PermitLeaseHeld {
			t.Fatalf("half-open loser reason = %q", result.Permit.Reason)
		}
	}
	if granted != 1 {
		t.Fatalf("half-open permits granted = %d, want 1", granted)
	}
	snapshot, err = s.GetTargetHealth(context.Background(), targetID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.Phase != routing.CircuitHalfOpen || snapshot.State.HalfOpenSequence != grantedSequence ||
		!snapshot.State.HalfOpenLeaseUntil.After(cooldownUntil) {
		t.Fatalf("durable half-open lease = %+v, winner=%d", snapshot.State, grantedSequence)
	}

	recoveredAt := cooldownUntil.Add(time.Second)
	snapshot, applied, err = s.ApplyTargetHealthEvent(context.Background(), targetID, policy, routing.HealthEvent{
		Revision: 1, Sequence: grantedSequence, OccurredAt: recoveredAt, Outcome: routing.HealthSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || snapshot.State.Phase != routing.CircuitClosed ||
		snapshot.State.ConsecutiveFailures != 0 || !snapshot.State.CooldownUntil.IsZero() ||
		!snapshot.State.HalfOpenLeaseUntil.IsZero() {
		t.Fatalf("successful durable half-open trial did not recover target: result=%+v state=%+v", applied, snapshot.State)
	}
}

func TestConcurrentHealthEventsCannotOverwriteNewerSequence(t *testing.T) {
	s := newTestStore(t)
	targetID := createHealthTestTarget(t, s)
	policy := routing.DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	const attempts = 32
	for index := 0; index < attempts; index++ {
		if _, err := s.AcquireTargetAttempt(context.Background(), targetID, 1, policy, now); err != nil {
			t.Fatal(err)
		}
	}

	errorsFound := make(chan error, attempts)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for sequence := uint64(1); sequence <= attempts; sequence++ {
		wait.Add(1)
		go func(sequence uint64) {
			defer wait.Done()
			<-start
			event := routing.HealthEvent{
				Revision: 1, Sequence: sequence,
				OccurredAt: now.Add(time.Duration(sequence) * time.Millisecond),
				Outcome:    routing.HealthFailure,
				IncidentID: "independent-request",
				Failure:    routing.Failure{Kind: routing.FailureTransport},
			}
			if sequence == attempts {
				event.Outcome = routing.HealthSuccess
				event.IncidentID = ""
				event.Failure = routing.Failure{}
			}
			if _, _, err := s.ApplyTargetHealthEvent(context.Background(), targetID, policy, event); err != nil {
				errorsFound <- err
			}
		}(sequence)
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("ApplyTargetHealthEvent: %v", err)
	}
	if t.Failed() {
		return
	}

	snapshot, err := s.GetTargetHealth(context.Background(), targetID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.LastEventSequence != attempts || snapshot.State.Phase != routing.CircuitClosed ||
		snapshot.State.Capability != routing.CapabilitySupported || snapshot.State.ConsecutiveFailures != 0 {
		t.Fatalf("final state was overwritten by an older completion: %+v", snapshot.State)
	}
}

func TestStaleSequenceAndStaleConfigRevisionCannotMutateHealth(t *testing.T) {
	s := newTestStore(t)
	targetID := createHealthTestTarget(t, s)
	policy := routing.DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	first, err := s.AcquireTargetAttempt(context.Background(), targetID, 1, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquireTargetAttempt(context.Background(), targetID, 1, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, result, err := s.ApplyTargetHealthEvent(context.Background(), targetID, policy, routing.HealthEvent{
		Revision: 1, Sequence: second.Sequence, OccurredAt: now.Add(time.Second), Outcome: routing.HealthSuccess,
	})
	if err != nil || !result.Applied {
		t.Fatalf("newer success: result=%+v err=%v", result, err)
	}
	before := snapshot
	snapshot, result, err = s.ApplyTargetHealthEvent(context.Background(), targetID, policy, routing.HealthEvent{
		Revision: 1, Sequence: first.Sequence, OccurredAt: now.Add(2 * time.Second),
		Outcome: routing.HealthFailure, IncidentID: "slow-old-request",
		Failure: routing.Failure{Kind: routing.FailureTransport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Reason != routing.ApplyStaleSequence || snapshot != before {
		t.Fatalf("old sequence changed state: result=%+v before=%+v after=%+v", result, before, snapshot)
	}

	third, err := s.AcquireTargetAttempt(context.Background(), targetID, 1, policy, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE provider_model_targets
SET revision=2,updated_at=? WHERE id=?`, NowMS(), targetID); err != nil {
		t.Fatal(err)
	}
	snapshot, result, err = s.ApplyTargetHealthEvent(context.Background(), targetID, policy, routing.HealthEvent{
		Revision: 1, Sequence: third.Sequence, OccurredAt: now.Add(4 * time.Second),
		Outcome: routing.HealthFailure, IncidentID: "old-config",
		Failure: routing.Failure{Kind: routing.FailureTransport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Reason != routing.ApplyStaleRevision || snapshot != before {
		t.Fatalf("old config revision changed state: result=%+v before=%+v after=%+v", result, before, snapshot)
	}
	if _, err := s.AcquireTargetAttempt(context.Background(), targetID, 1, policy, now.Add(5*time.Second)); !errors.Is(err, routing.ErrStaleRevision) {
		t.Fatalf("stale acquire revision error = %v", err)
	}

	fourth, err := s.AcquireTargetAttempt(context.Background(), targetID, 2, policy, now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Sequence != third.Sequence+1 || fourth.Health.State.Revision != 2 ||
		fourth.Health.State.LastEventSequence != 0 || fourth.Health.State.Capability != routing.CapabilityUnknown {
		t.Fatalf("revision reconciliation did not reset learned health: %+v", fourth)
	}

	_, _, err = s.ApplyTargetHealthEvent(context.Background(), targetID, policy, routing.HealthEvent{
		Revision: 2, Sequence: fourth.Sequence + 100, OccurredAt: now.Add(7 * time.Second), Outcome: routing.HealthSuccess,
	})
	if !errors.Is(err, ErrAttemptSequenceNotAllocated) {
		t.Fatalf("unallocated sequence error = %v", err)
	}
}

func createHealthTestTarget(t *testing.T, s *Store) int64 {
	t.Helper()
	siteID := mustCreateSite(t, s, "Health upstream")
	endpointID := mustCreateEndpoint(t, s, siteID, "Health endpoint", "https://health.example/v1")
	return mustCreateProviderTarget(t, s, siteID, endpointID, "health-model")
}
