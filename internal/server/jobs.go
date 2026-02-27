package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
)

const (
	jobStatusRunning         = "RUNNING"
	jobStatusWaitingApproval = "WAITING_APPROVAL"
	jobStatusCompleted       = "COMPLETED"
	jobStatusFailed          = "FAILED"
	jobStatusPausedBudget    = "PAUSED_BUDGET"
	jobStatusCancelled       = "CANCELLED"
)

var errJobTerminal = errors.New("job is already terminal")

type createJobInput struct {
	Goal           string
	OriginThreadID string
	TriggerType    protocolv1.TriggerType
	TriggerSource  string
	MaxToolSteps   uint32
	MaxWallTimeMS  uint64
}

type jobRecord struct {
	JobID          string
	UserID         string
	Goal           string
	Status         string
	ThreadID       string
	OriginThreadID string
	TriggerTypeRaw string
	TriggerSource  string
	MaxToolSteps   int
	MaxWallTimeMS  uint64
	CurrentTurnID  string
	StartedAtRaw   string
	LastError      string
	CreatedAtRaw   string
	UpdatedAtRaw   string
}

func normalizeJobMaxToolSteps(raw uint32) int {
	value := int(raw)
	if value <= 0 {
		return defaultJobMaxToolSteps
	}
	if value > maxJobMaxToolSteps {
		return maxJobMaxToolSteps
	}
	return value
}

func normalizeJobMaxWallTimeMS(raw uint64) uint64 {
	if raw == 0 {
		return uint64(defaultJobMaxWallTime / time.Millisecond)
	}
	min := uint64(minJobMaxWallTime / time.Millisecond)
	if raw < min {
		return min
	}
	max := uint64(maxJobMaxWallTime / time.Millisecond)
	if raw > max {
		return max
	}
	return raw
}

func dbJobStatusToProto(status string) protocolv1.JobStatus {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case jobStatusRunning:
		return protocolv1.JobStatus_JOB_RUNNING
	case jobStatusWaitingApproval:
		return protocolv1.JobStatus_JOB_WAITING_APPROVAL
	case jobStatusCompleted:
		return protocolv1.JobStatus_JOB_COMPLETED
	case jobStatusFailed:
		return protocolv1.JobStatus_JOB_FAILED
	case jobStatusPausedBudget:
		return protocolv1.JobStatus_JOB_PAUSED_BUDGET
	case jobStatusCancelled:
		return protocolv1.JobStatus_JOB_CANCELLED
	default:
		return protocolv1.JobStatus_JOB_STATUS_UNSPECIFIED
	}
}

func dbTriggerTypeToProto(raw string) protocolv1.TriggerType {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if value, ok := protocolv1.TriggerType_value[raw]; ok {
		return protocolv1.TriggerType(value)
	}
	return protocolv1.TriggerType_TRIGGER_TYPE_UNSPECIFIED
}

func (a *App) emitJobStatusEvent(ctx context.Context, threadID, jobID, status string) {
	if strings.TrimSpace(threadID) == "" || strings.TrimSpace(jobID) == "" {
		return
	}
	_, _ = a.appendThreadEvent(ctx, &protocolv1.ThreadEvent{
		ThreadId:     threadID,
		JobId:        jobID,
		Source:       protocolv1.EventSource_SYSTEM,
		ContentTrust: protocolv1.ContentTrust_TRUSTED_SYSTEM,
		Payload: &protocolv1.ThreadEvent_JobStatusChanged{JobStatusChanged: &protocolv1.JobStatusChanged{
			JobId:  jobID,
			Status: status,
		}},
	})
}

func appendJobEventTx(tx *sql.Tx, jobID, eventType, payload, createdAt string) error {
	_, err := tx.Exec(`
		INSERT INTO job_events(event_id, job_id, event_type, payload_json, created_at)
		VALUES(?, ?, ?, ?, ?)
	`, newID("jev"), jobID, eventType, payload, createdAt)
	return err
}

