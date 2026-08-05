package health

import (
	"net/http"
	"testing"
	"time"
)

func TestFailureThresholdRequiresTwoIndependentFailures(t *testing.T) {
	now := time.Now().UnixMilli()
	state := RecordFailure(State{}, now, 2, 5*time.Minute, 5*time.Minute, false)
	if state.Phase != "closed" || state.ConsecutiveFailures != 1 {
		t.Fatalf("first failure should be suspect, got %+v", state)
	}
	state = RecordFailure(state, now+1_000, 2, 5*time.Minute, 5*time.Minute, false)
	if state.Phase != "open" || state.CooldownUntil <= now {
		t.Fatalf("second failure should open circuit, got %+v", state)
	}
}

func TestInvalidSuccessRecognizesChineseUnsupportedModel(t *testing.T) {
	decision := ClassifyInvalidSuccess([]byte(`{"error":{"message":"模型不存在"}}`))
	if decision.Class != ClassModelUnsupported || !decision.Failover || !decision.UnsupportedModel || decision.PenalizeTarget {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestFailureOutsideWindowStartsNewSequence(t *testing.T) {
	state := RecordFailure(State{}, 1_000, 2, time.Minute, 5*time.Minute, false)
	state = RecordFailure(state, 62_000, 2, time.Minute, 5*time.Minute, false)
	if state.Phase != "closed" || state.ConsecutiveFailures != 1 {
		t.Fatalf("expected fresh sequence, got %+v", state)
	}
}

func TestClassifierDoesNotPenalizeClientErrors(t *testing.T) {
	decision := Classify(http.StatusBadRequest, []byte(`{"error":"invalid request"}`), nil, false, nil)
	if decision.Failover || decision.PenalizeTarget {
		t.Fatalf("client input must not affect upstream health: %+v", decision)
	}
	decision = Classify(http.StatusUnauthorized, nil, nil, false, nil)
	if !decision.Failover || !decision.InvalidateCredential {
		t.Fatalf("auth failure should isolate credential: %+v", decision)
	}
	decision = Classify(http.StatusForbidden, []byte(`{"error":"model not found for this account"}`), nil, false, nil)
	if !decision.Failover || !decision.PenalizeTarget || decision.InvalidateCredential || decision.Class != ClassPermissionDenied {
		t.Fatalf("permission failure should cool only the target: %+v", decision)
	}
	decision = Classify(http.StatusPaymentRequired, nil, nil, false, nil)
	if !decision.Failover || !decision.PenalizeTarget || decision.Class != ClassPaymentRequired {
		t.Fatalf("payment failure should fail over and cool the target: %+v", decision)
	}
}
