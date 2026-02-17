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

	a.logger.Debug(
		"thread event appended",
		"thread_id", copyEvent.GetThreadId(),
		"turn_id", copyEvent.GetTurnId(),
		"event_id", copyEvent.GetEventId(),
		"sequence", copyEvent.GetSequence(),
		"payload", threadEventPayloadName(copyEvent),
	)

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
	a.logger.Debug("thread subscription opened", "thread_id", threadID, "subscriber_count", len(subs))
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
	a.logger.Debug("thread subscription closed", "thread_id", threadID, "subscriber_count", len(subs))
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
	a.logger.Debug(
		"publishing thread event",
		"thread_id", event.GetThreadId(),
		"event_id", event.GetEventId(),
		"sequence", event.GetSequence(),
		"payload", threadEventPayloadName(event),
		"subscriber_count", len(channels),
	)

	publishedEvent := &threadEvent{event: event}
	for _, ch := range channels {
		select {
		case ch <- publishedEvent:
		default:
			a.logger.Debug(
				"thread subscriber channel full; delivering asynchronously",
				"thread_id", event.GetThreadId(),
				"event_id", event.GetEventId(),
				"sequence", event.GetSequence(),
			)
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

func threadEventPayloadName(event *protocolv1.ThreadEvent) string {
	if event == nil || event.GetPayload() == nil {
		return "none"
	}

	switch event.GetPayload().(type) {
	case *protocolv1.ThreadEvent_TurnStarted:
		return "turn_started"
	case *protocolv1.ThreadEvent_TurnBudgetApplied:
		return "turn_budget_applied"
	case *protocolv1.ThreadEvent_ModelOutputRepairAttempted:
		return "model_output_repair_attempted"
	case *protocolv1.ThreadEvent_TurnCompleted:
		return "turn_completed"
	case *protocolv1.ThreadEvent_TurnFailed:
		return "turn_failed"
	case *protocolv1.ThreadEvent_TurnPaused:
		return "turn_paused"
	case *protocolv1.ThreadEvent_TurnResumed:
		return "turn_resumed"
	case *protocolv1.ThreadEvent_AssistantThinkingDelta:
		return "assistant_thinking_delta"
	case *protocolv1.ThreadEvent_AssistantTextDelta:
		return "assistant_text_delta"
	case *protocolv1.ThreadEvent_AssistantMessageCommitted:
		return "assistant_message_committed"
	case *protocolv1.ThreadEvent_ToolCallPlanned:
		return "tool_call_planned"
	case *protocolv1.ThreadEvent_ToolExecutionStarted:
		return "tool_execution_started"
	case *protocolv1.ThreadEvent_ToolExecutionOutputDelta:
		return "tool_execution_output_delta"
	case *protocolv1.ThreadEvent_ToolExecutionFinished:
		return "tool_execution_finished"
	case *protocolv1.ThreadEvent_PolicyDecisionMade:
		return "policy_decision_made"
	case *protocolv1.ThreadEvent_ProposedActionCreated:
		return "proposed_action_created"
	case *protocolv1.ThreadEvent_ProposedActionStatusChanged:
		return "proposed_action_status_changed"
	case *protocolv1.ThreadEvent_IdempotencyConflict:
		return "idempotency_conflict"
	case *protocolv1.ThreadEvent_JobStatusChanged:
		return "job_status_changed"
	case *protocolv1.ThreadEvent_ScheduleTriggered:
		return "schedule_triggered"
	case *protocolv1.ThreadEvent_DelegatedCallbackReceived:
		return "delegated_callback_received"
	case *protocolv1.ThreadEvent_AuditEventRecorded:
		return "audit_event_recorded"
	case *protocolv1.ThreadEvent_NotificationQueued:
		return "notification_queued"
	case *protocolv1.ThreadEvent_ArtifactCreated:
		return "artifact_created"
	case *protocolv1.ThreadEvent_MemoryCheckpointSaved:
		return "memory_checkpoint_saved"
	case *protocolv1.ThreadEvent_SkillProposalCreated:
		return "skill_proposal_created"
	case *protocolv1.ThreadEvent_SelfImprovementProposalCreated:
		return "self_improvement_proposal_created"
	case *protocolv1.ThreadEvent_Heartbeat:
		return "heartbeat"
	case *protocolv1.ThreadEvent_StreamGap:
		return "stream_gap"
	default:
		return "unknown"
	}
}
