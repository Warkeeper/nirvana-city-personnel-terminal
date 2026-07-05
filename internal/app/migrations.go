package app

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *Store) migrate(ctx context.Context) error {
	if err := s.rejectLegacySchema(ctx); err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		tableStmts := []string{
			`CREATE TABLE IF NOT EXISTS schema_migrations (
				version INTEGER PRIMARY KEY,
				applied_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS city_sessions (
				opened_at TEXT PRIMARY KEY,
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
				session_opened_at TEXT NOT NULL REFERENCES city_sessions(opened_at) ON UPDATE RESTRICT ON DELETE RESTRICT,
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
		}
		for _, stmt := range tableStmts {
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
		if err := s.cleanupDuplicateActiveTravelRecordsTx(ctx, tx); err != nil {
			return err
		}
		indexStmts := []string{
			`CREATE INDEX IF NOT EXISTS idx_residents_kind ON residents(kind)`,
			`CREATE INDEX IF NOT EXISTS idx_identity_history_code ON identity_history(resident_code, occurred_at)`,
			`CREATE INDEX IF NOT EXISTS idx_gold_records_code_time ON gold_records(resident_code, occurred_at)`,
			`CREATE INDEX IF NOT EXISTS idx_gold_records_time ON gold_records(occurred_at)`,
			`CREATE INDEX IF NOT EXISTS idx_travel_records_session ON travel_records(session_opened_at, resident_code)`,
			`CREATE INDEX IF NOT EXISTS idx_travel_records_enter ON travel_records(enter_at)`,
			`CREATE INDEX IF NOT EXISTS idx_travel_records_leave_visible ON travel_records(leave_at, canceled_at, hidden_at)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_travel_records_active_unique
				ON travel_records(session_opened_at, resident_code)
				WHERE canceled_at IS NULL AND hidden_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_travel_extensions_travel ON travel_extensions(travel_id)`,
		}
		for _, stmt := range indexStmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if current < schemaVersion {
			if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", schemaVersion, s.nowString()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) cleanupDuplicateActiveTravelRecordsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT session_opened_at, resident_code, COUNT(*)
		FROM travel_records
		WHERE canceled_at IS NULL AND hidden_at IS NULL
		GROUP BY session_opened_at, resident_code
		HAVING COUNT(*) > 1`)
	if err != nil {
		return err
	}
	type duplicate struct {
		sessionOpenedAt string
		code            string
		count           int
	}
	var duplicates []duplicate
	for rows.Next() {
		var dup duplicate
		if err := rows.Scan(&dup.sessionOpenedAt, &dup.code, &dup.count); err != nil {
			_ = rows.Close()
			return err
		}
		duplicates = append(duplicates, dup)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(duplicates) == 0 {
		return nil
	}

	now := s.nowString()
	for _, dup := range duplicates {
		res, err := tx.ExecContext(ctx, `UPDATE travel_records
			SET canceled_at = ?, hidden_at = ?, hidden_after_leave = 0
			WHERE id IN (
				SELECT id FROM travel_records
				WHERE session_opened_at = ? AND resident_code = ? AND canceled_at IS NULL AND hidden_at IS NULL
				ORDER BY id ASC
				LIMIT -1 OFFSET 1
			)`, now, now, dup.sessionOpenedAt, dup.code)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			message := fmt.Sprintf("cleanup duplicate active travel records: session=%s code=%s canceled=%d", dup.sessionOpenedAt, dup.code, affected)
			if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(kind, message, operator, created_at)
				VALUES(?, ?, ?, ?)`, "migration.travel.dedupe", message, "", now); err != nil {
				return err
			}
		}
	}
	return nil
}