func (a *App) createJob(ctx context.Context, input createJobInput) (*jobRecord, error) {
	goal := strings.TrimSpace(input.Goal)
	if goal == "" {
		return nil, errors.New("goal is required")
	}
	if !a.llmConfigured {
		return nil, errors.New("planner is not configured")
	}

	maxToolSteps := normalizeJobMaxToolSteps(input.MaxToolSteps)
	maxWallTimeMS := normalizeJobMaxWallTimeMS(input.MaxWallTimeMS)

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	jobID := newID("job")
	threadID := newID("thr")
	originThreadID := strings.TrimSpace(input.OriginThreadID)
	if originThreadID == "" {
		originThreadID = threadID
	}

	triggerType := input.TriggerType
	if triggerType == protocolv1.TriggerType_TRIGGER_TYPE_UNSPECIFIED {
		triggerType = protocolv1.TriggerType_JOB_WAKEUP
	}
	triggerSourceID := strings.TrimSpace(input.TriggerSource)
	if triggerSourceID == "" {
		triggerSourceID = originThreadID
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create job tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO threads(thread_id, user_id, channel, created_at, title, updated_at)
		VALUES(?, ?, 'system', ?, ?, ?)
	`, threadID, a.ownerID, nowStr, jobThreadTitle(goal), nowStr); err != nil {
		return nil, fmt.Errorf("create job thread: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jobs(
			job_id, user_id, goal, status, thread_id, origin_thread_id,
			trigger_type, trigger_source_id, max_tool_steps, max_wall_time_ms,
			current_turn_id, started_at, last_error, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', ?, ?)
	`, jobID, a.ownerID, goal, jobStatusRunning, threadID, originThreadID,
		triggerType.String(), triggerSourceID, maxToolSteps, maxWallTimeMS,
		nowStr, nowStr, nowStr); err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}

	payloadBytes, _ := json.Marshal(map[string]any{
		"status":            jobStatusRunning,
		"thread_id":         threadID,
		"origin_thread_id":  originThreadID,
		"trigger_type":      triggerType.String(),
		"trigger_source_id": triggerSourceID,
		"max_tool_steps":    maxToolSteps,
		"max_wall_time_ms":  maxWallTimeMS,
	})
	if err := insertAuditTx(tx, "job_created", jobID, string(payloadBytes), nowStr); err != nil {
		return nil, fmt.Errorf("insert job_created audit: %w", err)
	}
	if err := appendJobEventTx(tx, jobID, "job_created", string(payloadBytes), nowStr); err != nil {
		return nil, fmt.Errorf("insert job_created event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create job tx: %w", err)
	}

	created := &jobRecord{
		JobID:          jobID,
		UserID:         a.ownerID,
		Goal:           goal,
		Status:         jobStatusRunning,
		ThreadID:       threadID,
		OriginThreadID: originThreadID,
		TriggerTypeRaw: triggerType.String(),
		TriggerSource:  triggerSourceID,
		MaxToolSteps:   maxToolSteps,
		MaxWallTimeMS:  maxWallTimeMS,
		StartedAtRaw:   nowStr,
		CreatedAtRaw:   nowStr,
		UpdatedAtRaw:   nowStr,
	}

	a.emitJobStatusEvent(context.Background(), threadID, jobID, jobStatusRunning)
	a.logger.Info("job created", "job_id", jobID, "thread_id", threadID, "origin_thread_id", originThreadID)
	return created, nil
}

