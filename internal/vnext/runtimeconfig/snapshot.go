package runtimeconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var ErrRevisionNotNewer = errors.New("runtime configuration revision is not newer than the active revision")

type Loader[T any] interface {
	Load(context.Context, int64) (T, error)
}

type LoaderFunc[T any] func(context.Context, int64) (T, error)

func (function LoaderFunc[T]) Load(ctx context.Context, revision int64) (T, error) {
	return function(ctx, revision)
}

type CloneFunc[T any] func(T) T

type ValidateFunc[T any] func(T) error

type EncodeFunc[T any] func(T) ([]byte, error)

type ManagerOptions[T any] struct {
	Loader       Loader[T]
	Clone        CloneFunc[T]
	Validate     ValidateFunc[T]
	Encode       EncodeFunc[T]
	HistoryLimit int
	Now          func() time.Time
}

type ReloadStatus string

const (
	ReloadIdle      ReloadStatus = "idle"
	ReloadLoading   ReloadStatus = "loading"
	ReloadSucceeded ReloadStatus = "succeeded"
	ReloadFailed    ReloadStatus = "failed"
	ReloadRejected  ReloadStatus = "rejected"
)

type ReloadState struct {
	Status            ReloadStatus
	AttemptedRevision int64
	StartedAt         time.Time
	FinishedAt        time.Time
	Error             string
}

type Transition struct {
	AttemptedRevision int64
	PreviousRevision  int64
	ActiveRevision    int64
	At                time.Time
	Status            ReloadStatus
	Checksum          string
	Error             string
}

type State struct {
	ActiveRevision int64
	BuiltAt        time.Time
	Checksum       string
	Reload         ReloadState
	Transitions    []Transition
}

// RuntimeSnapshot is an immutable published generation. Value returns a deep
// clone, keeping callers from mutating the manager's active generation.
type RuntimeSnapshot[T any] struct {
	Revision int64
	BuiltAt  time.Time
	Checksum string

	value T
	clone CloneFunc[T]
}

func (snapshot RuntimeSnapshot[T]) Value() T {
	if snapshot.clone == nil {
		return snapshot.value
	}
	return snapshot.clone(snapshot.value)
}

type snapshotGeneration[T any] struct {
	revision int64
	builtAt  time.Time
	checksum string
	value    T
}

// Manager completely builds, clones, validates, and checksums a candidate
// before publishing it with one atomic pointer swap. Failed generations never
// disturb the last-known-good active generation.
type Manager[T any] struct {
	loader       Loader[T]
	clone        CloneFunc[T]
	validate     ValidateFunc[T]
	encode       EncodeFunc[T]
	now          func() time.Time
	historyLimit int

	current  atomic.Pointer[snapshotGeneration[T]]
	reloadMu sync.Mutex
	stateMu  sync.RWMutex
	reload   ReloadState
	history  []Transition
}

