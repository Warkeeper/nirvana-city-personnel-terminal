package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

type Store struct {
	db      *sql.DB
	dataDir string
	dbPath  string
	loc     *time.Location
	now     func() time.Time
}

func OpenStore(ctx context.Context, cfg Config) (*Store, error) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().In(loc) }
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(cfg.DataDir, "ncfms.db")
	existed := fileExists(dbPath)
	dsn := sqliteDSN(dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{
		db:      db,
		dataDir: cfg.DataDir,
		dbPath:  dbPath,
		loc:     loc,
		now:     cfg.Now,
	}
	if err := store.configure(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if existed {
		needs, err := store.needsMigration(ctx)
		if err != nil {
			db.Close()
			return nil, err
		}
		if needs {
			if _, err := store.Backup(ctx, "migration"); err != nil {
				db.Close()
				return nil, fmt.Errorf("migration backup failed: %w", err)
			}
		}
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func sqliteDSN(path string) string {
	values := url.Values{}
	values.Set("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "journal_mode(WAL)")
	values.Add("_pragma", "synchronous(FULL)")
	values.Add("_pragma", "busy_timeout(5000)")
	return "file:" + filepath.ToSlash(path) + "?" + values.Encode()
}

func (s *Store) configure(ctx context.Context) error {
	s.db.SetMaxOpenConns(4)
	s.db.SetMaxIdleConns(4)
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA busy_timeout = 5000",
	}
	for _, pragma := range pragmas {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) needsMigration(ctx context.Context) (bool, error) {
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	current, err := s.currentSchemaVersion(ctx)
	if err != nil {
		return false, err
	}
	return current < schemaVersion, nil
}

func (s *Store) currentSchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func (s *Store) SchemaVersion(ctx context.Context) int {
	version, err := s.currentSchemaVersion(ctx)
	if err != nil {
		return 0
	}
	return version
}

func (s *Store) Backup(ctx context.Context, reason string) (string, error) {
	if !fileExists(s.dbPath) {
		return "", nil
	}
	if reason == "" {
		reason = "manual"
	}
	backupsDir := filepath.Join(s.dataDir, "backups")
	if err := os.MkdirAll(backupsDir, 0755); err != nil {
		return "", err
	}
	stamp := s.now().In(s.loc).Format("20060102-150405")
	base := filepath.Join(backupsDir, fmt.Sprintf("ncfms-%s-%s", sanitizeBackupReason(reason), stamp))
	dest := base + ".db"
	for i := 1; fileExists(dest); i++ {
		dest = fmt.Sprintf("%s-%02d.db", base, i)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return "", err
	}
	if err := s.pruneBackups(backupsDir, 20); err != nil {
		return "", err
	}
	return dest, nil
}

func (s *Store) pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type fileInfo struct {
		path string
		mod  time.Time
	}
	var backups []fileInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "ncfms-") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		backups = append(backups, fileInfo{path: filepath.Join(dir, entry.Name()), mod: info.ModTime()})
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].mod.After(backups[j].mod)
	})
	for i := keep; i < len(backups); i++ {
		if err := os.Remove(backups[i].path); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeBackupReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	var b strings.Builder
	for _, r := range reason {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "backup"
	}
	return b.String()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) nowString() string {
	return s.now().In(s.loc).Format(time.RFC3339)
}

func (s *Store) formatDisplayTime(value string) string {
	t, err := parseDBTime(value, s.loc)
	if err != nil {
		return value
	}
	return formatSheetTime(t.In(s.loc))
}

func parseDBTime(value string, loc *time.Location) (time.Time, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return time.Time{}, errors.New("empty time")
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.In(loc), nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", raw, loc); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006/1/2 15:04:05", raw, loc); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q", value)
}

func formatSheetTime(t time.Time) string {
	t = t.In(time.FixedZone("CST", 8*60*60))
	return fmt.Sprintf("%d/%d/%d %02d:%02d:%02d", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
}

func normalizeCode(code string) string {
	return strings.TrimSpace(code)
}
