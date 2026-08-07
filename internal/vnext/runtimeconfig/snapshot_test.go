package runtimeconfig

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type testConfig struct {
	Left   int               `json:"left"`
	Right  int               `json:"right"`
	Labels map[string]string `json:"labels"`
}

func cloneTestConfig(input testConfig) testConfig {
	copy := input
	copy.Labels = make(map[string]string, len(input.Labels))
	for key, value := range input.Labels {
		copy.Labels[key] = value
	}
	return copy
}

func validateTestConfig(input testConfig) error {
	if input.Left <= 0 || input.Left != input.Right {
		return errors.New("configuration generation is inconsistent")
	}
	if input.Labels["generation"] != fmt.Sprint(input.Left) {
		return errors.New("configuration label is inconsistent")
	}
	return nil
}

func TestManagerKeepsLastKnownGoodOnInvalidSnapshot(t *testing.T) {
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(ManagerOptions[testConfig]{
		Loader: LoaderFunc[testConfig](func(_ context.Context, revision int64) (testConfig, error) {
			return testConfig{Left: int(revision), Right: 1, Labels: map[string]string{"generation": fmt.Sprint(revision)}}, nil
		}),
		Clone: cloneTestConfig, Validate: validateTestConfig, Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Reload(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || len(first.Checksum) != 64 {
		t.Fatalf("initial snapshot = %+v", first)
	}
	if _, err := manager.Reload(context.Background(), 2); err == nil {
		t.Fatal("invalid revision was published")
	}
	active, ok := manager.Current()
	if !ok || active.Revision != 1 || active.Checksum != first.Checksum || active.BuiltAt != first.BuiltAt {
		t.Fatalf("last-known-good snapshot changed: %+v", active)
	}
	state := manager.State()
	if state.ActiveRevision != 1 || state.Reload.Status != ReloadFailed || state.Reload.AttemptedRevision != 2 ||
		state.Reload.Error == "" || len(state.Transitions) != 2 || state.Transitions[1].ActiveRevision != 1 {
		t.Fatalf("reload state = %+v", state)
	}
}

func TestManagerConcurrentReadersObserveWholeImmutableGenerations(t *testing.T) {
	manager, err := NewManager(ManagerOptions[testConfig]{
		Loader: LoaderFunc[testConfig](func(_ context.Context, revision int64) (testConfig, error) {
			value := int(revision)
			return testConfig{Left: value, Right: value, Labels: map[string]string{"generation": fmt.Sprint(value)}}, nil
		}),
		Clone: cloneTestConfig, Validate: validateTestConfig, HistoryLimit: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reload(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for index := 0; index < 16; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snapshot, ok := manager.Current()
				if !ok {
					t.Error("active snapshot disappeared")
					return
				}
				value := snapshot.Value()
				if err := validateTestConfig(value); err != nil || int64(value.Left) != snapshot.Revision {
					t.Errorf("observed partial generation %d: %+v, %v", snapshot.Revision, value, err)
					return
				}
				value.Labels["generation"] = "mutated"
				fresh, _ := manager.Current()
				if fresh.Value().Labels["generation"] == "mutated" {
					t.Error("caller mutated the active generation")
					return
				}
			}
		}()
	}
	for revision := int64(2); revision <= 100; revision++ {
		if _, err := manager.Reload(context.Background(), revision); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	state := manager.State()
	if state.ActiveRevision != 100 || len(state.Transitions) != 8 || state.Transitions[0].ActiveRevision != 93 {
		t.Fatalf("bounded transition history = %+v", state)
	}
}

func TestManagerRejectsNonMonotonicRevision(t *testing.T) {
	manager, err := NewManager(ManagerOptions[int]{
		Loader: LoaderFunc[int](func(_ context.Context, revision int64) (int, error) { return int(revision), nil }),
		Clone:  func(value int) int { return value }, Validate: func(value int) error {
			if value <= 0 {
				return errors.New("value must be positive")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reload(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reload(context.Background(), 5); !errors.Is(err, ErrRevisionNotNewer) {
		t.Fatalf("duplicate revision error = %v", err)
	}
	if _, err := manager.Reload(context.Background(), 4); !errors.Is(err, ErrRevisionNotNewer) {
		t.Fatalf("older revision error = %v", err)
	}
	active, _ := manager.Current()
	if active.Revision != 5 || manager.State().Reload.Status != ReloadRejected {
		t.Fatalf("active snapshot after stale reload = %+v, state = %+v", active, manager.State())
	}
}
