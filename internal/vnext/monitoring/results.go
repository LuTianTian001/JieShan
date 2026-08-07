package monitoring

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

const DefaultProbeInterval = 5 * time.Minute

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
	OutcomeSkipped Outcome = "skipped"
)

type ModelState string

const (
	ModelUnprobed    ModelState = "unprobed"
	ModelHealthy     ModelState = "healthy"
	ModelDegraded    ModelState = "degraded"
	ModelUnavailable ModelState = "unavailable"
)

type ProbeAttempt struct {
	CredentialID int64
	Outcome      Outcome
	FailureKind  routing.FailureKind
	ErrorCode    string
	HTTPStatus   int
	LatencyMS    int64
	FinishedAt   time.Time
}

type TargetResult struct {
	RunID                string
	TargetID             int64
	Outcome              Outcome
	FailureKind          routing.FailureKind
	ErrorCode            string
	HTTPStatus           int
	PermitMode           routing.PermitMode
	PermitReason         routing.PermitReason
	LatencyMS            int64
	FirstOutputLatencyMS *int64
	StartedAt            time.Time
	FinishedAt           time.Time
	HealthApplied        bool
	HealthApplyReason    routing.ApplyReason
	HealthErrorCode      string
	Attempts             []ProbeAttempt
}

func (r TargetResult) Validate() error {
	if strings.TrimSpace(r.RunID) == "" {
		return errors.New("probe run ID is required")
	}
	if r.TargetID <= 0 {
		return errors.New("probe target ID must be positive")
	}
	if r.Outcome != OutcomeSuccess && r.Outcome != OutcomeFailure && r.Outcome != OutcomeSkipped {
		return fmt.Errorf("unsupported probe outcome %q", r.Outcome)
	}
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
		return errors.New("probe result timestamps are invalid")
	}
	if r.LatencyMS < 0 {
		return errors.New("probe latency cannot be negative")
	}
	if r.FirstOutputLatencyMS != nil && (*r.FirstOutputLatencyMS < 0 || *r.FirstOutputLatencyMS > r.LatencyMS) {
		return errors.New("first output latency must fit within total latency")
	}
	if r.HTTPStatus < 0 || r.HTTPStatus > 599 || (r.HTTPStatus > 0 && r.HTTPStatus < 100) {
		return errors.New("probe HTTP status must be zero or between 100 and 599")
	}
	switch r.Outcome {
	case OutcomeSuccess:
		if r.FailureKind != "" {
			return errors.New("successful probe result cannot contain a failure kind")
		}
	case OutcomeFailure:
		if r.FailureKind == "" {
			return errors.New("failed probe result requires a failure kind")
		}
	case OutcomeSkipped:
		if r.PermitReason == "" {
			return errors.New("skipped probe result requires a permit reason")
		}
	}
	return nil
}

type HistoryPoint struct {
	RunID      string
	Outcome    Outcome
	LatencyMS  int64
	FinishedAt time.Time
}

type TargetSummary struct {
	TargetID  int64
	Successes int
	Failures  int
	Skipped   int
	Total     int
	Latest    *HistoryPoint
	History   []HistoryPoint
}

func (s TargetSummary) SuccessBasisPoints() int {
	attempted := s.Successes + s.Failures
	if attempted == 0 {
		return 0
	}
	return s.Successes * 10_000 / attempted
}

// SummarizeTarget consumes one final result per probe run and target. It never
// expands sparse probe data into synthetic minute-level points.
func SummarizeTarget(targetID int64, results []TargetResult, from, to time.Time) (TargetSummary, error) {
	if targetID <= 0 {
		return TargetSummary{}, errors.New("target ID must be positive")
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return TargetSummary{}, errors.New("summary end time cannot be before start time")
	}
	filtered := make([]TargetResult, 0, len(results))
	seenRuns := make(map[string]struct{}, len(results))
	for _, result := range results {
		if result.TargetID != targetID {
			continue
		}
		if err := result.Validate(); err != nil {
			return TargetSummary{}, err
		}
		if !from.IsZero() && result.FinishedAt.Before(from) {
			continue
		}
		if !to.IsZero() && result.FinishedAt.After(to) {
			continue
		}
		if _, exists := seenRuns[result.RunID]; exists {
			return TargetSummary{}, fmt.Errorf("duplicate final result for run %q and target %d", result.RunID, targetID)
		}
		seenRuns[result.RunID] = struct{}{}
		filtered = append(filtered, result)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].FinishedAt.Equal(filtered[j].FinishedAt) {
			return filtered[i].RunID < filtered[j].RunID
		}
		return filtered[i].FinishedAt.Before(filtered[j].FinishedAt)
	})
	summary := TargetSummary{TargetID: targetID, History: make([]HistoryPoint, 0, len(filtered))}
	for _, result := range filtered {
		point := HistoryPoint{
			RunID:      result.RunID,
			Outcome:    result.Outcome,
			LatencyMS:  result.LatencyMS,
			FinishedAt: result.FinishedAt,
		}
		summary.History = append(summary.History, point)
		if result.Outcome == OutcomeSuccess {
			summary.Successes++
		} else if result.Outcome == OutcomeFailure {
			summary.Failures++
		} else {
			summary.Skipped++
		}
	}
	summary.Total = len(summary.History)
	if summary.Total > 0 {
		latest := summary.History[summary.Total-1]
		summary.Latest = &latest
	}
	return summary, nil
}

func SummarizeModel(targets []TargetSummary) ModelState {
	if len(targets) == 0 {
		return ModelUnprobed
	}
	probed := 0
	available := 0
	for _, target := range targets {
		if target.Latest == nil {
			continue
		}
		probed++
		if target.Latest.Outcome == OutcomeSuccess {
			available++
		}
	}
	if probed == 0 {
		return ModelUnprobed
	}
	if available == len(targets) {
		return ModelHealthy
	}
	if available > 0 {
		return ModelDegraded
	}
	return ModelUnavailable
}
