package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SwitchTrigger is the reason a switch_audit row was written.
type SwitchTrigger string

const (
	// TriggerManual is `aimonitor switch <label>`.
	TriggerManual SwitchTrigger = "manual"
	// TriggerAutoswitch is the auto-switch engine reacting to a tripwire.
	TriggerAutoswitch SwitchTrigger = "autoswitch"
	// TriggerFirstRun is the first-run import of an existing Claude Code
	// credential.
	TriggerFirstRun SwitchTrigger = "first-run"
)

// SwitchAuditRecord is one row in the switch_audit table. From and To
// labels are nullable to handle first-run (no `from`) and switches into
// an unknown account (no `to`).
type SwitchAuditRecord struct {
	ID                  int64
	Ts                  time.Time
	FromLabel           string
	ToLabel             string
	Trigger             SwitchTrigger
	Reason              string
	FromProbedRemaining int64 // 0 when no probe was taken
	ToProbedRemaining   int64 // 0 when no probe was taken
}

// InsertSwitchAudit writes a row. Ts defaults to time.Now() when zero.
func (s *Store) InsertSwitchAudit(ctx context.Context, r SwitchAuditRecord) error {
	if r.Trigger == "" {
		return fmt.Errorf("InsertSwitchAudit: trigger required")
	}
	if r.ToLabel == "" {
		return fmt.Errorf("InsertSwitchAudit: to_label required")
	}
	if r.Ts.IsZero() {
		r.Ts = time.Now()
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO switch_audit (ts, from_label, to_label, trigger, reason, from_probed_remaining, to_probed_remaining)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Ts.UnixMilli(),
		sqlNullable(r.FromLabel),
		r.ToLabel,
		string(r.Trigger),
		sqlNullable(r.Reason),
		nullableInt(r.FromProbedRemaining),
		nullableInt(r.ToProbedRemaining),
	)
	if err != nil {
		return fmt.Errorf("insert switch_audit: %w", err)
	}
	return nil
}

// ListSwitchAudit returns the most recent n rows, newest first.
func (s *Store) ListSwitchAudit(ctx context.Context, n int) ([]SwitchAuditRecord, error) {
	if n <= 0 {
		n = 20
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, ts, COALESCE(from_label, ''), to_label, trigger, COALESCE(reason, ''),
		        COALESCE(from_probed_remaining, 0), COALESCE(to_probed_remaining, 0)
		 FROM switch_audit ORDER BY ts DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("query switch_audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SwitchAuditRecord
	for rows.Next() {
		var r SwitchAuditRecord
		var ts int64
		var trigger string
		if err := rows.Scan(&r.ID, &ts, &r.FromLabel, &r.ToLabel, &trigger, &r.Reason,
			&r.FromProbedRemaining, &r.ToProbedRemaining); err != nil {
			return nil, err
		}
		r.Ts = time.UnixMilli(ts)
		r.Trigger = SwitchTrigger(trigger)
		out = append(out, r)
	}
	return out, rows.Err()
}

// sqlNullable returns a sql.NullString suitable for binding into nullable
// TEXT columns. Empty string maps to NULL.
func sqlNullable(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}

// nullableInt maps 0 -> NULL for nullable INTEGER columns where 0 has a
// semantic meaning ('we didn't take a probe' vs '0 tokens left').
func nullableInt(n int64) any {
	if n == 0 {
		return sql.NullInt64{}
	}
	return n
}
