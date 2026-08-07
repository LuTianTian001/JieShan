package routing

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

type TargetID int64
type CredentialID int64

type CredentialState string

const (
	CredentialReady     CredentialState = "ready"
	CredentialCooling   CredentialState = "cooling"
	CredentialInvalid   CredentialState = "invalid"
	CredentialExhausted CredentialState = "exhausted"
)

type Credential struct {
	ID            CredentialID
	Position      int
	Enabled       bool
	State         CredentialState
	CooldownUntil time.Time
}

func (credential Credential) eligible(now time.Time) bool {
	if !credential.Enabled {
		return false
	}
	switch credential.State {
	case "", CredentialReady:
		return true
	case CredentialCooling:
		return !credential.CooldownUntil.After(now)
	case CredentialInvalid, CredentialExhausted:
		return false
	default:
		return false
	}
}

type Target struct {
	ID          TargetID
	Revision    Revision
	Position    int
	Enabled     bool
	Credentials []Credential
}

type Plan struct {
	targets []Target
}

// CompilePlan rejects ambiguous positions. The compiled plan is ordered only
// by the positions supplied by the user; health never re-ranks a target.
func CompilePlan(targets []Target) (Plan, error) {
	ordered := append([]Target(nil), targets...)
	seenTargetIDs := make(map[TargetID]struct{}, len(ordered))
	seenTargetPositions := make(map[int]struct{}, len(ordered))
	for index := range ordered {
		target := &ordered[index]
		if target.ID <= 0 {
			return Plan{}, errors.New("routing: target ID must be positive")
		}
		if target.Revision == 0 {
			return Plan{}, fmt.Errorf("routing: target %d revision must be positive", target.ID)
		}
		if target.Position < 0 {
			return Plan{}, fmt.Errorf("routing: target %d position must not be negative", target.ID)
		}
		if _, exists := seenTargetIDs[target.ID]; exists {
			return Plan{}, fmt.Errorf("routing: duplicate target ID %d", target.ID)
		}
		seenTargetIDs[target.ID] = struct{}{}
		if _, exists := seenTargetPositions[target.Position]; exists {
			return Plan{}, fmt.Errorf("routing: duplicate target position %d", target.Position)
		}
		seenTargetPositions[target.Position] = struct{}{}

		credentials := append([]Credential(nil), target.Credentials...)
		seenCredentialIDs := make(map[CredentialID]struct{}, len(credentials))
		seenCredentialPositions := make(map[int]struct{}, len(credentials))
		for _, credential := range credentials {
			if credential.ID <= 0 {
				return Plan{}, fmt.Errorf("routing: target %d credential ID must be positive", target.ID)
			}
			if credential.Position < 0 {
				return Plan{}, fmt.Errorf("routing: credential %d position must not be negative", credential.ID)
			}
			if _, exists := seenCredentialIDs[credential.ID]; exists {
				return Plan{}, fmt.Errorf("routing: target %d has duplicate credential ID %d", target.ID, credential.ID)
			}
			seenCredentialIDs[credential.ID] = struct{}{}
			if _, exists := seenCredentialPositions[credential.Position]; exists {
				return Plan{}, fmt.Errorf("routing: target %d has duplicate credential position %d", target.ID, credential.Position)
			}
			seenCredentialPositions[credential.Position] = struct{}{}
		}
		sort.Slice(credentials, func(left, right int) bool {
			return credentials[left].Position < credentials[right].Position
		})
		target.Credentials = credentials
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].Position < ordered[right].Position
	})
	return Plan{targets: ordered}, nil
}

func (plan Plan) Targets() []Target {
	result := make([]Target, len(plan.targets))
	for index, target := range plan.targets {
		result[index] = target
		result[index].Credentials = append([]Credential(nil), target.Credentials...)
	}
	return result
}

type Candidate struct {
	Target     Target
	Credential Credential
	PermitMode PermitMode
}

type Cursor struct {
	targets []Target
	health  map[TargetID]HealthState
	now     time.Time

	targetIndex     int
	credentialIndex int
	current         Candidate
	hasCurrent      bool
	stopped         bool
}