func NewManager[T any](options ManagerOptions[T]) (*Manager[T], error) {
	if options.Loader == nil || options.Clone == nil || options.Validate == nil {
		return nil, errors.New("runtime configuration loader, clone, and validation functions are required")
	}
	if options.HistoryLimit == 0 {
		options.HistoryLimit = 32
	}
	if options.HistoryLimit < 1 || options.HistoryLimit > 1000 {
		return nil, errors.New("runtime configuration history limit must be between 1 and 1000")
	}
	if options.Encode == nil {
		options.Encode = func(value T) ([]byte, error) { return json.Marshal(value) }
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Manager[T]{
		loader: options.Loader, clone: options.Clone, validate: options.Validate,
		encode: options.Encode, now: options.Now, historyLimit: options.HistoryLimit,
		reload: ReloadState{Status: ReloadIdle}, history: make([]Transition, 0, options.HistoryLimit),
	}, nil
}

func (manager *Manager[T]) Current() (RuntimeSnapshot[T], bool) {
	if manager == nil {
		return RuntimeSnapshot[T]{}, false
	}
	generation := manager.current.Load()
	if generation == nil {
		return RuntimeSnapshot[T]{}, false
	}
	return RuntimeSnapshot[T]{
		Revision: generation.revision, BuiltAt: generation.builtAt, Checksum: generation.checksum,
		value: manager.clone(generation.value), clone: manager.clone,
	}, true
}

func (manager *Manager[T]) State() State {
	if manager == nil {
		return State{Reload: ReloadState{Status: ReloadIdle}}
	}
	result := State{}
	if generation := manager.current.Load(); generation != nil {
		result.ActiveRevision = generation.revision
		result.BuiltAt = generation.builtAt
		result.Checksum = generation.checksum
	}
	manager.stateMu.RLock()
	result.Reload = manager.reload
	result.Transitions = append([]Transition(nil), manager.history...)
	manager.stateMu.RUnlock()
	return result
}

func (manager *Manager[T]) Reload(ctx context.Context, revision int64) (RuntimeSnapshot[T], error) {
	if manager == nil {
		return RuntimeSnapshot[T]{}, errors.New("runtime configuration manager is unavailable")
	}
	if ctx == nil {
		return RuntimeSnapshot[T]{}, errors.New("runtime configuration reload context is required")
	}
	if revision <= 0 {
		return RuntimeSnapshot[T]{}, errors.New("runtime configuration revision must be positive")
	}

	manager.reloadMu.Lock()
	defer manager.reloadMu.Unlock()
	previous := int64(0)
	if active := manager.current.Load(); active != nil {
		previous = active.revision
	}
	startedAt := manager.now().UTC()
	if revision <= previous {
		err := fmt.Errorf("%w: attempted %d, active %d", ErrRevisionNotNewer, revision, previous)
		manager.finishReload(ReloadState{
			Status: ReloadRejected, AttemptedRevision: revision, StartedAt: startedAt,
			FinishedAt: startedAt, Error: err.Error(),
		}, Transition{
			AttemptedRevision: revision, PreviousRevision: previous, ActiveRevision: previous,
			At: startedAt, Status: ReloadRejected, Error: err.Error(),
		})
		return RuntimeSnapshot[T]{}, err
	}
	manager.setReload(ReloadState{Status: ReloadLoading, AttemptedRevision: revision, StartedAt: startedAt})

	candidate, err := manager.loader.Load(ctx, revision)
	if err == nil {
		candidate = manager.clone(candidate)
		err = manager.validate(candidate)
	}
	var checksum string
	if err == nil {
		var encoded []byte
		encoded, err = manager.encode(candidate)
		if err == nil {
			digest := sha256.Sum256(encoded)
			checksum = hex.EncodeToString(digest[:])
		}
	}
	finishedAt := manager.now().UTC()
	if err != nil {
		wrapped := fmt.Errorf("build runtime configuration revision %d: %w", revision, err)
		manager.finishReload(ReloadState{
			Status: ReloadFailed, AttemptedRevision: revision, StartedAt: startedAt,
			FinishedAt: finishedAt, Error: wrapped.Error(),
		}, Transition{
			AttemptedRevision: revision, PreviousRevision: previous, ActiveRevision: previous,
			At: finishedAt, Status: ReloadFailed, Error: wrapped.Error(),
		})
		return RuntimeSnapshot[T]{}, wrapped
	}

	generation := &snapshotGeneration[T]{
		revision: revision, builtAt: finishedAt, checksum: checksum, value: candidate,
	}
	manager.current.Store(generation)
	manager.finishReload(ReloadState{
		Status: ReloadSucceeded, AttemptedRevision: revision, StartedAt: startedAt, FinishedAt: finishedAt,
	}, Transition{
		AttemptedRevision: revision, PreviousRevision: previous, ActiveRevision: revision,
		At: finishedAt, Status: ReloadSucceeded, Checksum: checksum,
	})
	return RuntimeSnapshot[T]{
		Revision: revision, BuiltAt: finishedAt, Checksum: checksum,
		value: manager.clone(candidate), clone: manager.clone,
	}, nil
}

func (manager *Manager[T]) setReload(state ReloadState) {
	manager.stateMu.Lock()
	manager.reload = state
	manager.stateMu.Unlock()
}

func (manager *Manager[T]) finishReload(state ReloadState, transition Transition) {
	manager.stateMu.Lock()
	manager.reload = state
	manager.history = append(manager.history, transition)
	if overflow := len(manager.history) - manager.historyLimit; overflow > 0 {
		copy(manager.history, manager.history[overflow:])
		manager.history = manager.history[:manager.historyLimit]
	}
	manager.stateMu.Unlock()
}
