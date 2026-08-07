package runtimeconfig

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryRevisionRepository struct {
	events []RevisionEvent
}

func (repository *memoryRevisionRepository) LatestConfigRevision(context.Context) (RevisionEvent, error) {
	if len(repository.events) == 0 {
		return RevisionEvent{}, errors.New("no revisions")
	}
	return repository.events[len(repository.events)-1], nil
}

func (repository *memoryRevisionRepository) PollConfigRevisions(
	_ context.Context,
	after RevisionCursor,
	limit int,
) ([]RevisionEvent, error) {
	result := make([]RevisionEvent, 0, limit)
	for _, event := range repository.events {
		if event.Cursor > after && len(result) < limit {
			result = append(result, event)
		}
	}
	return result, nil
}

func (repository *memoryRevisionRepository) EnqueueConfigRevision(
	_ context.Context,
	reason string,
	createdAt time.Time,
) (RevisionEvent, error) {
	revision := int64(len(repository.events) + 1)
	event := RevisionEvent{
		Cursor: RevisionCursor(revision), Revision: revision, Reason: reason, CreatedAt: createdAt,
	}
	repository.events = append(repository.events, event)
	return event, nil
}

func TestPollerRetriesUnacknowledgedRevision(t *testing.T) {
	repository := &memoryRevisionRepository{events: []RevisionEvent{
		{Cursor: 2, Revision: 2, Reason: "first", CreatedAt: time.Unix(1, 0)},
		{Cursor: 3, Revision: 3, Reason: "second", CreatedAt: time.Unix(2, 0)},
	}}
	poller, err := NewPoller(PollerOptions{Repository: repository, Cursor: 1})
	if err != nil {
		t.Fatal(err)
	}
	wantFailure := errors.New("reload failed")
	processed, err := poller.Poll(context.Background(), func(_ context.Context, event RevisionEvent) error {
		if event.Revision == 3 {
			return wantFailure
		}
		return nil
	})
	if processed != 1 || !errors.Is(err, wantFailure) || poller.Cursor() != 2 {
		t.Fatalf("failed poll = processed %d cursor %d error %v", processed, poller.Cursor(), err)
	}
	var retried []int64
	processed, err = poller.Poll(context.Background(), func(_ context.Context, event RevisionEvent) error {
		retried = append(retried, event.Revision)
		return nil
	})
	if err != nil || processed != 1 || len(retried) != 1 || retried[0] != 3 || poller.Cursor() != 3 {
		t.Fatalf("retry poll = processed %d revisions %v cursor %d error %v", processed, retried, poller.Cursor(), err)
	}
}