func (a *App) scanJob(row scanner) (*jobRecord, error) {
	item := &jobRecord{}
	var maxToolSteps int
	var maxWallTimeMS int64
	if err := row.Scan(
		&item.JobID,
		&item.UserID,
		&item.Goal,
		&item.Status,
		&item.ThreadID,
		&item.OriginThreadID,
		&item.TriggerTypeRaw,
		&item.TriggerSource,
		&maxToolSteps,
		&maxWallTimeMS,
		&item.CurrentTurnID,
		&item.StartedAtRaw,
		&item.LastError,
		&item.CreatedAtRaw,
		&item.UpdatedAtRaw,
	); err != nil {
		return nil, err
	}
	item.MaxToolSteps = maxToolSteps
	if maxWallTimeMS > 0 {
		item.MaxWallTimeMS = uint64(maxWallTimeMS)
	}
	if strings.TrimSpace(item.OriginThreadID) == "" {
		item.OriginThreadID = item.ThreadID
	}
	return item, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func (a *App) getJobByID(ctx context.Context, jobID string) (*jobRecord, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job_id is required")
	}

	row := a.db.QueryRowContext(ctx, `
		SELECT job_id, user_id, goal, status, thread_id, origin_thread_id,
			trigger_type, trigger_source_id, max_tool_steps, max_wall_time_ms,
			current_turn_id, started_at, last_error, created_at, updated_at
		FROM jobs
		WHERE job_id = ?
	`, jobID)
	job, err := a.scanJob(row)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (a *App) getJobByThreadID(ctx context.Context, threadID string) (*jobRecord, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, errors.New("thread_id is required")
	}

	row := a.db.QueryRowContext(ctx, `
		SELECT job_id, user_id, goal, status, thread_id, origin_thread_id,
			trigger_type, trigger_source_id, max_tool_steps, max_wall_time_ms,
			current_turn_id, started_at, last_error, created_at, updated_at
		FROM jobs
		WHERE thread_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, threadID)
	job, err := a.scanJob(row)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (a *App) listJobs(ctx context.Context) ([]*jobRecord, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT job_id, user_id, goal, status, thread_id, origin_thread_id,
			trigger_type, trigger_source_id, max_tool_steps, max_wall_time_ms,
			current_turn_id, started_at, last_error, created_at, updated_at
		FROM jobs
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]*jobRecord, 0)
	for rows.Next() {
		item, scanErr := a.scanJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (a *App) postJobSummaryToOriginThread(ctx context.Context, job *jobRecord, status, summary string) error {
	if job == nil {
		return nil
	}
	originThreadID := strings.TrimSpace(job.OriginThreadID)
	if originThreadID == "" {
		return nil
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "No additional details were provided."
	}

	prefix := fmt.Sprintf("Background job %s", job.JobID)
	content := ""
	switch status {
	case jobStatusCompleted:
		content = fmt.Sprintf("%s completed.\n\n%s", prefix, summary)
	case jobStatusFailed:
		content = fmt.Sprintf("%s failed.\n\n%s", prefix, summary)
	case jobStatusPausedBudget:
		content = fmt.Sprintf("%s paused due to budget limits.\n\n%s", prefix, summary)
	case jobStatusCancelled:
		content = fmt.Sprintf("%s was cancelled.\n\n%s", prefix, summary)
	default:
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO messages(message_id, thread_id, role, content, created_at)
		VALUES(?, ?, 'system', ?, ?)
	`, newID("msg"), originThreadID, content, now); err != nil {
		return err
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE threads SET updated_at = ? WHERE thread_id = ?`, now, originThreadID)
	return nil
}

func (a *App) setJobStatus(ctx context.Context, job *jobRecord, status, lastError, currentTurnID string) error {
	if job == nil {
		return errors.New("job is required")
	}
	status = strings.ToUpper(strings.TrimSpace(status))
	if status == "" {
		return errors.New("status is required")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	payloadBytes, _ := json.Marshal(map[string]any{
		"status":          status,
		"last_error":      lastError,
		"current_turn_id": currentTurnID,
	})

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs
		SET status = ?, last_error = ?, current_turn_id = ?, updated_at = ?
		WHERE job_id = ?
	`, status, lastError, currentTurnID, now, job.JobID); err != nil {
		return err
	}

	if err := insertAuditTx(tx, "job_status_changed", job.JobID, string(payloadBytes), now); err != nil {
		return err
	}
	if err := appendJobEventTx(tx, job.JobID, "job_status_changed", string(payloadBytes), now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	job.Status = status
	job.LastError = lastError
	job.CurrentTurnID = currentTurnID
	job.UpdatedAtRaw = now

	a.emitJobStatusEvent(context.Background(), job.ThreadID, job.JobID, status)
	return nil
}

func (a *App) markRunningJobsFailedOnStartup(ctx context.Context) error {
	rows, err := a.db.QueryContext(ctx, `
		SELECT job_id, thread_id
		FROM jobs
		WHERE status = ?
	`, jobStatusRunning)
	if err != nil {
		return err
	}
	defer rows.Close()

	type failedJob struct {
		jobID    string
		threadID string
	}
	failed := make([]failedJob, 0)
	for rows.Next() {
		var item failedJob
		if err := rows.Scan(&item.jobID, &item.threadID); err != nil {
			return err
		}
		failed = append(failed, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(failed) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	payload := `{"reason":"failed_restart"}`
	for _, item := range failed {
		if _, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET status = ?, last_error = ?, current_turn_id = '', updated_at = ?
			WHERE job_id = ?
		`, jobStatusFailed, "failed_restart", now, item.jobID); err != nil {
			return err
		}
		if err := insertAuditTx(tx, "job_failed_restart", item.jobID, payload, now); err != nil {
			return err
		}
		if err := appendJobEventTx(tx, item.jobID, "job_failed_restart", payload, now); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	for _, item := range failed {
		a.emitJobStatusEvent(context.Background(), item.threadID, item.jobID, jobStatusFailed)
	}
	a.logger.Warn("marked running jobs failed after restart", "count", len(failed))
	return nil
}

func (a *App) failRunningJobsOnStartup(ctx context.Context) error {
	return a.markRunningJobsFailedOnStartup(ctx)
}

func (a *App) runJobRunner() {
	if !a.llmConfigured {
		a.logger.Info("job runner skipped: planner is not configured")
		return
	}

	ticker := time.NewTicker(a.jobRunnerInterval)
	defer ticker.Stop()

	a.processJobQueueOnce()

	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.processJobQueueOnce()
		}
	}
}

func (a *App) processJobQueueOnce() {
	jobIDs, err := a.listRunnableJobIDs(context.Background())
	if err != nil {
		a.logger.Error("job runner failed to list jobs", "error", err)
		return
	}
	for _, jobID := range jobIDs {
		a.enqueueRunnableJob(jobID)
	}
}

func (a *App) enqueueRunnableJob(jobID string) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}

	ctx := context.Background()
	job, err := a.getJobByID(ctx, jobID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.logger.Error("job runner failed to load job", "job_id", jobID, "error", err)
		}
		return
	}
	if job.Status != jobStatusRunning {
		return
	}

	if failedErr, hasFailed, err := a.latestFailedWorkItemError(ctx, job.JobID); err != nil {
		a.logger.Error("job runner failed to query failed work items", "job_id", job.JobID, "error", err)
		return
	} else if hasFailed {
		if strings.TrimSpace(failedErr) == "" {
			failedErr = "queued work item failed"
		}
		if err := a.setJobStatus(ctx, job, jobStatusFailed, failedErr, turnIDOrCurrent(job)); err != nil {
			a.logger.Error("job runner failed to mark job failed after work item failure", "job_id", job.JobID, "error", err)
		}
		_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusFailed, failedErr)
		return
	}

	remaining := a.remainingJobWallTime(job, time.Now().UTC())
	if remaining <= 0 {
		_ = a.setJobStatus(ctx, job, jobStatusPausedBudget, "max_wall_time_exceeded", "")
		_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusPausedBudget, "The job reached its wall-time budget before completion.")
		return
	}

	turnID := strings.TrimSpace(job.CurrentTurnID)
	if turnID == "" {
		turnID = newID("turn")
		if err := a.setJobStatus(ctx, job, jobStatusRunning, "", turnID); err != nil {
			a.logger.Error("job runner failed to set job turn", "job_id", job.JobID, "error", err)
			return
		}
	}

	workKind := workItemKindJob
	triggerType := dbTriggerTypeToProto(job.TriggerTypeRaw)
	if triggerType == protocolv1.TriggerType_TRIGGER_TYPE_UNSPECIFIED {
		triggerType = protocolv1.TriggerType_JOB_WAKEUP
	}
	if triggerType == protocolv1.TriggerType_SCHEDULE_WAKEUP {
		workKind = workItemKindSchedule
	}

	remainingMS := uint64(remaining / time.Millisecond)
	if remainingMS == 0 {
		remainingMS = 1
	}

	_, err = a.enqueueWorkItem(ctx, workItemInput{
		Kind:             workKind,
		TriggerType:      triggerType,
		ThreadID:         job.ThreadID,
		TurnID:           turnID,
		Prompt:           job.Goal,
		SourceID:         job.JobID,
		JobID:            job.JobID,
		MaxToolSteps:     job.MaxToolSteps,
		MaxWallTimeMS:    remainingMS,
		ProposalSource:   "job",
		ProposalSourceID: job.ThreadID,
	})
	if err != nil {
		if errors.Is(err, errWorkItemAlreadyQueued) {
			return
		}
		a.logger.Error("job runner failed to enqueue work item", "job_id", job.JobID, "error", err)
	}
}

func (a *App) listRunnableJobIDs(ctx context.Context) ([]string, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT job_id
		FROM jobs
		WHERE status = ?
		ORDER BY updated_at ASC
		LIMIT ?
	`, jobStatusRunning, maxConcurrentJobs*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var jobID string
		if err := rows.Scan(&jobID); err != nil {
			return nil, err
		}
		ids = append(ids, jobID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (a *App) startJobRun(jobID string) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	if _, loaded := a.runningJobs.LoadOrStore(jobID, struct{}{}); loaded {
		return
	}

	select {
	case a.jobSem <- struct{}{}:
		go func() {
			defer func() {
				<-a.jobSem
				a.runningJobs.Delete(jobID)
			}()
			a.runJob(jobID)
		}()
	default:
		a.runningJobs.Delete(jobID)
	}
}

func (a *App) remainingJobWallTime(job *jobRecord, now time.Time) time.Duration {
	if job == nil {
		return 0
	}
	maxWall := time.Duration(job.MaxWallTimeMS) * time.Millisecond
	if maxWall <= 0 {
		maxWall = defaultJobMaxWallTime
	}

	startedAtRaw := strings.TrimSpace(job.StartedAtRaw)
	if startedAtRaw == "" {
		startedAtRaw = strings.TrimSpace(job.CreatedAtRaw)
	}
	if startedAtRaw == "" {
		return maxWall
	}

	startedAt, err := parseTimestamp(startedAtRaw)
	if err != nil {
		return maxWall
	}
	return startedAt.Add(maxWall).Sub(now)
}

func (a *App) isJobCancelled(ctx context.Context, jobID string) bool {
	var status string
	err := a.db.QueryRowContext(ctx, `SELECT status FROM jobs WHERE job_id = ?`, jobID).Scan(&status)
	if err != nil {
		return false
	}
	return strings.EqualFold(status, jobStatusCancelled)
}

func (a *App) runJob(jobID string) {
	ctx := context.Background()
	job, err := a.getJobByID(ctx, jobID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.logger.Error("job runner failed to load job", "job_id", jobID, "error", err)
		}
		return
	}
	if job.Status != jobStatusRunning {
		return
	}

	remaining := a.remainingJobWallTime(job, time.Now().UTC())
	if remaining <= 0 {
		_ = a.setJobStatus(ctx, job, jobStatusPausedBudget, "max_wall_time_exceeded", "")
		_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusPausedBudget, "The job reached its wall-time budget before completion.")
		return
	}

	turnID := strings.TrimSpace(job.CurrentTurnID)
	if turnID == "" {
		turnID = newID("turn")
		if err := a.setJobStatus(ctx, job, jobStatusRunning, "", turnID); err != nil {
			a.logger.Error("job runner failed to set job turn", "job_id", job.JobID, "error", err)
			return
		}
	}

	runCtx, cancel := context.WithTimeout(context.Background(), remaining)
	a.jobCancels.Store(job.JobID, cancel)
	defer func() {
		a.jobCancels.Delete(job.JobID)
		cancel()
	}()

	result, runErr := a.executeTurnFromStep(
		runCtx,
		job.ThreadID,
		job.Goal,
		turnID,
		protocolv1.TriggerType_JOB_WAKEUP,
		0,
		"",
		false,
		job.MaxToolSteps,
		"job",
		job.ThreadID,
	)

	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			_ = a.setJobStatus(ctx, job, jobStatusPausedBudget, "max_wall_time_exceeded", turnID)
			_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusPausedBudget, "The job reached its wall-time budget before completion.")
			return
		}
		if errors.Is(runErr, context.Canceled) && a.isJobCancelled(context.Background(), job.JobID) {
			return
		}
		_ = a.setJobStatus(ctx, job, jobStatusFailed, runErr.Error(), turnID)
		_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusFailed, runErr.Error())
		return
	}

	if a.isJobCancelled(context.Background(), job.JobID) {
		return
	}

	if result != nil && result.Paused {
		if err := a.setJobStatus(ctx, job, jobStatusWaitingApproval, "", turnID); err != nil {
			a.logger.Error("job runner failed to set waiting approval", "job_id", job.JobID, "error", err)
		}
		return
	}

	assistantSummary := ""
	if result != nil {
		assistantSummary = result.AssistantMessage
	}
	if err := a.setJobStatus(ctx, job, jobStatusCompleted, "", ""); err != nil {
		a.logger.Error("job runner failed to set completed", "job_id", job.JobID, "error", err)
		return
	}
	if err := a.postJobSummaryToOriginThread(ctx, job, jobStatusCompleted, assistantSummary); err != nil {
		a.logger.Warn("failed posting job completion summary", "job_id", job.JobID, "error", err)
	}
}

