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
	"github.com/robfig/cron/v3"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	wakeupStatusPending      = "PENDING"
	wakeupStatusProcessing   = "PROCESSING"
	wakeupStatusDispatched   = "DISPATCHED"
	wakeupStatusSkipped      = "SKIPPED_ACTIVE"
	wakeupStatusFailed       = "FAILED"
	wakeupReasonScheduleDue  = "schedule_due"
	wakeupReasonManualRunNow = "manual_run_now"
)

var scheduleCronParser = cron.NewParser(
	cron.Minute |
		cron.Hour |
		cron.Dom |
		cron.Month |
		cron.Dow |
		cron.Descriptor,
)

type scheduleRecord struct {
	ScheduleID     string
	UserID         string
	Name           string
	Goal           string
	ThreadID       string
	TriggerKindRaw string
	TriggerSpec    string
	Timezone       string
	Enabled        bool
	NextRunAtRaw   string
	LastRunAtRaw   string
	CreatedAtRaw   string
	UpdatedAtRaw   string
}

type wakeupEventRecord struct {
	WakeupEventID   string
	ScheduleID      string
	ScheduledForRaw string
	Status          string
	Reason          string
	JobID           string
	TurnID          string
	Error           string
	CreatedAtRaw    string
	ProcessedAtRaw  string
}

type createScheduleInput struct {
	Name        string
	Goal        string
	ThreadID    string
	TriggerKind protocolv1.ScheduleTriggerKind
	TriggerSpec string
	Timezone    string
	Enabled     bool
}

type updateScheduleInput struct {
	ScheduleID   string
	Name         *string
	Goal         *string
	TriggerKind  *protocolv1.ScheduleTriggerKind
	TriggerSpec  *string
	Timezone     *string
	Enabled      *bool
	RecomputeNow bool
}

func scheduleTriggerKindToDB(kind protocolv1.ScheduleTriggerKind) (string, error) {
	switch kind {
	case protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_CRON,
		protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_INTERVAL,
		protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_AT:
		return kind.String(), nil
	default:
		return "", errors.New("trigger_kind is required")
	}
}

func dbScheduleTriggerKindToProto(raw string) protocolv1.ScheduleTriggerKind {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if v, ok := protocolv1.ScheduleTriggerKind_value[raw]; ok {
		return protocolv1.ScheduleTriggerKind(v)
	}
	return protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_KIND_UNSPECIFIED
}

func normalizeScheduleTimezone(raw string) (string, *time.Location, error) {
	zone := strings.TrimSpace(raw)
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "", nil, fmt.Errorf("invalid timezone %q", zone)
	}
	return zone, loc, nil
}

func parseScheduleInterval(spec string) (time.Duration, error) {
	interval, err := time.ParseDuration(strings.TrimSpace(spec))
	if err != nil {
		return 0, fmt.Errorf("invalid interval: %w", err)
	}
	if interval < minScheduleInterval {
		return 0, fmt.Errorf("interval must be at least %s", minScheduleInterval)
	}
	return interval, nil
}

func parseScheduleAt(spec string, loc *time.Location) (time.Time, error) {
	raw := strings.TrimSpace(spec)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04"} {
		if parsed, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid at timestamp %q (use RFC3339)", raw)
}

func normalizeScheduleTrigger(kind protocolv1.ScheduleTriggerKind, spec string, loc *time.Location, now time.Time) (string, time.Time, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", time.Time{}, errors.New("trigger_spec is required")
	}

	switch kind {
	case protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_CRON:
		sched, err := scheduleCronParser.Parse(spec)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("invalid cron trigger: %w", err)
		}
		next := sched.Next(now.In(loc))
		if next.IsZero() {
			return "", time.Time{}, errors.New("cron trigger did not produce a next run")
		}
		return spec, next.UTC(), nil
	case protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_INTERVAL:
		interval, err := parseScheduleInterval(spec)
		if err != nil {
			return "", time.Time{}, err
		}
		return interval.String(), now.Add(interval).UTC(), nil
	case protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_AT:
		at, err := parseScheduleAt(spec, loc)
		if err != nil {
			return "", time.Time{}, err
		}
		if !at.After(now) {
			return "", time.Time{}, errors.New("at trigger must be in the future")
		}
		return at.Format(time.RFC3339Nano), at, nil
	default:
		return "", time.Time{}, errors.New("trigger_kind is required")
	}
}

