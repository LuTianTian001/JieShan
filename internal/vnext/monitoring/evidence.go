package monitoring

import (
	"errors"
	"sort"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

const (
	LiveTrafficEvidenceWindow = time.Hour
	ProbeEvidenceWindow       = 24 * time.Hour
)

type EvidenceSource string

const (
	EvidenceLiveTraffic EvidenceSource = "live_traffic"
	EvidenceProbe       EvidenceSource = "probe"
)

type EvidenceObservation struct {
	Outcome       Outcome
	FailureKind   routing.FailureKind
	FirstOutputMS *int64
	ObservedAt    time.Time
}

type EvidenceSummary struct {
	Source             EvidenceSource
	Window             time.Duration
	Samples            int
	Successes          int
	Failures           int
	Skipped            int
	SuccessBasisPoints int
	P50FirstOutputMS   *int64
	P95FirstOutputMS   *int64
	LastObservedAt     *time.Time
	LastFailureKind    routing.FailureKind
}

func SummarizeEvidence(
	source EvidenceSource,
	window time.Duration,
	observations []EvidenceObservation,
) (EvidenceSummary, error) {
	if source != EvidenceLiveTraffic && source != EvidenceProbe {
		return EvidenceSummary{}, errors.New("monitor evidence source is invalid")
	}
	if window <= 0 {
		return EvidenceSummary{}, errors.New("monitor evidence window must be positive")
	}
	summary := EvidenceSummary{Source: source, Window: window}
	firstOutputs := make([]int64, 0, len(observations))
	var lastFailureAt time.Time
	for _, observation := range observations {
		if observation.ObservedAt.IsZero() {
			return EvidenceSummary{}, errors.New("monitor evidence time is required")
		}
		switch observation.Outcome {
		case OutcomeSuccess:
			summary.Successes++
		case OutcomeFailure:
			if observation.FailureKind == "" {
				return EvidenceSummary{}, errors.New("failed monitor evidence requires a failure kind")
			}
			summary.Failures++
			if lastFailureAt.IsZero() || !observation.ObservedAt.Before(lastFailureAt) {
				lastFailureAt = observation.ObservedAt
				summary.LastFailureKind = observation.FailureKind
			}
		case OutcomeSkipped:
			summary.Skipped++
		default:
			return EvidenceSummary{}, errors.New("monitor evidence outcome is invalid")
		}
		if observation.FirstOutputMS != nil {
			if *observation.FirstOutputMS < 0 {
				return EvidenceSummary{}, errors.New("monitor evidence first output cannot be negative")
			}
			firstOutputs = append(firstOutputs, *observation.FirstOutputMS)
		}
		if summary.LastObservedAt == nil || observation.ObservedAt.After(*summary.LastObservedAt) {
			observedAt := observation.ObservedAt
			summary.LastObservedAt = &observedAt
		}
	}
	summary.Samples = summary.Successes + summary.Failures + summary.Skipped
	attempted := summary.Successes + summary.Failures
	if attempted > 0 {
		summary.SuccessBasisPoints = summary.Successes * 10_000 / attempted
	}
	summary.P50FirstOutputMS = percentileMilliseconds(firstOutputs, 50)
	summary.P95FirstOutputMS = percentileMilliseconds(firstOutputs, 95)
	return summary, nil
}

func percentileMilliseconds(values []int64, percentile int) *int64 {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	index := (percentile*len(ordered) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(ordered) {
		index = len(ordered)
	}
	value := ordered[index-1]
	return &value
}
