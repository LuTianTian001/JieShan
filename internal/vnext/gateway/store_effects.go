package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const credentialEffectCASAttempts = 4

type CredentialEffectRepository interface {
	GetCredentialRuntimeState(context.Context, int64) (vnextstore.CredentialRuntimeState, error)
	UpdateCredentialRuntimeState(context.Context, vnextstore.CredentialRuntimeUpdate) (vnextstore.CredentialRuntimeState, error)
	UpsertCredentialTargetAccess(context.Context, vnextstore.CredentialTargetAccessWrite) error
}

type StoreCredentialEffects struct {
	repository      CredentialEffectRepository
	defaultCooldown time.Duration
}

func NewStoreCredentialEffects(repository CredentialEffectRepository, defaultCooldown time.Duration) (*StoreCredentialEffects, error) {
	if repository == nil {
		return nil, errors.New("credential effect repository is required")
	}
	if defaultCooldown <= 0 {
		defaultCooldown = time.Minute
	}
	return &StoreCredentialEffects{repository: repository, defaultCooldown: defaultCooldown}, nil
}

func (effects *StoreCredentialEffects) ApplyCredentialEffect(ctx context.Context, event CredentialEffectEvent) error {
	if event.CredentialID <= 0 || event.TargetID <= 0 || event.SiteID <= 0 || event.EndpointID <= 0 || event.OccurredAt.IsZero() {
		return errors.New("credential effect identity and occurrence time are required")
	}
	switch event.Effect {
	case routing.CredentialEffectNone:
		return nil
	case routing.CredentialEffectDenyTargetAccess:
		if event.HTTPStatus != 403 {
			return errors.New("credential target denial requires HTTP 403")
		}
		checkedAt := event.OccurredAt.UTC().UnixMilli()
		status := event.HTTPStatus
		return effects.repository.UpsertCredentialTargetAccess(ctx, vnextstore.CredentialTargetAccessWrite{
			SiteID: event.SiteID, EndpointID: event.EndpointID, CredentialID: int64(event.CredentialID),
			ProviderModelTargetID: int64(event.TargetID), Availability: "forbidden",
			LastHTTPStatus: &status, LastErrorCode: event.ErrorCode, LastCheckedAt: &checkedAt,
		})
	case routing.CredentialEffectInvalidate:
		if event.HTTPStatus != 401 {
			return errors.New("credential invalidation requires HTTP 401")
		}
		return effects.updateRuntimeState(ctx, event, "invalid", nil)
	case routing.CredentialEffectExhaust:
		if event.HTTPStatus != 402 {
			return errors.New("credential exhaustion requires HTTP 402")
		}
		return effects.updateRuntimeState(ctx, event, "exhausted", nil)
	case routing.CredentialEffectCooldown:
		if event.HTTPStatus != 429 {
			return errors.New("credential cooldown requires HTTP 429")
		}
		duration := event.RetryAfter
		if duration <= 0 {
			duration = effects.defaultCooldown
		}
		until := event.OccurredAt.UTC().Add(duration).UnixMilli()
		return effects.updateRuntimeState(ctx, event, "cooling", &until)
	default:
		return errors.New("credential effect is unsupported")
	}
}

func (effects *StoreCredentialEffects) updateRuntimeState(
	ctx context.Context,
	event CredentialEffectEvent,
	state string,
	coolingUntil *int64,
) error {
	for attempt := 0; attempt < credentialEffectCASAttempts; attempt++ {
		current, err := effects.repository.GetCredentialRuntimeState(ctx, int64(event.CredentialID))
		if err != nil {
			return err
		}
		if sameCredentialRuntimeEffect(current, state, coolingUntil, event.HTTPStatus, event.ErrorCode) {
			return nil
		}
		status := event.HTTPStatus
		_, err = effects.repository.UpdateCredentialRuntimeState(ctx, vnextstore.CredentialRuntimeUpdate{
			CredentialID: int64(event.CredentialID), ExpectedRevision: current.Revision,
			State: state, CoolingUntil: cloneMillis(coolingUntil), LastHTTPStatus: &status,
			LastErrorCode: event.ErrorCode, UpdatedAt: event.OccurredAt.UTC().UnixMilli(),
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, vnextstore.ErrRevisionConflict) {
			return err
		}
	}
	return vnextstore.ErrRevisionConflict
}

func sameCredentialRuntimeEffect(
	current vnextstore.CredentialRuntimeState,
	state string,
	coolingUntil *int64,
	status int,
	errorCode string,
) bool {
	if current.State != state || current.LastHTTPStatus == nil || *current.LastHTTPStatus != status || current.LastErrorCode != errorCode {
		return false
	}
	if state != "cooling" {
		return current.CoolingUntil == nil
	}
	return current.CoolingUntil != nil && coolingUntil != nil && *current.CoolingUntil >= *coolingUntil
}

func cloneMillis(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

var _ CredentialEffectStore = (*StoreCredentialEffects)(nil)