func (a *App) nextScheduleRun(item *scheduleRecord, after time.Time) (time.Time, bool, error) {
	if item == nil {
		return time.Time{}, false, errors.New("schedule is required")
	}
	_, loc, err := normalizeScheduleTimezone(item.Timezone)
	if err != nil {
		return time.Time{}, false, err
	}

	kind := dbScheduleTriggerKindToProto(item.TriggerKindRaw)
	spec := strings.TrimSpace(item.TriggerSpec)
	switch kind {
	case protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_CRON:
		sched, err := scheduleCronParser.Parse(spec)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("invalid cron trigger: %w", err)
		}
		next := sched.Next(after.In(loc))
		if next.IsZero() {
			return time.Time{}, false, errors.New("cron trigger did not produce a next run")
		}
		return next.UTC(), true, nil
	case protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_INTERVAL:
		interval, err := parseScheduleInterval(spec)
		if err != nil {
			return time.Time{}, false, err
		}
		return after.Add(interval).UTC(), true, nil
	case protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_AT:
		at, err := parseScheduleAt(spec, loc)
		if err != nil {
			return time.Time{}, false, err
		}
		if at.After(after) {
			return at, true, nil
		}
		return time.Time{}, false, nil
	default:
		return time.Time{}, false, errors.New("invalid trigger kind")
	}
}

func scheduleThreadTitle(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Scheduled Task"
	}
	runes := []rune(name)
	if len(runes) > 60 {
		name = strings.TrimSpace(string(runes[:60])) + "…"
	}
	return "Schedule: " + name
}

func (a *App) scanSchedule(row scanner) (*scheduleRecord, error) {
	item := &scheduleRecord{}
	var enabledInt int
	if err := row.Scan(
		&item.ScheduleID,
		&item.UserID,
		&item.Name,
		&item.Goal,
		&item.ThreadID,
		&item.TriggerKindRaw,
		&item.TriggerSpec,
		&item.Timezone,
		&enabledInt,
		&item.NextRunAtRaw,
		&item.LastRunAtRaw,
		&item.CreatedAtRaw,
		&item.UpdatedAtRaw,
	); err != nil {
		return nil, err
	}
	item.Enabled = enabledInt != 0
	return item, nil
}

func (a *App) getScheduleByID(ctx context.Context, scheduleID string) (*scheduleRecord, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return nil, errors.New("schedule_id is required")
	}

	row := a.db.QueryRowContext(ctx, `
		SELECT schedule_id, user_id, name, goal, thread_id, trigger_kind, trigger_spec,
			timezone, enabled, next_run_at, last_run_at, created_at, updated_at
		FROM schedules
		WHERE schedule_id = ?
	`, scheduleID)
	return a.scanSchedule(row)
}

