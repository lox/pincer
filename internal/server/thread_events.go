package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type threadEvent struct {
	event *protocolv1.ThreadEvent
}

func (a *App) appendThreadEvent(ctx context.Context, event *protocolv1.ThreadEvent) (*protocolv1.ThreadEvent, error) {
	if event == nil {
		return nil, errors.New("event is required")
	}
	if event.GetThreadId() == "" {
		return nil, errors.New("thread_id is required")
	}

	copyEvent, ok := proto.Clone(event).(*protocolv1.ThreadEvent)
	if !ok {
		return nil, errors.New("failed to clone event")
	}
	if copyEvent.EventId == "" {
		copyEvent.EventId = newID("evt")
	}
	if copyEvent.OccurredAt == nil {
		copyEvent.OccurredAt = timestamppb.Now()
	}

	a.eventAppendMu.Lock()
	defer a.eventAppendMu.Unlock()

	nextSeq, err := a.nextThreadSequence(ctx, copyEvent.ThreadId)
	if err != nil {
		return nil, err
	}
	copyEvent.Sequence = nextSeq

	blob, err := proto.Marshal(copyEvent)
	if err != nil {
		return nil, fmt.Errorf("marshal thread event: %w", err)
	}

	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO thread_events(event_id, thread_id, job_id, turn_id, sequence, occurred_at, event_blob)
		VALUES(?, ?, ?, ?, ?, ?, ?)
	`, copyEvent.EventId, copyEvent.ThreadId, copyEvent.JobId, copyEvent.TurnId, copyEvent.Sequence, copyEvent.OccurredAt.AsTime().UTC().Format(time.RFC3339Nano), blob); err != nil {
		return nil, fmt.Errorf("insert thread event: %w", err)
	}

	a.publishThreadEvent(copyEvent)
	return copyEvent, nil
}

func (a *App) nextThreadSequence(ctx context.Context, threadID string) (uint64, error) {
	var next uint64
	err := a.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1
		FROM thread_events
		WHERE thread_id = ?
	`, threadID).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("query next thread sequence: %w", err)
	}
	return next, nil
}

func (a *App) maxThreadSequence(ctx context.Context, threadID string) (uint64, error) {
	var max sql.NullInt64
	err := a.db.QueryRowContext(ctx, `
		SELECT MAX(sequence)
		FROM thread_events
		WHERE thread_id = ?
	`, threadID).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("query max thread sequence: %w", err)
	}
	if !max.Valid || max.Int64 < 0 {
		return 0, nil
	}
	return uint64(max.Int64), nil
}

func (a *App) listThreadEvents(ctx context.Context, threadID string, fromSequence uint64, limit int) ([]*protocolv1.ThreadEvent, error) {
	if limit <= 0 {
		limit = 500
	}

	rows, err := a.db.QueryContext(ctx, `
		SELECT event_blob
		FROM thread_events
		WHERE thread_id = ? AND sequence > ?
		ORDER BY sequence ASC
		LIMIT ?
	`, threadID, fromSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("query thread events: %w", err)
	}
	defer rows.Close()

	events := make([]*protocolv1.ThreadEvent, 0)
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("scan thread event: %w", err)
		}

		var event protocolv1.ThreadEvent
		if err := proto.Unmarshal(blob, &event); err != nil {
			return nil, fmt.Errorf("unmarshal thread event: %w", err)
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate thread events: %w", err)
	}
	return events, nil
}

func (a *App) subscribeThread(threadID string) chan *threadEvent {
	ch := make(chan *threadEvent, 128)
	a.eventSubsMu.Lock()
	defer a.eventSubsMu.Unlock()

	subs, ok := a.eventSubs[threadID]
	if !ok {
		subs = make(map[chan *threadEvent]struct{})
		a.eventSubs[threadID] = subs
	}
	subs[ch] = struct{}{}
	return ch
}

func (a *App) unsubscribeThread(threadID string, ch chan *threadEvent) {
	a.eventSubsMu.Lock()
	defer a.eventSubsMu.Unlock()

	subs, ok := a.eventSubs[threadID]
	if !ok {
		return
	}
	if _, exists := subs[ch]; !exists {
		return
	}
	delete(subs, ch)
	close(ch)
	if len(subs) == 0 {
		delete(a.eventSubs, threadID)
	}
}

func (a *App) publishThreadEvent(event *protocolv1.ThreadEvent) {
	if event == nil {
		return
	}

	a.eventSubsMu.RLock()
	subs := a.eventSubs[event.GetThreadId()]
	if len(subs) == 0 {
		a.eventSubsMu.RUnlock()
		return
	}
	channels := make([]chan *threadEvent, 0, len(subs))
	for ch := range subs {
		channels = append(channels, ch)
	}
	a.eventSubsMu.RUnlock()

	publishedEvent := &threadEvent{event: event}
	for _, ch := range channels {
		select {
		case ch <- publishedEvent:
		default:
			// Preserve delivery ordering for slow subscribers without blocking publishers.
			go func(target chan *threadEvent, incoming *threadEvent) {
				defer func() {
					_ = recover()
				}()
				target <- incoming
			}(ch, publishedEvent)
		}
	}
}
