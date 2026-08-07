package capacity

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Manager struct {
	mu     sync.Mutex
	config Config
	sites  map[SiteID]*siteState

	queues     map[KeyID]*keyQueue
	keyOrder   []KeyID
	nextKey    int
	queued     int
	nextWaiter uint64
	closed     bool
}

type siteState struct {
	configured     bool
	maxInFlight    int
	inFlight       int
	throttledUntil time.Time
	throttleTimer  *time.Timer
}

type keyQueue struct {
	waiters []*waiter
}

type waiter struct {
	id         uint64
	keyID      KeyID
	candidates []Candidate
	enqueuedAt time.Time
	overflowAt time.Time
	result     chan admissionResult
	queued     bool
}

type admissionResult struct {
	permit *Permit
	err    error
}

func New(config Config) (*Manager, error) {
	if config.MaxQueued < 0 {
		return nil, errors.New("capacity: maximum queued requests must not be negative")
	}
	if config.MaxQueued > 0 && config.QueueTimeout <= 0 {
		return nil, errors.New("capacity: a positive queue timeout is required when queuing is enabled")
	}
	if grace := config.PreferredTargetGrace; grace != 0 &&
		(grace < MinPreferredTargetGrace || grace > MaxPreferredTargetGrace) {
		return nil, fmt.Errorf("capacity: preferred target grace must be zero or between %s and %s", MinPreferredTargetGrace, MaxPreferredTargetGrace)
	}
	return &Manager{
		config: config,
		sites:  make(map[SiteID]*siteState),
		queues: make(map[KeyID]*keyQueue),
	}, nil
}