func (a *App) getScheduleByThreadID(ctx context.Context, threadID string) (*scheduleRecord, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, errors.New("thread_id is required")
	}

	row := a.db.QueryRowContext(ctx, `
		SELECT schedule_id, user_id, name, goal, thread_id, trigger_kind, trigger_spec,
			timezone, enabled, next_run_at, last_run_at, created_at, updated_at
		FROM schedules
		WHERE thread_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, threadID)
	return a.scanSchedule(row)
}

func (a *App) listSchedules(ctx context.Context) ([]*scheduleRecord, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT schedule_id, user_id, name, goal, thread_id, trigger_kind, trigger_spec,
			timezone, enabled, next_run_at, last_run_at, created_at, updated_at
		FROM schedules
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]*scheduleRecord, 0)
	for rows.Next() {
		item, scanErr := a.scanSchedule(rows)
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

func (a *App) scheduleToProto(item *scheduleRecord) *protocolv1.Schedule {
	if item == nil {
		return nil
	}
	result := &protocolv1.Schedule{
		ScheduleId:  item.ScheduleID,
		Name:        item.Name,
		TriggerKind: dbScheduleTriggerKindToProto(item.TriggerKindRaw),
		TriggerSpec: item.TriggerSpec,
		Timezone:    item.Timezone,
		Enabled:     item.Enabled,
	}
	if ts := timestampOrNil(item.NextRunAtRaw); ts != nil {
		result.NextRunAt = ts
	}
	if ts := timestampOrNil(item.LastRunAtRaw); ts != nil {
		result.LastRunAt = ts
	}
	if ts := timestampOrNil(item.CreatedAtRaw); ts != nil {
		result.CreatedAt = ts
	}
	if ts := timestampOrNil(item.UpdatedAtRaw); ts != nil {
		result.UpdatedAt = ts
	}
	return result
}

func (a *App) createSchedule(ctx context.Context, input createScheduleInput) (*scheduleRecord, error) {
	if !a.llmConfigured {
		return nil, errors.New("planner is not configured")
	}

	name := strings.TrimSpace(input.Name)
	goal := strings.TrimSpace(input.Goal)
	if goal == "" {
		goal = name
	}
	if name == "" {
		name = goal
	}
	if name == "" {
		return nil, errors.New("name is required")
	}
	if goal == "" {
		return nil, errors.New("goal is required")
	}

	zone, loc, err := normalizeScheduleTimezone(input.Timezone)
	if err != nil {
		return nil, err
	}
	kindRaw, err := scheduleTriggerKindToDB(input.TriggerKind)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	normalizedSpec, nextRunAt, err := normalizeScheduleTrigger(input.TriggerKind, input.TriggerSpec, loc, now)
	if err != nil {
		return nil, err
	}

	enabled := input.Enabled
	nextRunRaw := ""
	if enabled {
		nextRunRaw = nextRunAt.Format(time.RFC3339Nano)
	}

	nowStr := now.Format(time.RFC3339Nano)
	scheduleID := newID("sch")
	threadID := strings.TrimSpace(input.ThreadID)
	createThread := threadID == ""
	if createThread {
		threadID = newID("thr")
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if enabled {
		var activeCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedules WHERE enabled = 1`).Scan(&activeCount); err != nil {
			return nil, err
		}
		if activeCount >= maxActiveSchedules {
			return nil, fmt.Errorf("max active schedules reached (%d)", maxActiveSchedules)
		}
	}

	if createThread {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO threads(thread_id, user_id, channel, created_at, title, updated_at)
			VALUES(?, ?, 'ios', ?, ?, ?)
		`, threadID, a.ownerID, nowStr, scheduleThreadTitle(name), nowStr); err != nil {
			return nil, fmt.Errorf("create schedule thread: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schedules(
			schedule_id, user_id, name, goal, thread_id, trigger_kind, trigger_spec,
			timezone, enabled, next_run_at, last_run_at, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)
	`, scheduleID, a.ownerID, name, goal, threadID, kindRaw, normalizedSpec,
		zone, boolToInt(enabled), nextRunRaw, nowStr, nowStr); err != nil {
		return nil, fmt.Errorf("create schedule: %w", err)
	}

	payloadBytes, _ := json.Marshal(map[string]any{
		"name":         name,
		"goal":         goal,
		"thread_id":    threadID,
		"trigger_kind": kindRaw,
		"trigger_spec": normalizedSpec,
		"timezone":     zone,
		"enabled":      enabled,
		"next_run_at":  nextRunRaw,
	})
	if err := insertAuditTx(tx, "schedule_created", scheduleID, string(payloadBytes), nowStr); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	created := &scheduleRecord{
		ScheduleID:     scheduleID,
		UserID:         a.ownerID,
		Name:           name,
		Goal:           goal,
		ThreadID:       threadID,
		TriggerKindRaw: kindRaw,
		TriggerSpec:    normalizedSpec,
		Timezone:       zone,
		Enabled:        enabled,
		NextRunAtRaw:   nextRunRaw,
		CreatedAtRaw:   nowStr,
		UpdatedAtRaw:   nowStr,
	}
	return created, nil
}

