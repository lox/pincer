package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
)

const (
	workItemStatusPending    = "PENDING"
	workItemStatusProcessing = "PROCESSING"
	workItemStatusCompleted  = "COMPLETED"
	workItemStatusFailed     = "FAILED"
	workItemStatusSkipped    = "SKIPPED"

	workItemKindChat           = "chat"
	workItemKindApprovalResume = "approval_resume"
	workItemKindJob            = "job"
	workItemKindSchedule       = "schedule"
	workItemKindHeartbeat      = "heartbeat"

	workItemClaimBatchSize = 64
	workItemPollInterval   = 25 * time.Millisecond
)

var (
	errWorkItemAlreadyQueued = errors.New("work item already queued")
	errWorkItemSkippedBusy   = errors.New("work item skipped: thread busy")
)

type workItemInput struct {
	Kind             string
	TriggerType      protocolv1.TriggerType
	ThreadID         string
	TurnID           string
	Prompt           string
	SourceID         string
	JobID            string
	WakeupEventID    string
	StartStep        int
	InputMessageID   string
	IsContinuation   bool
	MaxToolSteps     int
	MaxWallTimeMS    uint64
	ProposalSource   string
	ProposalSourceID string
	SkipIfThreadBusy bool
}

type workItemRecord struct {
	WorkItemID       string
	Kind             string
	Priority         int
	TriggerTypeRaw   string
	ThreadID         string
	TurnID           string
	Prompt           string
	SourceID         string
	JobID            string
	WakeupEventID    string
	StartStep        int
	InputMessageID   string
	IsContinuation   bool
	MaxToolSteps     int
	MaxWallTimeMS    uint64
	ProposalSource   string
	ProposalSourceID string
	Status           string
	AssistantMessage string
	FirstActionID    string
	Error            string
	CreatedAtRaw     string
	StartedAtRaw     string
	FinishedAtRaw    string
	SkipIfThreadBusy bool
}

func normalizeWorkItemKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case workItemKindChat:
		return workItemKindChat
	case workItemKindApprovalResume:
		return workItemKindApprovalResume
	case workItemKindJob:
		return workItemKindJob
	case workItemKindSchedule:
		return workItemKindSchedule
	case workItemKindHeartbeat:
		return workItemKindHeartbeat
	default:
		return workItemKindChat
	}
}

func queuePriority(kind string) int {
	switch normalizeWorkItemKind(kind) {
	case workItemKindChat:
		return 10
	case workItemKindApprovalResume:
		return 20
	case workItemKindJob:
		return 30
	case workItemKindSchedule:
		return 40
	case workItemKindHeartbeat:
		return 50
	default:
		return 100
	}
}

func defaultTriggerTypeForWorkItemKind(kind string) protocolv1.TriggerType {
	switch normalizeWorkItemKind(kind) {
	case workItemKindJob:
		return protocolv1.TriggerType_JOB_WAKEUP
	case workItemKindSchedule:
		return protocolv1.TriggerType_SCHEDULE_WAKEUP
	case workItemKindHeartbeat:
		return protocolv1.TriggerType_HEARTBEAT
	default:
		return protocolv1.TriggerType_CHAT_MESSAGE
	}
}