func (plan Plan) NewCursor(health map[TargetID]HealthState, now time.Time) *Cursor {
	healthCopy := make(map[TargetID]HealthState, len(health))
	for targetID, state := range health {
		healthCopy[targetID] = state
	}
	return &Cursor{
		targets:         append([]Target(nil), plan.targets...),
		health:          healthCopy,
		now:             now,
		targetIndex:     -1,
		credentialIndex: -1,
	}
}

func (cursor *Cursor) First() (Candidate, bool) {
	if cursor.stopped || cursor.hasCurrent {
		return cursor.current, cursor.hasCurrent
	}
	return cursor.moveToTarget(0)
}

// RemainingTargets returns the current eligible target followed by later
// eligible targets in strict plan order. Each target appears once, with the
// credential the cursor would use if that target were selected now.
func (cursor *Cursor) RemainingTargets() []Candidate {
	if cursor == nil || cursor.stopped || !cursor.hasCurrent {
		return nil
	}
	probe := *cursor
	result := make([]Candidate, 0, len(probe.targets)-probe.targetIndex)
	result = append(result, probe.current)
	for {
		candidate, ok := probe.SkipTarget()
		if !ok {
			return result
		}
		result = append(result, candidate)
	}
}

// Advance applies only request-level retry semantics. Target health mutation
// is deliberately separate and only applies when Disposition penalizes the
// target.
func (cursor *Cursor) Advance(failure Failure) (Candidate, bool) {
	if cursor.stopped || !cursor.hasCurrent {
		return Candidate{}, false
	}
	switch failure.Disposition().Retry {
	case RetryNextCredential:
		if candidate, ok := cursor.moveToCredential(cursor.credentialIndex + 1); ok {
			return candidate, true
		}
		return cursor.moveToTarget(cursor.targetIndex + 1)
	case RetryNextTarget:
		return cursor.moveToTarget(cursor.targetIndex + 1)
	case RetryStop:
		cursor.Stop()
		return Candidate{}, false
	default:
		cursor.Stop()
		return Candidate{}, false
	}
}

// SkipTarget is used when atomic half-open permit acquisition loses a race.
func (cursor *Cursor) SkipTarget() (Candidate, bool) {
	if cursor.stopped || !cursor.hasCurrent {
		return Candidate{}, false
	}
	return cursor.moveToTarget(cursor.targetIndex + 1)
}

func (cursor *Cursor) Stop() {
	cursor.stopped = true
	cursor.hasCurrent = false
	cursor.current = Candidate{}
}

func (cursor *Cursor) moveToTarget(start int) (Candidate, bool) {
	for targetIndex := start; targetIndex < len(cursor.targets); targetIndex++ {
		target := cursor.targets[targetIndex]
		if !target.Enabled {
			continue
		}
		eligibility := EvaluateTarget(cursor.health[target.ID], target.Revision, cursor.now)
		if !eligibility.Eligible {
			continue
		}
		cursor.targetIndex = targetIndex
		cursor.credentialIndex = -1
		candidate, ok := cursor.moveToCredential(0)
		if ok {
			candidate.PermitMode = eligibility.Mode
			cursor.current = candidate
			return candidate, true
		}
	}
	cursor.Stop()
	return Candidate{}, false
}

func (cursor *Cursor) moveToCredential(start int) (Candidate, bool) {
	if cursor.targetIndex < 0 || cursor.targetIndex >= len(cursor.targets) {
		return Candidate{}, false
	}
	target := cursor.targets[cursor.targetIndex]
	for credentialIndex := start; credentialIndex < len(target.Credentials); credentialIndex++ {
		credential := target.Credentials[credentialIndex]
		if !credential.eligible(cursor.now) {
			continue
		}
		cursor.credentialIndex = credentialIndex
		candidate := Candidate{Target: target, Credential: credential, PermitMode: cursor.current.PermitMode}
		if candidate.PermitMode == "" {
			candidate.PermitMode = EvaluateTarget(cursor.health[target.ID], target.Revision, cursor.now).Mode
		}
		cursor.current = candidate
		cursor.hasCurrent = true
		return candidate, true
	}
	return Candidate{}, false
}