func (a *App) updateSchedule(ctx context.Context, input updateScheduleInput) (*scheduleRecord, error) {
	item, err := a.getScheduleByID(ctx, input.ScheduleID)
	if err != nil {
		return nil, err
	}

	name := item.Name
	goal := item.Goal
	zone := item.Timezone
	kind := dbScheduleTriggerKindToProto(item.TriggerKindRaw)
	triggerSpec := item.TriggerSpec
	enabled := item.Enabled

	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if input.Goal != nil {
		goal = strings.TrimSpace(*input.Goal)
	}
	if goal == "" {
		goal = name
	}
	if name == "" {
		name = goal
	}
	if name == "" {
		return nil, errors.New("name is required")
	}
	if goal == "" {
		return nil, errors.New("goal is required")
	}

	recomputeNextRun := input.RecomputeNow
	if input.TriggerKind != nil {
		kind = *input.TriggerKind
		recomputeNextRun = true
	}
	if input.TriggerSpec != nil {
		triggerSpec = strings.TrimSpace(*input.TriggerSpec)
		recomputeNextRun = true
	}
	if input.Timezone != nil {
		zone = strings.TrimSpace(*input.Timezone)
		recomputeNextRun = true
	}
	if input.Enabled != nil {
		enabled = *input.Enabled
		if enabled && !item.Enabled {
			recomputeNextRun = true
		}
	}

	zone, loc, err := normalizeScheduleTimezone(zone)
	if err != nil {
		return nil, err
	}
	kindRaw, err := scheduleTriggerKindToDB(kind)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	normalizedSpec := triggerSpec
	nextRunRaw := item.NextRunAtRaw
	if enabled {
		if recomputeNextRun || strings.TrimSpace(nextRunRaw) == "" {
			nextRunAt := time.Time{}
			normalizedSpec, nextRunAt, err = normalizeScheduleTrigger(kind, triggerSpec, loc, now)
			if err != nil {
				return nil, err
			}
			nextRunRaw = nextRunAt.Format(time.RFC3339Nano)
		}
	} else {
		nextRunRaw = ""
	}

	if enabled && !item.Enabled {
		var activeCount int
		if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedules WHERE enabled = 1`).Scan(&activeCount); err != nil {
			return nil, err
		}
		if activeCount >= maxActiveSchedules {
			return nil, fmt.Errorf("max active schedules reached (%d)", maxActiveSchedules)
		}
	}

	if _, err := a.db.ExecContext(ctx, `
		UPDATE schedules
		SET name = ?, goal = ?, trigger_kind = ?, trigger_spec = ?, timezone = ?,
			enabled = ?, next_run_at = ?, updated_at = ?
		WHERE schedule_id = ?
	`, name, goal, kindRaw, normalizedSpec, zone, boolToInt(enabled), nextRunRaw, nowStr, item.ScheduleID); err != nil {
		return nil, err
	}

	payloadBytes, _ := json.Marshal(map[string]any{
		"name":         name,
		"goal":         goal,
		"trigger_kind": kindRaw,
		"trigger_spec": normalizedSpec,
		"timezone":     zone,
		"enabled":      enabled,
		"next_run_at":  nextRunRaw,
	})
	if err := a.insertAuditEvent(ctx, "schedule_updated", item.ScheduleID, string(payloadBytes)); err != nil {
		return nil, err
	}

	item.Name = name
	item.Goal = goal
	item.TriggerKindRaw = kindRaw
	item.TriggerSpec = normalizedSpec
	item.Timezone = zone
	item.Enabled = enabled
	item.NextRunAtRaw = nextRunRaw
	item.UpdatedAtRaw = nowStr
	return item, nil
}

func (a *App) deleteSchedule(ctx context.Context, scheduleID string) error {
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return errors.New("schedule_id is required")
	}

	res, err := a.db.ExecContext(ctx, `DELETE FROM schedules WHERE schedule_id = ?`, scheduleID)
	if err != nil {
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	if err := a.insertAuditEvent(ctx, "schedule_deleted", scheduleID, `{"deleted":true}`); err != nil {
		return err
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (a *App) insertAuditEvent(ctx context.Context, eventType, entityID, payload string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertAuditTx(tx, eventType, entityID, payload, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) runSchedulerService() {
	if !a.llmConfigured {
		a.logger.Info("scheduler service skipped: planner is not configured")
		return
	}

	ticker := time.NewTicker(a.schedulerInterval)
	defer ticker.Stop()

	a.processSchedulerQueueOnce()

	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.processSchedulerQueueOnce()
		}
	}
}

func (a *App) processSchedulerQueueOnce() {
	now := time.Now().UTC()
	if err := a.enqueueDueScheduleWakeups(context.Background(), now); err != nil {
		a.logger.Error("scheduler failed to enqueue due wakeups", "error", err)
	}
	a.dispatchPendingWakeups(context.Background(), maxPendingWakeupsPerTick)
}

func (a *App) enqueueDueScheduleWakeups(ctx context.Context, now time.Time) error {
	ids, err := a.listDueScheduleIDs(ctx, now, maxPendingWakeupsPerTick)
	if err != nil {
		return err
	}
	for _, scheduleID := range ids {
		if err := a.enqueueDueScheduleWakeupByID(ctx, scheduleID, now); err != nil {
			a.logger.Error("scheduler failed to enqueue due schedule", "schedule_id", scheduleID, "error", err)
		}
	}
	return nil
}

func (a *App) listDueScheduleIDs(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		limit = maxPendingWakeupsPerTick
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT schedule_id
		FROM schedules
		WHERE enabled = 1 AND next_run_at != '' AND next_run_at <= ?
		ORDER BY next_run_at ASC
		LIMIT ?
	`, now.Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var scheduleID string
		if err := rows.Scan(&scheduleID); err != nil {
			return nil, err
		}
		ids = append(ids, scheduleID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (a *App) enqueueDueScheduleWakeupByID(ctx context.Context, scheduleID string, now time.Time) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT schedule_id, user_id, name, goal, thread_id, trigger_kind, trigger_spec,
			timezone, enabled, next_run_at, last_run_at, created_at, updated_at
		FROM schedules
		WHERE schedule_id = ?
	`, scheduleID)
	item, err := a.scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !item.Enabled || strings.TrimSpace(item.NextRunAtRaw) == "" {
		return tx.Commit()
	}

	scheduledFor, err := parseTimestamp(item.NextRunAtRaw)
	if err != nil {
		return err
	}
	if scheduledFor.After(now) {
		return tx.Commit()
	}

	nowStr := now.Format(time.RFC3339Nano)
	wakeupID, err := a.enqueueScheduleWakeupTx(tx, item.ScheduleID, scheduledFor, wakeupReasonScheduleDue, nowStr)
	if err != nil {
		return err
	}

	nextRun, hasNext, err := a.nextScheduleRun(item, scheduledFor)
	if err != nil {
		return err
	}

	enabledInt := boolToInt(item.Enabled)
	nextRunRaw := ""
	if hasNext {
		nextRunRaw = nextRun.Format(time.RFC3339Nano)
	} else {
		enabledInt = 0
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE schedules
		SET last_run_at = ?, next_run_at = ?, enabled = ?, updated_at = ?
		WHERE schedule_id = ?
	`, scheduledFor.Format(time.RFC3339Nano), nextRunRaw, enabledInt, nowStr, item.ScheduleID); err != nil {
		return err
	}

	payloadBytes, _ := json.Marshal(map[string]any{
		"wakeup_event_id":   wakeupID,
		"scheduled_for_utc": scheduledFor.Format(time.RFC3339Nano),
		"next_run_at":       nextRunRaw,
		"enabled":           enabledInt == 1,
	})
	if err := insertAuditTx(tx, "schedule_wakeup_enqueued", item.ScheduleID, string(payloadBytes), nowStr); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) enqueueScheduleWakeupTx(tx *sql.Tx, scheduleID string, scheduledFor time.Time, reason, nowStr string) (string, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = wakeupReasonScheduleDue
	}

	wakeupID := newID("wak")
	scheduledForRaw := scheduledFor.UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`
		INSERT OR IGNORE INTO wakeup_events(
			wakeup_event_id, schedule_id, scheduled_for_utc, status, reason,
			job_id, turn_id, error, created_at, processed_at
		) VALUES(?, ?, ?, ?, ?, '', '', '', ?, '')
	`, wakeupID, scheduleID, scheduledForRaw, wakeupStatusPending, reason, nowStr)
	if err != nil {
		return "", err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if rowsAffected > 0 {
		return wakeupID, nil
	}

	var existingID string
	if err := tx.QueryRow(`
		SELECT wakeup_event_id
		FROM wakeup_events
		WHERE schedule_id = ? AND scheduled_for_utc = ?
	`, scheduleID, scheduledForRaw).Scan(&existingID); err != nil {
		return "", err
	}
	return existingID, nil
}

