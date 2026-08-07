package monitoring

import (
	"fmt"
	"testing"
	"time"
)

func TestSummaryUsesOneRealPointPerRun(t *testing.T) {
	start := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	results := make([]TargetResult, 0, 12)
	for index := 0; index < 12; index++ {
		finished := start.Add(time.Duration(index) * 5 * time.Minute)
		results = append(results, TargetResult{
			RunID:      fmt.Sprintf("run-%02d", index),
			TargetID:   7,
			Outcome:    OutcomeSuccess,
			LatencyMS:  int64(100 + index),
			StartedAt:  finished.Add(-time.Second),
			FinishedAt: finished,
		})
	}
	summary, err := SummarizeTarget(7, results, start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 12 || len(summary.History) != 12 {
		t.Fatalf("expected 12 real five-minute points, got total=%d history=%d", summary.Total, len(summary.History))
	}
	if summary.SuccessBasisPoints() != 10_000 {
		t.Fatalf("unexpected success rate %d", summary.SuccessBasisPoints())
	}
}

func TestSummaryRejectsMultipleFinalResultsForOneRunTarget(t *testing.T) {
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	result := TargetResult{
		RunID:      "run-1",
		TargetID:   7,
		Outcome:    OutcomeSuccess,
		LatencyMS:  100,
		StartedAt:  now,
		FinishedAt: now.Add(time.Second),
	}
	if _, err := SummarizeTarget(7, []TargetResult{result, result}, time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected duplicate final results to fail")
	}
}

func TestModelStateDoesNotCallPartialAvailabilityHealthy(t *testing.T) {
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	success := HistoryPoint{RunID: "success", Outcome: OutcomeSuccess, FinishedAt: now}
	failure := HistoryPoint{RunID: "failure", Outcome: OutcomeFailure, FinishedAt: now}
	state := SummarizeModel([]TargetSummary{
		{TargetID: 1, Latest: &success},
		{TargetID: 2, Latest: &failure},
		{TargetID: 3},
	})
	if state != ModelDegraded {
		t.Fatalf("expected partial availability to be degraded, got %q", state)
	}
	if state := SummarizeModel([]TargetSummary{{TargetID: 1, Latest: &failure}}); state != ModelUnavailable {
		t.Fatalf("expected failed target to be unavailable, got %q", state)
	}
}

func TestAttemptsDoNotBecomeHistoryPoints(t *testing.T) {
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	result := TargetResult{
		RunID:      "run-1",
		TargetID:   7,
		Outcome:    OutcomeSuccess,
		LatencyMS:  120,
		StartedAt:  now,
		FinishedAt: now.Add(time.Second),
		Attempts: []ProbeAttempt{
			{CredentialID: 1, Outcome: OutcomeFailure, FailureKind: "credential_auth", FinishedAt: now},
			{CredentialID: 2, Outcome: OutcomeSuccess, FinishedAt: now.Add(time.Second)},
		},
	}
	summary, err := SummarizeTarget(7, []TargetResult{result}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 1 || summary.Successes != 1 {
		t.Fatalf("credential attempts leaked into target history: %#v", summary)
	}
}

func TestSkippedCircuitPointsRemainVisibleWithoutPollutingSuccessRate(t *testing.T) {
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	results := []TargetResult{
		{
			RunID: "success", TargetID: 7, Outcome: OutcomeSuccess,
			StartedAt: now, FinishedAt: now.Add(time.Second), LatencyMS: 100,
		},
		{
			RunID: "skipped", TargetID: 7, Outcome: OutcomeSkipped, PermitReason: "cooling",
			StartedAt: now.Add(time.Minute), FinishedAt: now.Add(time.Minute),
		},
	}
	summary, err := SummarizeTarget(7, results, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || summary.Successes != 1 || summary.Failures != 0 || summary.Skipped != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.SuccessBasisPoints() != 10_000 {
		t.Fatalf("skipped point reduced success rate to %d", summary.SuccessBasisPoints())
	}
}
