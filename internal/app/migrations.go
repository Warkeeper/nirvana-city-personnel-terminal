package app

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *Store) migrate(ctx context.Context) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS schema_migrations (
				version INTEGER PRIMARY KEY,
				applied_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS city_sessions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				opened_at TEXT NOT NULL,
				closed_at TEXT,
				operator TEXT NOT NULL,
				note TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE IF NOT EXISTS residents (
				code TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				kind TEXT NOT NULL CHECK (kind IN ('player', 'npc')),
				balance INTEGER NOT NULL DEFAULT 0,
				identity_current TEXT NOT NULL DEFAULT '未设置',
				remark TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS identity_history (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				resident_code TEXT NOT NULL REFERENCES residents(code) ON UPDATE RESTRICT ON DELETE RESTRICT,
				resident_name_snapshot TEXT NOT NULL,
				identity TEXT NOT NULL,
				occurred_at TEXT NOT NULL,
				deleted_at TEXT
			)`,
			`CREATE TABLE IF NOT EXISTS gold_records (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				resident_code TEXT NOT NULL REFERENCES residents(code) ON UPDATE RESTRICT ON DELETE RESTRICT,
				resident_name_snapshot TEXT NOT NULL,
				identity_snapshot TEXT NOT NULL,
				record_type TEXT NOT NULL CHECK (record_type IN ('in', 'out', 'forfeit', 'allocate')),
				amount INTEGER NOT NULL,
				balance_after INTEGER NOT NULL,
				remark TEXT NOT NULL DEFAULT '',
				affect_balance INTEGER NOT NULL,
				voided INTEGER NOT NULL DEFAULT 0,
				balance_reverted INTEGER NOT NULL DEFAULT 0,
				operator TEXT NOT NULL DEFAULT '',
				occurred_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS travel_records (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id INTEGER NOT NULL REFERENCES city_sessions(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
				resident_code TEXT NOT NULL REFERENCES residents(code) ON UPDATE RESTRICT ON DELETE RESTRICT,
				resident_name_snapshot TEXT NOT NULL,
				identity_snapshot TEXT NOT NULL,
				enter_at TEXT NOT NULL,
				leave_at TEXT NOT NULL,
				stay_minutes INTEGER NOT NULL,
				canceled_at TEXT,
				hidden_at TEXT,
				hidden_after_leave INTEGER NOT NULL DEFAULT 0,
				operator TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS travel_extensions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				travel_id INTEGER NOT NULL REFERENCES travel_records(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
				added_minutes INTEGER NOT NULL,
				occurred_at TEXT NOT NULL,
				operator TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE IF NOT EXISTS npc_panel_state (
				resident_code TEXT PRIMARY KEY REFERENCES residents(code) ON UPDATE RESTRICT ON DELETE RESTRICT,
				visible INTEGER NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS idempotency_requests (
				key TEXT PRIMARY KEY,
				method TEXT NOT NULL,
				path TEXT NOT NULL,
				payload_hash TEXT NOT NULL,
				response_status INTEGER NOT NULL,
				response_body TEXT NOT NULL,
				created_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS audit_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				kind TEXT NOT NULL,
				message TEXT NOT NULL,
				operator TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_residents_kind ON residents(kind)`,
			`CREATE INDEX IF NOT EXISTS idx_identity_history_code ON identity_history(resident_code, occurred_at)`,
			`CREATE INDEX IF NOT EXISTS idx_gold_records_code_time ON gold_records(resident_code, occurred_at)`,
			`CREATE INDEX IF NOT EXISTS idx_gold_records_time ON gold_records(occurred_at)`,
			`CREATE INDEX IF NOT EXISTS idx_travel_records_session ON travel_records(session_id, resident_code)`,
			`CREATE INDEX IF NOT EXISTS idx_travel_records_enter ON travel_records(enter_at)`,
			`CREATE INDEX IF NOT EXISTS idx_travel_records_leave_visible ON travel_records(leave_at, canceled_at, hidden_at)`,
			`CREATE INDEX IF NOT EXISTS idx_travel_extensions_travel ON travel_extensions(travel_id)`,
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		var current int
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
			return err
		}
		if current > schemaVersion {
			return fmt.Errorf("database schema version %d is newer than app schema %d", current, schemaVersion)
		}
		if current < schemaVersion {
			if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", schemaVersion, s.nowString()); err != nil {
				return err
			}
		}
		return nil
	})
}
