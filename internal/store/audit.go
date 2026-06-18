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
	// TriggerExternal records an active-account change the daemon OBSERVED
	// but did not perform — another credential manager (or a manual
	// `claude /login`) rewrote the live slot. Written by the external-switch
	// watcher, never by a switch aimonitor executed itself.
	TriggerExternal SwitchTrigger = "external"
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

// LatestSwitchTo returns the newest audit row whose to_label is toLabel,
// EXCLUDING TriggerExternal rows. Used by the external-switch watcher to
// attribute an observed active-account change: a recent non-external row
// means aimonitor itself performed the switch. External rows must not
// satisfy the lookup — they are written BY the watcher, and counting them
// would let one detected external switch legitimize the next one to the
// same label. Returns sql.ErrNoRows via the wrapped error when none exists.
func (s *Store) LatestSwitchTo(ctx context.Context, toLabel string) (SwitchAuditRecord, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, ts, COALESCE(from_label, ''), to_label, trigger, COALESCE(reason, '')
		 FROM switch_audit WHERE to_label = ? AND trigger != ?
		 ORDER BY ts DESC LIMIT 1`, toLabel, string(TriggerExternal))
	var r SwitchAuditRecord
	var ts int64
	var trigger string
	if err := row.Scan(&r.ID, &ts, &r.FromLabel, &r.ToLabel, &trigger, &r.Reason); err != nil {
		return SwitchAuditRecord{}, fmt.Errorf("latest switch to %q: %w", toLabel, err)
	}
	r.Ts = time.UnixMilli(ts)
	r.Trigger = SwitchTrigger(trigger)
	return r, nil
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
	defer rows.Close()

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

// ConfigAuditRecord is one row in the config_audit table: a single change
// made through `aimonitor config set` or config import.
type ConfigAuditRecord struct {
	ID       int64
	Ts       time.Time
	Key      string
	OldValue string // "" when the key was previously unset
	NewValue string
	Source   string // "cli", "import", …
}

// InsertConfigAudit records a config change. Ts defaults to time.Now() when
// zero; Source defaults to "cli". Callers treat this as best-effort — a failed
// audit insert must not fail the underlying setting write.
func (s *Store) InsertConfigAudit(ctx context.Context, r ConfigAuditRecord) error {
	if r.Key == "" {
		return fmt.Errorf("InsertConfigAudit: key required")
	}
	if r.Ts.IsZero() {
		r.Ts = time.Now()
	}
	if r.Source == "" {
		r.Source = "cli"
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO config_audit (ts, key, old_value, new_value, source)
		 VALUES (?, ?, ?, ?, ?)`,
		r.Ts.UnixMilli(), r.Key, sqlNullable(r.OldValue), r.NewValue, r.Source)
	if err != nil {
		return fmt.Errorf("insert config_audit: %w", err)
	}
	return nil
}

// ListConfigAudit returns the most recent n config changes, newest first.
// When key is non-empty, only rows for that key are returned.
func (s *Store) ListConfigAudit(ctx context.Context, key string, n int) ([]ConfigAuditRecord, error) {
	if n <= 0 {
		n = 50
	}
	var rows *sql.Rows
	var err error
	if key == "" {
		rows, err = s.DB.QueryContext(ctx,
			`SELECT id, ts, key, COALESCE(old_value, ''), new_value, source
			 FROM config_audit ORDER BY ts DESC LIMIT ?`, n)
	} else {
		rows, err = s.DB.QueryContext(ctx,
			`SELECT id, ts, key, COALESCE(old_value, ''), new_value, source
			 FROM config_audit WHERE key = ? ORDER BY ts DESC LIMIT ?`, key, n)
	}
	if err != nil {
		return nil, fmt.Errorf("query config_audit: %w", err)
	}
	defer rows.Close()

	var out []ConfigAuditRecord
	for rows.Next() {
		var r ConfigAuditRecord
		var ts int64
		if err := rows.Scan(&r.ID, &ts, &r.Key, &r.OldValue, &r.NewValue, &r.Source); err != nil {
			return nil, err
		}
		r.Ts = time.UnixMilli(ts)
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