func turnIDOrCurrent(job *jobRecord) string {
	if job == nil {
		return ""
	}
	return strings.TrimSpace(job.CurrentTurnID)
}

func (a *App) latestFailedWorkItemError(ctx context.Context, jobID string) (string, bool, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return "", false, nil
	}

	var errText string
	err := a.db.QueryRowContext(ctx, `
		SELECT error
		FROM work_items
		WHERE job_id = ? AND status = ?
		ORDER BY finished_at DESC, created_at DESC
		LIMIT 1
	`, jobID, workItemStatusFailed).Scan(&errText)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return errText, true, nil
}

func (a *App) applyJobContinuationResult(job *jobRecord, turnID string, result *turnExecutionResult, runErr error) {
	if job == nil {
		return
	}
	ctx := context.Background()
	if a.isJobCancelled(ctx, job.JobID) {
		return
	}

	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			_ = a.setJobStatus(ctx, job, jobStatusPausedBudget, "max_wall_time_exceeded", turnID)
			_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusPausedBudget, "The job reached its wall-time budget before completion.")
			return
		}
		if errors.Is(runErr, context.Canceled) && a.isJobCancelled(ctx, job.JobID) {
			return
		}
		_ = a.setJobStatus(ctx, job, jobStatusFailed, runErr.Error(), turnID)
		_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusFailed, runErr.Error())
		return
	}

	if result != nil && result.Paused {
		_ = a.setJobStatus(ctx, job, jobStatusWaitingApproval, "", turnID)
		return
	}

	assistantSummary := ""
	if result != nil {
		assistantSummary = result.AssistantMessage
	}
	_ = a.setJobStatus(ctx, job, jobStatusCompleted, "", "")
	_ = a.postJobSummaryToOriginThread(ctx, job, jobStatusCompleted, assistantSummary)
}

