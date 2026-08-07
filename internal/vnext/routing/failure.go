package routing

import "time"

// FailureKind is the stable vocabulary shared by request routing and target
// health. It deliberately distinguishes failures owned by one credential from
// failures owned by the target itself.
type FailureKind string

const (
	FailureClientInvalid             FailureKind = "client_invalid"
	FailureTargetMisconfigured       FailureKind = "target_misconfigured"
	FailureCredentialAuth            FailureKind = "credential_auth"
	FailureCredentialPermission      FailureKind = "credential_permission"
	FailureCredentialPaymentRequired FailureKind = "credential_payment_required"
	FailureCredentialRateLimited     FailureKind = "credential_rate_limited"
	FailureTransport                 FailureKind = "transport"
	FailureFirstOutputTimeout        FailureKind = "first_output_timeout"
	FailureStreamIdleTimeout         FailureKind = "stream_idle_timeout"
	FailureRequestTimeout            FailureKind = "request_timeout"
	FailureUpstreamTransient         FailureKind = "upstream_transient"
	FailureModelUnsupported          FailureKind = "model_unsupported"
	FailureStreamTruncated           FailureKind = "stream_truncated"
	FailureDownstreamCanceled        FailureKind = "downstream_canceled"
	FailureUnknown                   FailureKind = "unknown"
)

type FailureScope string

const (
	FailureScopeRequest    FailureScope = "request"
	FailureScopeCredential FailureScope = "credential"
	FailureScopeTarget     FailureScope = "target"
)

type RetryStep string

const (
	RetryStop           RetryStep = "stop"
	RetryNextCredential RetryStep = "next_credential"
	RetryNextTarget     RetryStep = "next_target"
)

// CredentialEffect tells the credential runtime which ownership boundary to
// update. DenyTargetAccess applies only to the credential-target access
// binding; it never disables the credential globally.
type CredentialEffect string

const (
	CredentialEffectNone             CredentialEffect = "none"
	CredentialEffectInvalidate       CredentialEffect = "invalidate"
	CredentialEffectDenyTargetAccess CredentialEffect = "deny_target_access"
	CredentialEffectExhaust          CredentialEffect = "exhaust"
	CredentialEffectCooldown         CredentialEffect = "cooldown"
)

type Failure struct {
	Kind              FailureKind
	RetryAfter        time.Duration
	ResponseCommitted bool
}

type FailureDisposition struct {
	Scope             FailureScope
	Retry             RetryStep
	PenalizeTarget    bool
	MarkUnsupported   bool
	CredentialEffect  CredentialEffect
	ResponseCommitted bool
}

// Disposition turns a protocol/runtime failure into one routing action. A
// credential-local failure never penalizes the target; the request tries the
// next credential on that same target before moving to the next target.
func (failure Failure) Disposition() FailureDisposition {
	stop := FailureDisposition{
		Scope: FailureScopeRequest, Retry: RetryStop,
		CredentialEffect: CredentialEffectNone,
	}
	switch failure.Kind {
	case FailureCredentialAuth:
		return FailureDisposition{
			Scope: FailureScopeCredential, Retry: RetryNextCredential,
			CredentialEffect: CredentialEffectInvalidate,
		}
	case FailureCredentialPermission:
		// Permission is scoped to this credential-target access binding. It
		// must not invalidate the credential for its other targets.
		return FailureDisposition{
			Scope: FailureScopeCredential, Retry: RetryNextCredential,
			CredentialEffect: CredentialEffectDenyTargetAccess,
		}
	case FailureCredentialPaymentRequired:
		return FailureDisposition{
			Scope: FailureScopeCredential, Retry: RetryNextCredential,
			CredentialEffect: CredentialEffectExhaust,
		}
	case FailureCredentialRateLimited:
		return FailureDisposition{
			Scope: FailureScopeCredential, Retry: RetryNextCredential,
			CredentialEffect: CredentialEffectCooldown,
		}
	case FailureTargetMisconfigured, FailureTransport, FailureFirstOutputTimeout, FailureUpstreamTransient:
		return FailureDisposition{
			Scope: FailureScopeTarget, Retry: RetryNextTarget, PenalizeTarget: true,
			CredentialEffect: CredentialEffectNone,
		}
	case FailureStreamIdleTimeout, FailureRequestTimeout:
		retry := RetryNextTarget
		if failure.ResponseCommitted {
			retry = RetryStop
		}
		return FailureDisposition{
			Scope: FailureScopeTarget, Retry: retry,
			PenalizeTarget: true, ResponseCommitted: failure.ResponseCommitted,
			CredentialEffect: CredentialEffectNone,
		}
	case FailureModelUnsupported:
		return FailureDisposition{
			Scope: FailureScopeTarget, Retry: RetryNextTarget,
			PenalizeTarget: true, MarkUnsupported: true, CredentialEffect: CredentialEffectNone,
		}
	case FailureStreamTruncated:
		retry := RetryNextTarget
		if failure.ResponseCommitted {
			retry = RetryStop
		}
		return FailureDisposition{
			Scope: FailureScopeTarget, Retry: retry,
			PenalizeTarget: true, ResponseCommitted: failure.ResponseCommitted, CredentialEffect: CredentialEffectNone,
		}
	case FailureClientInvalid, FailureDownstreamCanceled, FailureUnknown, "":
		return stop
	default:
		return stop
	}
}