// ReplaceSites atomically installs the complete configured Site limit set.
// Lowering a limit below current in-flight usage stops new grants until the
// existing work drains. Removed Sites likewise drain existing permits without
// accepting new work.
func (manager *Manager) ReplaceSites(configs []SiteConfig) error {
	if manager == nil {
		return ErrClosed
	}
	validated := make(map[SiteID]int, len(configs))
	for _, config := range configs {
		if config.SiteID <= 0 {
			return errors.New("capacity: Site ID must be positive")
		}
		if config.MaxInFlight <= 0 {
			return fmt.Errorf("capacity: Site %d maximum in-flight must be positive", config.SiteID)
		}
		if _, exists := validated[config.SiteID]; exists {
			return fmt.Errorf("capacity: duplicate Site ID %d", config.SiteID)
		}
		validated[config.SiteID] = config.MaxInFlight
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	for _, state := range manager.sites {
		state.configured = false
	}
	for siteID, maximum := range validated {
		state := manager.sites[siteID]
		if state == nil {
			state = &siteState{}
			manager.sites[siteID] = state
		}
		state.configured = true
		state.maxInFlight = maximum
	}
	manager.scheduleLocked(time.Now().UTC())
	manager.pruneSitesLocked()
	return nil
}

func (manager *Manager) Acquire(ctx context.Context, request Request) (*Permit, error) {
	if manager == nil {
		return nil, ErrClosed
	}
	if ctx == nil {
		return nil, errors.New("capacity: request context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	candidates, err := validateRequest(request)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil, ErrClosed
	}
	configured, missingSite := manager.hasConfiguredCandidateLocked(candidates)
	if !configured {
		manager.mu.Unlock()
		return nil, &SiteConfigError{SiteID: missingSite}
	}

	// Give already queued Keys the first chance at any capacity that became
	// available since the last scheduling edge. Remaining queued work must not
	// globally block an unrelated request whose own preferred Site is idle.
	if manager.queued > 0 {
		manager.scheduleLocked(now)
	}
	if candidate, index, ok := manager.immediateCandidateLocked(candidates, now); ok {
		permit := manager.grantLocked(candidate, index > 0, now, now)
		manager.mu.Unlock()
		return permit, nil
	}
	if manager.config.MaxQueued == 0 || manager.queued >= manager.config.MaxQueued {
		manager.mu.Unlock()
		return nil, &BusyError{Reason: BusyQueueFull}
	}

	manager.nextWaiter++
	queued := &waiter{
		id: manager.nextWaiter, keyID: request.KeyID, candidates: candidates,
		enqueuedAt: now, result: make(chan admissionResult, 1), queued: true,
	}
	if manager.config.PreferredTargetGrace > 0 && manager.isSaturatedLocked(candidates[0], now) {
		queued.overflowAt = now.Add(manager.config.PreferredTargetGrace)
	}
	manager.enqueueLocked(queued)
	manager.scheduleLocked(now)
	manager.mu.Unlock()

	return manager.wait(ctx, queued)
}

func validateRequest(request Request) ([]Candidate, error) {
	if request.KeyID <= 0 {
		return nil, errors.New("capacity: downstream Key ID must be positive")
	}
	if len(request.Candidates) == 0 {
		return nil, errors.New("capacity: at least one route candidate is required")
	}
	candidates := append([]Candidate(nil), request.Candidates...)
	seenTargets := make(map[TargetID]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.TargetID <= 0 || candidate.SiteID <= 0 {
			return nil, errors.New("capacity: candidate target and Site IDs must be positive")
		}
		if _, exists := seenTargets[candidate.TargetID]; exists {
			return nil, fmt.Errorf("capacity: duplicate target ID %d", candidate.TargetID)
		}
		seenTargets[candidate.TargetID] = struct{}{}
	}
	return candidates, nil
}

func (manager *Manager) immediateCandidateLocked(candidates []Candidate, now time.Time) (Candidate, int, bool) {
	if manager.config.PreferredTargetGrace > 0 && manager.isSaturatedLocked(candidates[0], now) {
		return Candidate{}, 0, false
	}
	return manager.availableCandidateLocked(candidates, now)
}

func (manager *Manager) availableCandidateLocked(candidates []Candidate, now time.Time) (Candidate, int, bool) {
	for index, candidate := range candidates {
		state := manager.sites[candidate.SiteID]
		if state == nil || !state.configured || state.throttledUntil.After(now) || state.inFlight >= state.maxInFlight {
			continue
		}
		return candidate, index, true
	}
	return Candidate{}, 0, false
}

func (manager *Manager) isSaturatedLocked(candidate Candidate, now time.Time) bool {
	state := manager.sites[candidate.SiteID]
	return state != nil && state.configured && !state.throttledUntil.After(now) && state.inFlight >= state.maxInFlight
}

func (manager *Manager) wait(ctx context.Context, queued *waiter) (*Permit, error) {
	timeout := time.NewTimer(manager.config.QueueTimeout)
	defer stopTimer(timeout)

	var grace *time.Timer
	var graceC <-chan time.Time
	if !queued.overflowAt.IsZero() {
		grace = time.NewTimer(time.Until(queued.overflowAt))
		graceC = grace.C
		defer stopTimer(grace)
	}

	for {
		select {
		case result := <-queued.result:
			return result.permit, result.err
		case <-ctx.Done():
			if manager.removeWaiter(queued) {
				return nil, context.Cause(ctx)
			}
			result := <-queued.result
			if result.permit != nil {
				result.permit.Release()
			}
			return nil, context.Cause(ctx)
		case <-graceC:
			graceC = nil
			manager.reschedule()
		case <-timeout.C:
			if manager.removeWaiter(queued) {
				return nil, &BusyError{Reason: BusyQueueTimeout, QueuedFor: time.Since(queued.enqueuedAt)}
			}
			result := <-queued.result
			return result.permit, result.err
		}
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (manager *Manager) enqueueLocked(queued *waiter) {
	queue := manager.queues[queued.keyID]
	if queue == nil {
		queue = &keyQueue{}
		manager.queues[queued.keyID] = queue
		manager.keyOrder = append(manager.keyOrder, queued.keyID)
	}
	queue.waiters = append(queue.waiters, queued)
	manager.queued++
}

// scheduleLocked grants at most one head request per Key before revisiting a
// Key. Blocked Keys are skipped so unrelated Site capacity does not sit idle.
func (manager *Manager) scheduleLocked(now time.Time) {
	for !manager.closed && len(manager.keyOrder) > 0 {
		progress := false
		keysToVisit := len(manager.keyOrder)
		for keysToVisit > 0 && len(manager.keyOrder) > 0 {
			if manager.nextKey >= len(manager.keyOrder) {
				manager.nextKey = 0
			}
			keyIndex := manager.nextKey
			keyID := manager.keyOrder[keyIndex]
			queue := manager.queues[keyID]
			if queue == nil || len(queue.waiters) == 0 {
				manager.removeKeyLocked(keyIndex)
				progress = true
				break
			}
			queued := queue.waiters[0]
			configured, missing := manager.hasConfiguredCandidateLocked(queued.candidates)
			if !configured {
				manager.removeHeadLocked(keyIndex)
				queued.result <- admissionResult{err: &SiteConfigError{SiteID: missing}}
				progress = true
				break
			}
			candidates := queued.candidates
			if !queued.overflowAt.IsZero() && now.Before(queued.overflowAt) &&
				manager.isSaturatedLocked(candidates[0], now) {
				candidates = candidates[:1]
			}
			candidate, routeIndex, available := manager.availableCandidateLocked(candidates, now)
			if available {
				manager.removeHeadLocked(keyIndex)
				queued.result <- admissionResult{permit: manager.grantLocked(candidate, routeIndex > 0, now, queued.enqueuedAt)}
				progress = true
				break
			}
			manager.nextKey = (keyIndex + 1) % len(manager.keyOrder)
			keysToVisit--
		}
		if !progress {
			return
		}
	}
}

func (manager *Manager) hasConfiguredCandidateLocked(candidates []Candidate) (bool, SiteID) {
	var firstMissing SiteID
	for _, candidate := range candidates {
		state := manager.sites[candidate.SiteID]
		if state != nil && state.configured {
			return true, firstMissing
		}
		if firstMissing == 0 {
			firstMissing = candidate.SiteID
		}
	}
	return false, firstMissing
}

func (manager *Manager) grantLocked(candidate Candidate, overflowed bool, now, enqueuedAt time.Time) *Permit {
	state := manager.sites[candidate.SiteID]
	state.inFlight++
	queuedFor := time.Duration(0)
	if !enqueuedAt.IsZero() && now.After(enqueuedAt) {
		queuedFor = now.Sub(enqueuedAt)
	}
	return &Permit{
		SiteID: candidate.SiteID, TargetID: candidate.TargetID, AcquiredAt: now,
		QueuedFor: queuedFor, Overflowed: overflowed, manager: manager, released: make(chan struct{}),
	}
}

func (manager *Manager) removeHeadLocked(keyIndex int) {
	keyID := manager.keyOrder[keyIndex]
	queue := manager.queues[keyID]
	queued := queue.waiters[0]
	queued.queued = false
	queue.waiters = queue.waiters[1:]
	manager.queued--
	if len(queue.waiters) == 0 {
		manager.removeKeyLocked(keyIndex)
		return
	}
	manager.nextKey = (keyIndex + 1) % len(manager.keyOrder)
}

func (manager *Manager) removeKeyLocked(index int) {
	keyID := manager.keyOrder[index]
	delete(manager.queues, keyID)
	manager.keyOrder = append(manager.keyOrder[:index], manager.keyOrder[index+1:]...)
	if len(manager.keyOrder) == 0 {
		manager.nextKey = 0
		return
	}
	manager.nextKey = index % len(manager.keyOrder)
}

func (manager *Manager) removeWaiter(queued *waiter) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !queued.queued {
		return false
	}
	queue := manager.queues[queued.keyID]
	if queue == nil {
		return false
	}
	for index, candidate := range queue.waiters {
		if candidate != queued {
			continue
		}
		queue.waiters = append(queue.waiters[:index], queue.waiters[index+1:]...)
		queued.queued = false
		manager.queued--
		if len(queue.waiters) == 0 {
			for keyIndex, keyID := range manager.keyOrder {
				if keyID == queued.keyID {
					manager.removeKeyLocked(keyIndex)
					break
				}
			}
		}
		manager.scheduleLocked(time.Now().UTC())
		return true
	}
	return false
}

func (manager *Manager) reschedule() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.scheduleLocked(time.Now().UTC())
}

func (manager *Manager) release(siteID SiteID) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.sites[siteID]
	if state == nil || state.inFlight == 0 {
		return
	}
	state.inFlight--
	manager.scheduleLocked(time.Now().UTC())
	manager.pruneSitesLocked()
}

func (manager *Manager) ReportThrottle(signal ThrottleSignal) error {
	if manager == nil {
		return ErrClosed
	}
	if signal.SiteID <= 0 {
		return errors.New("capacity: throttle Site ID must be positive")
	}
	observedAt := signal.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	until := signal.Until.UTC()
	if signal.RetryAfter > 0 {
		retryUntil := observedAt.Add(signal.RetryAfter)
		if until.IsZero() || retryUntil.After(until) {
			until = retryUntil
		}
	}
	if until.IsZero() || !until.After(observedAt) {
		return errors.New("capacity: throttle deadline or positive Retry-After is required")
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	state := manager.sites[signal.SiteID]
	if state == nil || !state.configured {
		return &SiteConfigError{SiteID: signal.SiteID}
	}
	// Concurrent 429 observations may extend a throttle but never shorten a
	// later deadline reported by another request.
	if until.After(state.throttledUntil) {
		state.throttledUntil = until
		manager.armThrottleTimerLocked(signal.SiteID, state, until)
	}
	manager.scheduleLocked(observedAt)
	return nil
}

func (manager *Manager) ClearThrottle(siteID SiteID) error {
	if manager == nil {
		return ErrClosed
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	state := manager.sites[siteID]
	if state == nil || !state.configured {
		return &SiteConfigError{SiteID: siteID}
	}
	if state.throttleTimer != nil {
		state.throttleTimer.Stop()
		state.throttleTimer = nil
	}
	state.throttledUntil = time.Time{}
	manager.scheduleLocked(time.Now().UTC())
	return nil
}

func (manager *Manager) armThrottleTimerLocked(siteID SiteID, state *siteState, deadline time.Time) {
	if state.throttleTimer != nil {
		state.throttleTimer.Stop()
	}
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	state.throttleTimer = time.AfterFunc(delay, func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		current := manager.sites[siteID]
		if manager.closed || current != state || !current.throttledUntil.Equal(deadline) {
			return
		}
		current.throttleTimer = nil
		current.throttledUntil = time.Time{}
		manager.scheduleLocked(time.Now().UTC())
		manager.pruneSitesLocked()
	})
}

func (manager *Manager) Snapshot() Snapshot {
	if manager == nil {
		return Snapshot{}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := time.Now().UTC()
	queuedBySite := make(map[SiteID]int)
	for _, queue := range manager.queues {
		for _, queued := range queue.waiters {
			seen := make(map[SiteID]struct{}, len(queued.candidates))
			for _, candidate := range queued.candidates {
				if _, exists := seen[candidate.SiteID]; exists {
					continue
				}
				seen[candidate.SiteID] = struct{}{}
				queuedBySite[candidate.SiteID]++
			}
		}
	}
	snapshot := Snapshot{UpdatedAt: now.UnixMilli(), Queued: manager.queued, Sites: make([]SiteSnapshot, 0, len(manager.sites))}
	for siteID, state := range manager.sites {
		if !state.configured && state.inFlight == 0 {
			continue
		}
		throttledUntil := state.throttledUntil
		if !throttledUntil.After(now) {
			throttledUntil = time.Time{}
		}
		snapshot.Sites = append(snapshot.Sites, SiteSnapshot{
			SiteID: siteID, InFlight: state.inFlight, MaxInFlight: state.maxInFlight,
			Queued: queuedBySite[siteID], ThrottledUntil: throttledUntil,
		})
	}
	sort.Slice(snapshot.Sites, func(left, right int) bool {
		return snapshot.Sites[left].SiteID < snapshot.Sites[right].SiteID
	})
	return snapshot
}

func (manager *Manager) pruneSitesLocked() {
	for siteID, state := range manager.sites {
		if state.configured || state.inFlight > 0 || state.throttledUntil.After(time.Now().UTC()) {
			continue
		}
		if state.throttleTimer != nil {
			state.throttleTimer.Stop()
		}
		delete(manager.sites, siteID)
	}
}

func (manager *Manager) Close() error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil
	}
	manager.closed = true
	for _, state := range manager.sites {
		if state.throttleTimer != nil {
			state.throttleTimer.Stop()
			state.throttleTimer = nil
		}
	}
	for _, queue := range manager.queues {
		for _, queued := range queue.waiters {
			queued.queued = false
			queued.result <- admissionResult{err: ErrClosed}
		}
	}
	manager.queues = make(map[KeyID]*keyQueue)
	manager.keyOrder = nil
	manager.nextKey = 0
	manager.queued = 0
	return nil
}