func (a *App) cancelJob(ctx context.Context, jobID string) (*jobRecord, error) {
	job, err := a.getJobByID(ctx, jobID)
	if err != nil {
		return nil, err
	}

	switch job.Status {
	case jobStatusCancelled:
		return job, nil
	case jobStatusCompleted, jobStatusFailed, jobStatusPausedBudget:
		return nil, errJobTerminal
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs
		SET status = ?, last_error = ?, current_turn_id = '', updated_at = ?
		WHERE job_id = ?
	`, jobStatusCancelled, "cancelled_by_user", now, job.JobID); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT action_id
		FROM proposed_actions
		WHERE source = 'job' AND source_id = ? AND status IN ('PENDING', 'APPROVED')
	`, job.ThreadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	actionIDs := make([]string, 0)
	for rows.Next() {
		var actionID string
		if err := rows.Scan(&actionID); err != nil {
			return nil, err
		}
		actionIDs = append(actionIDs, actionID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, actionID := range actionIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE proposed_actions
			SET status = 'REJECTED', rejection_reason = 'job_cancelled'
			WHERE action_id = ? AND status IN ('PENDING', 'APPROVED')
		`, actionID); err != nil {
			return nil, err
		}
		if err := insertAuditTx(tx, "action_rejected", actionID, `{"reason":"job_cancelled"}`, now); err != nil {
			return nil, err
		}
	}

	payload := `{"status":"CANCELLED","reason":"cancelled_by_user"}`
	if err := insertAuditTx(tx, "job_status_changed", job.JobID, payload, now); err != nil {
		return nil, err
	}
	if err := appendJobEventTx(tx, job.JobID, "job_status_changed", payload, now); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	job.Status = jobStatusCancelled
	job.LastError = "cancelled_by_user"
	job.CurrentTurnID = ""
	job.UpdatedAtRaw = now

	if cancelFn, ok := a.jobCancels.Load(job.JobID); ok {
		if cancel, ok := cancelFn.(context.CancelFunc); ok {
			cancel()
		}
	}

	for _, actionID := range actionIDs {
		a.emitActionStatusEvent(context.Background(), "job", job.ThreadID, "", actionID, protocolv1.ActionStatus_REJECTED, "job_cancelled")
	}
	a.emitJobStatusEvent(context.Background(), job.ThreadID, job.JobID, jobStatusCancelled)
	_ = a.postJobSummaryToOriginThread(context.Background(), job, jobStatusCancelled, "Cancelled by user.")

	return job, nil
}

func jobThreadTitle(goal string) string {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "Background Job"
	}
	runes := []rune(goal)
	if len(runes) > 60 {
		goal = strings.TrimSpace(string(runes[:60])) + "…"
	}
	return "Job: " + goal
}

func (a *App) inferJobTriggerFromThread(threadID string) (protocolv1.TriggerType, string) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return protocolv1.TriggerType_CHAT_MESSAGE, ""
	}
	if threadID == heartbeatThreadID {
		return protocolv1.TriggerType_HEARTBEAT, threadID
	}
	job, err := a.getJobByThreadID(context.Background(), threadID)
	if err == nil && job != nil {
		return protocolv1.TriggerType_JOB_WAKEUP, job.JobID
	}
	schedule, err := a.getScheduleByThreadID(context.Background(), threadID)
	if err == nil && schedule != nil {
		return protocolv1.TriggerType_SCHEDULE_WAKEUP, schedule.ScheduleID
	}
	return protocolv1.TriggerType_CHAT_MESSAGE, threadID
}

func (a *App) jobToProto(job *jobRecord) *protocolv1.Job {
	if job == nil {
		return nil
	}
	item := &protocolv1.Job{
		JobId:           job.JobID,
		Goal:            job.Goal,
		Status:          dbJobStatusToProto(job.Status),
		ThreadId:        job.ThreadID,
		TriggerType:     dbTriggerTypeToProto(job.TriggerTypeRaw),
		TriggerSourceId: job.TriggerSource,
		Budget: &protocolv1.TurnBudget{
			MaxToolSteps: uint32(job.MaxToolSteps),
		},
		MaxWallTimeMs: job.MaxWallTimeMS,
		LastError:     job.LastError,
	}
	if createdAt := timestampOrNil(job.CreatedAtRaw); createdAt != nil {
		item.CreatedAt = createdAt
	}
	if updatedAt := timestampOrNil(job.UpdatedAtRaw); updatedAt != nil {
		item.UpdatedAt = updatedAt
	}
	return item
}
