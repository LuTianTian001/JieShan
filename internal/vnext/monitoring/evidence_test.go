package monitoring

import (
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

func TestSummarizeEvidenceKeepsSourcesAndPercentilesIndependent(t *testing.T) {
	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	first := int64(100)
	second := int64(400)
	third := int64(900)
	summary, err := SummarizeEvidence(EvidenceLiveTraffic, LiveTrafficEvidenceWindow, []EvidenceObservation{
		{Outcome: OutcomeSuccess, FirstOutputMS: &first, ObservedAt: start},
		{Outcome: OutcomeFailure, FailureKind: routing.FailureTransport, FirstOutputMS: &second, ObservedAt: start.Add(time.Minute)},
		{Outcome: OutcomeSkipped, FirstOutputMS: &third, ObservedAt: start.Add(2 * time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Source != EvidenceLiveTraffic || summary.Window != time.Hour || summary.Samples != 3 ||
		summary.Successes != 1 || summary.Failures != 1 || summary.Skipped != 1 || summary.SuccessBasisPoints != 5000 {
		t.Fatalf("evidence summary = %+v", summary)
	}
	if summary.P50FirstOutputMS == nil || *summary.P50FirstOutputMS != 400 ||
		summary.P95FirstOutputMS == nil || *summary.P95FirstOutputMS != 900 {
		t.Fatalf("evidence percentiles = %+v", summary)
	}
	if summary.LastObservedAt == nil || !summary.LastObservedAt.Equal(start.Add(2*time.Minute)) ||
		summary.LastFailureKind != routing.FailureTransport {
		t.Fatalf("latest evidence = %+v", summary)
	}
}