func (a *App) enqueueScheduleWakeup(ctx context.Context, scheduleID string, scheduledFor time.Time, reason string) (string, error) {
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	wakeupID, err := a.enqueueScheduleWakeupTx(tx, scheduleID, scheduledFor, reason, nowStr)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return wakeupID, nil
}

func (a *App) listPendingWakeupIDs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = maxPendingWakeupsPerTick
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT wakeup_event_id
		FROM wakeup_events
		WHERE status = ?
		ORDER BY scheduled_for_utc ASC
		LIMIT ?
	`, wakeupStatusPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (a *App) dispatchPendingWakeups(ctx context.Context, limit int) {
	ids, err := a.listPendingWakeupIDs(ctx, limit)
	if err != nil {
		a.logger.Error("scheduler failed to list pending wakeups", "error", err)
		return
	}
	for _, wakeupID := range ids {
		if _, err := a.dispatchWakeupByID(ctx, wakeupID); err != nil {
			a.logger.Error("scheduler failed to dispatch wakeup", "wakeup_event_id", wakeupID, "error", err)
		}
	}
}

func (a *App) scanWakeup(row scanner) (*wakeupEventRecord, error) {
	item := &wakeupEventRecord{}
	if err := row.Scan(
		&item.WakeupEventID,
		&item.ScheduleID,
		&item.ScheduledForRaw,
		&item.Status,
		&item.Reason,
		&item.JobID,
		&item.TurnID,
		&item.Error,
		&item.CreatedAtRaw,
		&item.ProcessedAtRaw,
	); err != nil {
		return nil, err
	}
	return item, nil
}

func (a *App) getWakeupByID(ctx context.Context, wakeupID string) (*wakeupEventRecord, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT wakeup_event_id, schedule_id, scheduled_for_utc, status, reason,
			job_id, turn_id, error, created_at, processed_at
		FROM wakeup_events
		WHERE wakeup_event_id = ?
	`, wakeupID)
	return a.scanWakeup(row)
}