func (a *App) enqueueWorkItem(ctx context.Context, input workItemInput) (*workItemRecord, error) {
	kind := normalizeWorkItemKind(input.Kind)
	threadID := strings.TrimSpace(input.ThreadID)
	if threadID == "" {
		return nil, errors.New("thread_id is required")
	}

	turnID := strings.TrimSpace(input.TurnID)
	if turnID == "" {
		turnID = newID("turn")
	}

	triggerType := input.TriggerType
	if triggerType == protocolv1.TriggerType_TRIGGER_TYPE_UNSPECIFIED {
		triggerType = defaultTriggerTypeForWorkItemKind(kind)
	}

	maxSteps := input.MaxToolSteps
	if maxSteps <= 0 {
		maxSteps = maxInlineToolSteps
	}
	if kind == workItemKindJob || kind == workItemKindSchedule {
		if maxSteps <= 0 {
			maxSteps = defaultJobMaxToolSteps
		}
	}

	proposalSource := strings.TrimSpace(input.ProposalSource)
	if proposalSource == "" {
		if kind == workItemKindJob || kind == workItemKindSchedule {
			proposalSource = "job"
		} else {
			proposalSource = "chat"
		}
	}

	proposalSourceID := strings.TrimSpace(input.ProposalSourceID)
	if proposalSourceID == "" {
		proposalSourceID = threadID
	}

	sourceID := strings.TrimSpace(input.SourceID)
	if sourceID == "" {
		sourceID = threadID
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	startStep := input.StartStep
	if startStep < 0 {
		startStep = 0
	}
	item := &workItemRecord{
		WorkItemID:       newID("wq"),
		Kind:             kind,
		Priority:         queuePriority(kind),
		TriggerTypeRaw:   triggerType.String(),
		ThreadID:         threadID,
		TurnID:           turnID,
		Prompt:           input.Prompt,
		SourceID:         sourceID,
		JobID:            strings.TrimSpace(input.JobID),
		WakeupEventID:    strings.TrimSpace(input.WakeupEventID),
		StartStep:        startStep,
		InputMessageID:   strings.TrimSpace(input.InputMessageID),
		IsContinuation:   input.IsContinuation,
		MaxToolSteps:     maxSteps,
		MaxWallTimeMS:    input.MaxWallTimeMS,
		ProposalSource:   proposalSource,
		ProposalSourceID: proposalSourceID,
		Status:           workItemStatusPending,
		CreatedAtRaw:     now,
		SkipIfThreadBusy: input.SkipIfThreadBusy,
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var activeTurnID string
	if err := tx.QueryRowContext(ctx, `
		SELECT active_turn_id
		FROM threads
		WHERE thread_id = ?
	`, item.ThreadID).Scan(&activeTurnID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}

	if item.JobID != "" {
		var inFlight int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM work_items
			WHERE job_id = ? AND status IN (?, ?)
		`, item.JobID, workItemStatusPending, workItemStatusProcessing).Scan(&inFlight); err != nil {
			return nil, err
		}
		if inFlight > 0 {
			return nil, errWorkItemAlreadyQueued
		}
	}

	if item.IsContinuation {
		var inFlight int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM work_items
			WHERE turn_id = ? AND is_continuation = 1 AND status IN (?, ?)
		`, item.TurnID, workItemStatusPending, workItemStatusProcessing).Scan(&inFlight); err != nil {
			return nil, err
		}
		if inFlight > 0 {
			return nil, errWorkItemAlreadyQueued
		}
	}

	if item.SkipIfThreadBusy {
		if strings.TrimSpace(activeTurnID) != "" {
			return nil, errWorkItemSkippedBusy
		}
		var queued int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM work_items
			WHERE thread_id = ? AND status IN (?, ?)
		`, item.ThreadID, workItemStatusPending, workItemStatusProcessing).Scan(&queued); err != nil {
			return nil, err
		}
		if queued > 0 {
			return nil, errWorkItemSkippedBusy
		}
	}

	isContinuation := 0
	if item.IsContinuation {
		isContinuation = 1
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_items(
			work_item_id, kind, priority, trigger_type, thread_id, turn_id,
			prompt, source_id, job_id, wakeup_event_id, start_step, input_message_id,
			is_continuation, max_tool_steps, max_wall_time_ms, proposal_source,
			proposal_source_id, status, assistant_message, first_action_id,
			error, created_at, started_at, finished_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, '', '')
	`, item.WorkItemID, item.Kind, item.Priority, item.TriggerTypeRaw, item.ThreadID, item.TurnID,
		item.Prompt, item.SourceID, item.JobID, item.WakeupEventID, item.StartStep, item.InputMessageID,
		isContinuation, item.MaxToolSteps, int64(item.MaxWallTimeMS), item.ProposalSource,
		item.ProposalSourceID, item.Status, item.CreatedAtRaw); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.signalWorkQueue()

	return item, nil
}

func (a *App) signalWorkQueue() {
	if a == nil || a.workSignal == nil {
		return
	}
	select {
	case a.workSignal <- struct{}{}:
	default:
	}
}

func (a *App) scanWorkItem(row scanner) (*workItemRecord, error) {
	item := &workItemRecord{}
	var isContinuationInt int
	var maxWallTimeMS int64
	if err := row.Scan(
		&item.WorkItemID,
		&item.Kind,
		&item.Priority,
		&item.TriggerTypeRaw,
		&item.ThreadID,
		&item.TurnID,
		&item.Prompt,
		&item.SourceID,
		&item.JobID,
		&item.WakeupEventID,
		&item.StartStep,
		&item.InputMessageID,
		&isContinuationInt,
		&item.MaxToolSteps,
		&maxWallTimeMS,
		&item.ProposalSource,
		&item.ProposalSourceID,
		&item.Status,
		&item.AssistantMessage,
		&item.FirstActionID,
		&item.Error,
		&item.CreatedAtRaw,
		&item.StartedAtRaw,
		&item.FinishedAtRaw,
	); err != nil {
		return nil, err
	}
	item.IsContinuation = isContinuationInt != 0
	if maxWallTimeMS > 0 {
		item.MaxWallTimeMS = uint64(maxWallTimeMS)
	}
	return item, nil
}

func (a *App) getWorkItemByID(ctx context.Context, workItemID string) (*workItemRecord, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT work_item_id, kind, priority, trigger_type, thread_id, turn_id,
			prompt, source_id, job_id, wakeup_event_id, start_step, input_message_id,
			is_continuation, max_tool_steps, max_wall_time_ms, proposal_source,
			proposal_source_id, status, assistant_message, first_action_id,
			error, created_at, started_at, finished_at
		FROM work_items
		WHERE work_item_id = ?
	`, workItemID)
	return a.scanWorkItem(row)
}

func (a *App) waitForWorkItemTerminal(ctx context.Context, workItemID string) (*workItemRecord, error) {
	workItemID = strings.TrimSpace(workItemID)
	if workItemID == "" {
		return nil, errors.New("work_item_id is required")
	}

	ticker := time.NewTicker(workItemPollInterval)
	defer ticker.Stop()

	for {
		item, err := a.getWorkItemByID(ctx, workItemID)
		if err != nil {
			return nil, err
		}

		switch item.Status {
		case workItemStatusCompleted:
			return item, nil
		case workItemStatusFailed:
			errText := strings.TrimSpace(item.Error)
			if errText == "" {
				errText = "work item failed"
			}
			return item, errors.New(errText)
		case workItemStatusSkipped:
			errText := strings.TrimSpace(item.Error)
			if errText == "" {
				errText = "work item skipped"
			}
			if errText == "thread_busy" {
				return item, errWorkItemSkippedBusy
			}
			return item, errors.New(errText)
		}

		if a.backgroundWorkersOff {
			a.processWorkQueueOnce()
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *App) runWorkQueue() {
	ticker := time.NewTicker(a.workRunnerInterval)
	defer ticker.Stop()

	a.processWorkQueueOnce()

	for {
		select {
		case <-a.stopCh:
			return
		case <-a.workSignal:
			a.processWorkQueueOnce()
		case <-ticker.C:
			a.processWorkQueueOnce()
		}
	}
}

func (a *App) processWorkQueueOnce() {
	for {
		select {
		case a.workSem <- struct{}{}:
		default:
			return
		}

		item, err := a.claimNextWorkItem(context.Background())
		if err != nil {
			<-a.workSem
			a.logger.Error("work queue failed to claim item", "error", err)
			return
		}
		if item == nil {
			<-a.workSem
			return
		}

		go func(queued *workItemRecord) {
			defer func() { <-a.workSem }()
			a.executeWorkItem(queued)
		}(item)
	}
}

func (a *App) claimNextWorkItem(ctx context.Context) (*workItemRecord, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT work_item_id
		FROM work_items
		WHERE status = ?
		ORDER BY priority ASC, created_at ASC
		LIMIT ?
	`, workItemStatusPending, workItemClaimBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0, workItemClaimBatchSize)
	for rows.Next() {
		var workItemID string
		if err := rows.Scan(&workItemID); err != nil {
			return nil, err
		}
		ids = append(ids, workItemID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, workItemID := range ids {
		item, err := a.tryClaimWorkItemByID(ctx, workItemID)
		if err != nil {
			return nil, err
		}
		if item != nil {
			return item, nil
		}
	}

	return nil, nil
}

func (a *App) tryClaimWorkItemByID(ctx context.Context, workItemID string) (*workItemRecord, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	itemRow := tx.QueryRowContext(ctx, `
		SELECT work_item_id, kind, priority, trigger_type, thread_id, turn_id,
			prompt, source_id, job_id, wakeup_event_id, start_step, input_message_id,
			is_continuation, max_tool_steps, max_wall_time_ms, proposal_source,
			proposal_source_id, status, assistant_message, first_action_id,
			error, created_at, started_at, finished_at
		FROM work_items
		WHERE work_item_id = ?
	`, workItemID)
	item, err := a.scanWorkItem(itemRow)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if item.Status != workItemStatusPending {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	var activeTurnID string
	if err := tx.QueryRowContext(ctx, `
		SELECT active_turn_id
		FROM threads
		WHERE thread_id = ?
	`, item.ThreadID).Scan(&activeTurnID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, updateErr := tx.ExecContext(ctx, `
				UPDATE work_items
				SET status = ?, error = ?, finished_at = ?
				WHERE work_item_id = ? AND status = ?
			`, workItemStatusFailed, "thread_not_found", now, item.WorkItemID, workItemStatusPending); updateErr != nil {
				return nil, updateErr
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, err
	}

	if strings.TrimSpace(activeTurnID) != "" {
		if item.Kind == workItemKindHeartbeat {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `
				UPDATE work_items
				SET status = ?, error = ?, finished_at = ?
				WHERE work_item_id = ? AND status = ?
			`, workItemStatusSkipped, "thread_busy", now, item.WorkItemID, workItemStatusPending); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	threadLockRes, err := tx.ExecContext(ctx, `
		UPDATE threads
		SET active_turn_id = ?
		WHERE thread_id = ? AND active_turn_id = ''
	`, item.TurnID, item.ThreadID)
	if err != nil {
		return nil, err
	}
	threadRows, err := threadLockRes.RowsAffected()
	if err != nil {
		return nil, err
	}
	if threadRows == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET status = ?, started_at = ?, error = ''
		WHERE work_item_id = ? AND status = ?
	`, workItemStatusProcessing, now, item.WorkItemID, workItemStatusPending)
	if err != nil {
		return nil, err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rowsAffected == 0 {
		if _, clearErr := tx.ExecContext(ctx, `
			UPDATE threads
			SET active_turn_id = ''
			WHERE thread_id = ? AND active_turn_id = ?
		`, item.ThreadID, item.TurnID); clearErr != nil {
			return nil, clearErr
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	item.Status = workItemStatusProcessing
	item.StartedAtRaw = now
	return item, nil
}

func (a *App) completeWorkItem(ctx context.Context, workItemID, status, assistantMessage, firstActionID, errText string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `
		UPDATE work_items
		SET status = ?, assistant_message = ?, first_action_id = ?, error = ?, finished_at = ?
		WHERE work_item_id = ?
	`, status, assistantMessage, firstActionID, errText, now, workItemID)
	return err
}

func (a *App) releaseThreadTurnLock(ctx context.Context, threadID, turnID string) {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return
	}
	_, _ = a.db.ExecContext(ctx, `
		UPDATE threads
		SET active_turn_id = ''
		WHERE thread_id = ? AND active_turn_id = ?
	`, threadID, turnID)
}

func (a *App) executeWorkItem(item *workItemRecord) {
	if item == nil {
		return
	}

	defer a.releaseThreadTurnLock(context.Background(), item.ThreadID, item.TurnID)
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = a.completeWorkItem(context.Background(), item.WorkItemID, workItemStatusFailed, "", "", fmt.Sprintf("panic: %v", recovered))
		}
	}()

	runCtx := context.Background()
	cancel := context.CancelFunc(func() {})
	if item.MaxWallTimeMS > 0 {
		runCtx, cancel = context.WithTimeout(context.Background(), time.Duration(item.MaxWallTimeMS)*time.Millisecond)
	}
	defer cancel()

	if item.JobID != "" {
		a.jobCancels.Store(item.JobID, cancel)
		defer a.jobCancels.Delete(item.JobID)
	}

	triggerType := dbTriggerTypeToProto(item.TriggerTypeRaw)
	if triggerType == protocolv1.TriggerType_TRIGGER_TYPE_UNSPECIFIED {
		triggerType = defaultTriggerTypeForWorkItemKind(item.Kind)
	}

	var (
		result *turnExecutionResult
		runErr error
	)
	if item.IsContinuation {
		result, runErr = a.executeTurnFromStep(
			runCtx,
			item.ThreadID,
			item.Prompt,
			item.TurnID,
			triggerType,
			item.StartStep,
			item.InputMessageID,
			true,
			item.MaxToolSteps,
			item.ProposalSource,
			item.ProposalSourceID,
		)
	} else {
		result, runErr = a.executeTurnFromStep(
			runCtx,
			item.ThreadID,
			item.Prompt,
			item.TurnID,
			triggerType,
			0,
			"",
			false,
			item.MaxToolSteps,
			item.ProposalSource,
			item.ProposalSourceID,
		)
	}

	if item.JobID != "" {
		job, err := a.getJobByID(context.Background(), item.JobID)
		switch {
		case err == nil:
			a.applyJobContinuationResult(job, item.TurnID, result, runErr)
		case errors.Is(err, sql.ErrNoRows):
			// Job was deleted or compacted while queued.
		default:
			a.logger.Warn("failed loading queued job for result reconciliation", "job_id", item.JobID, "error", err)
		}
		if runErr != nil {
			a.ensureJobTerminalAfterWorkItemError(item.JobID, item.TurnID, runErr)
		}
	}

	if runErr != nil {
		if item.JobID != "" && errors.Is(runErr, context.Canceled) && a.isJobCancelled(context.Background(), item.JobID) {
			_ = a.completeWorkItem(context.Background(), item.WorkItemID, workItemStatusSkipped, "", "", "job_cancelled")
			return
		}
		_ = a.completeWorkItem(context.Background(), item.WorkItemID, workItemStatusFailed, "", "", runErr.Error())
		return
	}

	assistantMessage := ""
	firstActionID := ""
	if result != nil {
		assistantMessage = result.AssistantMessage
		if len(result.ActionIDs) > 0 {
			firstActionID = result.ActionIDs[0]
		}
	}
	_ = a.completeWorkItem(context.Background(), item.WorkItemID, workItemStatusCompleted, assistantMessage, firstActionID, "")
}

func (a *App) ensureJobTerminalAfterWorkItemError(jobID, turnID string, runErr error) {
	if strings.TrimSpace(jobID) == "" || runErr == nil {
		return
	}

	ctx := context.Background()
	if errors.Is(runErr, context.Canceled) && a.isJobCancelled(ctx, jobID) {
		return
	}

	job, err := a.getJobByID(ctx, jobID)
	if err != nil {
		return
	}
	if job.Status != jobStatusRunning {
		return
	}

	if errors.Is(runErr, context.DeadlineExceeded) {
		_ = a.setJobStatus(ctx, job, jobStatusPausedBudget, "max_wall_time_exceeded", turnID)
		_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusPausedBudget, "The job reached its wall-time budget before completion.")
		return
	}

	_ = a.setJobStatus(ctx, job, jobStatusFailed, runErr.Error(), turnID)
	_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusFailed, runErr.Error())
}

func (a *App) requeueInFlightWorkItemsOnStartup(ctx context.Context) error {
	if _, err := a.db.ExecContext(ctx, `
		UPDATE threads
		SET active_turn_id = ''
		WHERE active_turn_id != ''
	`); err != nil {
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "no such column") {
			return err
		}
	}

	res, err := a.db.ExecContext(ctx, `
		UPDATE work_items
		SET status = ?, error = ?, started_at = ''
		WHERE status = ?
	`, workItemStatusPending, "requeued_after_restart", workItemStatusProcessing)
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "no such table") {
			return nil
		}
		return err
	}

	rowsAffected, err := res.RowsAffected()
	if err == nil && rowsAffected > 0 {
		a.logger.Warn("requeued work items after restart", "count", rowsAffected)
	}
	return nil
}
