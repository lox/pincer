package server

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	runtimeSettingHeartbeatEnabled         = "heartbeat_enabled"
	runtimeSettingHeartbeatIntervalMinutes = "heartbeat_interval_minutes"
	minHeartbeatInterval                   = 15 * time.Minute
)

func normalizeHeartbeatInterval(raw time.Duration) time.Duration {
	if raw <= 0 {
		return defaultHeartbeatInterval
	}
	return raw
}

func clampHeartbeatInterval(raw time.Duration) time.Duration {
	normalized := normalizeHeartbeatInterval(raw)
	if normalized < minHeartbeatInterval {
		return minHeartbeatInterval
	}
	return normalized
}

func loadHeartbeatSettings(ctx context.Context, db *sql.DB, fallbackEnabled bool, fallbackInterval time.Duration) (bool, time.Duration, error) {
	enabled := fallbackEnabled
	interval := normalizeHeartbeatInterval(fallbackInterval)

	rows, err := db.QueryContext(ctx, `
		SELECT key, value
		FROM runtime_settings
		WHERE key IN (?, ?)
	`, runtimeSettingHeartbeatEnabled, runtimeSettingHeartbeatIntervalMinutes)
	if err != nil {
		return false, 0, fmt.Errorf("query runtime settings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			return false, 0, fmt.Errorf("scan runtime setting: %w", err)
		}

		switch strings.TrimSpace(key) {
		case runtimeSettingHeartbeatEnabled:
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err == nil {
				enabled = parsed
			}
		case runtimeSettingHeartbeatIntervalMinutes:
			minutes, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil {
				interval = clampHeartbeatInterval(time.Duration(minutes) * time.Minute)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, 0, fmt.Errorf("iterate runtime settings: %w", err)
	}

	return enabled, interval, nil
}

func upsertRuntimeSettingTx(tx *sql.Tx, key, value, now string) error {
	_, err := tx.Exec(`
		INSERT INTO runtime_settings(key, value, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at
	`, key, value, now)
	return err
}

func (a *App) heartbeatConfig() (bool, time.Duration) {
	a.heartbeatMu.RLock()
	defer a.heartbeatMu.RUnlock()
	return a.heartbeatEnabled, a.heartbeatInterval
}

func (a *App) applyHeartbeatConfig(enabled bool, interval time.Duration) {
	a.heartbeatMu.Lock()
	a.heartbeatEnabled = enabled
	a.heartbeatInterval = normalizeHeartbeatInterval(interval)
	a.heartbeatMu.Unlock()
}

func (a *App) notifyHeartbeatConfigUpdated() {
	select {
	case a.heartbeatConfigSignal <- struct{}{}:
	default:
	}
}

func (a *App) persistHeartbeatSettings(ctx context.Context, enabled bool, interval time.Duration) error {
	normalizedInterval := normalizeHeartbeatInterval(interval)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin heartbeat settings tx: %w", err)
	}
	defer tx.Rollback()

	if err := upsertRuntimeSettingTx(tx, runtimeSettingHeartbeatEnabled, strconv.FormatBool(enabled), now); err != nil {
		return fmt.Errorf("persist heartbeat enabled setting: %w", err)
	}
	if err := upsertRuntimeSettingTx(tx, runtimeSettingHeartbeatIntervalMinutes, strconv.FormatInt(int64(normalizedInterval/time.Minute), 10), now); err != nil {
		return fmt.Errorf("persist heartbeat interval setting: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit heartbeat settings tx: %w", err)
	}

	a.applyHeartbeatConfig(enabled, normalizedInterval)
	a.notifyHeartbeatConfigUpdated()
	return nil
}