func (a *App) claimWakeup(ctx context.Context, wakeupID string) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := a.db.ExecContext(ctx, `
		UPDATE wakeup_events
		SET status = ?, processed_at = ?
		WHERE wakeup_event_id = ? AND status = ?
	`, wakeupStatusProcessing, now, wakeupID, wakeupStatusPending)
	if err != nil {
		return false, err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected > 0, nil
}

func (a *App) setWakeupStatus(ctx context.Context, wakeupID, status, jobID, turnID, reason, errText string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `
		UPDATE wakeup_events
		SET status = ?, job_id = ?, turn_id = ?, reason = ?, error = ?, processed_at = ?
		WHERE wakeup_event_id = ?
	`, status, jobID, turnID, reason, errText, now, wakeupID)
	return err
}

func (a *App) hasActiveScheduleJob(ctx context.Context, scheduleID string) (bool, error) {
	var count int
	err := a.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM jobs
		WHERE trigger_type = ? AND trigger_source_id = ?
			AND status IN (?, ?)
	`, protocolv1.TriggerType_SCHEDULE_WAKEUP.String(), scheduleID, jobStatusRunning, jobStatusWaitingApproval).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (a *App) dispatchWakeupByID(ctx context.Context, wakeupID string) (*wakeupEventRecord, error) {
	claimed, err := a.claimWakeup(ctx, wakeupID)
	if err != nil {
		return nil, err
	}
	wakeup, err := a.getWakeupByID(ctx, wakeupID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return wakeup, nil
	}

	schedule, err := a.getScheduleByID(ctx, wakeup.ScheduleID)
	if errors.Is(err, sql.ErrNoRows) {
		setErr := a.setWakeupStatus(ctx, wakeupID, wakeupStatusFailed, "", "", wakeup.Reason, "schedule_not_found")
		if setErr != nil {
			return nil, setErr
		}
		return a.getWakeupByID(ctx, wakeupID)
	}
	if err != nil {
		setErr := a.setWakeupStatus(ctx, wakeupID, wakeupStatusFailed, "", "", wakeup.Reason, err.Error())
		if setErr != nil {
			return nil, setErr
		}
		return a.getWakeupByID(ctx, wakeupID)
	}

	active, err := a.hasActiveScheduleJob(ctx, wakeup.ScheduleID)
	if err != nil {
		setErr := a.setWakeupStatus(ctx, wakeupID, wakeupStatusFailed, "", "", wakeup.Reason, err.Error())
		if setErr != nil {
			return nil, setErr
		}
		return a.getWakeupByID(ctx, wakeupID)
	}
	if active {
		if err := a.setWakeupStatus(ctx, wakeupID, wakeupStatusSkipped, "", "", wakeup.Reason, "active_job_exists"); err != nil {
			return nil, err
		}
		_ = a.insertAuditEvent(ctx, "schedule_wakeup_skipped", wakeupID, `{"reason":"active_job_exists"}`)
		return a.getWakeupByID(ctx, wakeupID)
	}

	job, err := a.createJob(ctx, createJobInput{
		Goal:           schedule.Goal,
		OriginThreadID: schedule.ThreadID,
		TriggerType:    protocolv1.TriggerType_SCHEDULE_WAKEUP,
		TriggerSource:  schedule.ScheduleID,
	})
	if err != nil {
		if statusErr := a.setWakeupStatus(ctx, wakeupID, wakeupStatusFailed, "", "", wakeup.Reason, err.Error()); statusErr != nil {
			return nil, statusErr
		}
		_ = a.insertAuditEvent(ctx, "schedule_wakeup_failed", wakeupID, fmt.Sprintf(`{"error":%q}`, err.Error()))
		return a.getWakeupByID(ctx, wakeupID)
	}

	if err := a.setWakeupStatus(ctx, wakeupID, wakeupStatusDispatched, job.JobID, job.CurrentTurnID, wakeup.Reason, ""); err != nil {
		return nil, err
	}

	scheduledFor, parseErr := parseTimestamp(wakeup.ScheduledForRaw)
	if parseErr == nil {
		a.emitScheduleTriggeredEvent(context.Background(), schedule.ThreadID, job.JobID, schedule.ScheduleID, scheduledFor)
	}
	_ = a.insertAuditEvent(ctx, "schedule_wakeup_dispatched", wakeupID, fmt.Sprintf(`{"schedule_id":%q,"job_id":%q}`, schedule.ScheduleID, job.JobID))

	return a.getWakeupByID(ctx, wakeupID)
}

func (a *App) runScheduleNow(ctx context.Context, scheduleID string) (*wakeupEventRecord, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return nil, errors.New("schedule_id is required")
	}
	if _, err := a.getScheduleByID(ctx, scheduleID); err != nil {
		return nil, err
	}

	wakeupID, err := a.enqueueScheduleWakeup(ctx, scheduleID, time.Now().UTC(), wakeupReasonManualRunNow)
	if err != nil {
		return nil, err
	}
	if _, err := a.dispatchWakeupByID(ctx, wakeupID); err != nil {
		return nil, err
	}
	return a.getWakeupByID(ctx, wakeupID)
}

func (a *App) emitScheduleTriggeredEvent(ctx context.Context, threadID, jobID, scheduleID string, scheduledFor time.Time) {
	if strings.TrimSpace(threadID) == "" || strings.TrimSpace(scheduleID) == "" {
		return
	}
	_, _ = a.appendThreadEvent(ctx, &protocolv1.ThreadEvent{
		ThreadId:     threadID,
		JobId:        jobID,
		Source:       protocolv1.EventSource_SYSTEM,
		ContentTrust: protocolv1.ContentTrust_TRUSTED_SYSTEM,
		Payload: &protocolv1.ThreadEvent_ScheduleTriggered{ScheduleTriggered: &protocolv1.ScheduleTriggered{
			ScheduleId:      scheduleID,
			ScheduledForUtc: timestamppb.New(scheduledFor.UTC()),
		}},
	})
}

func (a *App) requeueInFlightWakeupsOnStartup(ctx context.Context) error {
	res, err := a.db.ExecContext(ctx, `
		UPDATE wakeup_events
		SET status = ?, error = ?, processed_at = ''
		WHERE status = ?
	`, wakeupStatusPending, "requeued_after_restart", wakeupStatusProcessing)
	if err != nil {
		// Table may not exist on pre-migration databases if migration failed; keep startup resilient.
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return nil
		}
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if err == nil && rowsAffected > 0 {
		a.logger.Warn("requeued wakeups after restart", "count", rowsAffected)
	}
	return nil
}
