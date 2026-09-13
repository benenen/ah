// Package history persists copy attempts independently of connection configuration.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

// Record contains copy metadata only; credentials and key contents must never be supplied.
type Record struct {
	ID                                                        int64
	Source, Destination, Cwd, ConfigPath, KnownHosts, KeyPath string
	Force                                                     bool
	StartedAt, FinishedAt                                     time.Time
	Status                                                    string
	Bytes                                                     int64
	Error                                                     string
}

type Store struct{ db *sql.DB }

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate history directory: %w", err)
	}
	return filepath.Join(dir, "ah", "history.db"), nil
}

// Open creates a private SQLite file, retaining existing parent directory permissions.
// One connection per handle and SQLite's busy timeout serialize concurrent writers.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("history database path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve history path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}
	fd, err := unix.Open(abs, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("open history file: %w", err)
	}
	f := os.NewFile(uintptr(fd), abs)
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("history database must be a regular file")
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, fmt.Errorf("prepare history file: %w", err)
	}
	uri := url.URL{Scheme: "file", Path: abs}
	params := url.Values{}
	params.Set("_pragma", "busy_timeout(5000)")
	uri.RawQuery = params.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("open history database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS copy_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source TEXT NOT NULL, destination TEXT NOT NULL, cwd TEXT NOT NULL,
		config_path TEXT NOT NULL, known_hosts TEXT NOT NULL, key_path TEXT NOT NULL,
		force INTEGER NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER,
		status TEXT NOT NULL CHECK(status IN ('running','success','failed','canceled')),
		bytes INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
		search_paths TEXT NOT NULL, search_error TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return nil, fmt.Errorf("initialize history database: %w", errors.Join(err, db.Close()))
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Begin records a running attempt. A crash leaves that state intact for inspection.
func (s *Store) Begin(ctx context.Context, r Record) (int64, error) {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO copy_history
		(source,destination,cwd,config_path,known_hosts,key_path,force,started_at,status,search_paths)
		VALUES (?,?,?,?,?,?,?,?,'running',?)`, r.Source, r.Destination, r.Cwd, r.ConfigPath, r.KnownHosts, r.KeyPath, r.Force, r.StartedAt.UnixNano(), strings.ToLower(r.Source+"\n"+r.Destination))
	if err != nil {
		return 0, fmt.Errorf("record copy attempt: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read history ID: %w", err)
	}
	return id, nil
}

// Finish only transitions a running attempt to a terminal status.
// Callers completing canceled work should supply a fresh, bounded context.
func (s *Store) Finish(ctx context.Context, id int64, bytes int64, status, errorText string) error {
	if status != "success" && status != "failed" && status != "canceled" {
		return fmt.Errorf("invalid copy history status %q", status)
	}
	if bytes < 0 {
		return errors.New("copy history bytes must not be negative")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE copy_history SET finished_at=?,bytes=?,status=?,error=?,search_error=? WHERE id=? AND status='running'`, time.Now().UTC().UnixNano(), bytes, status, errorText, strings.ToLower(errorText), id)
	if err != nil {
		return fmt.Errorf("finish copy history: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check copy history update: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("history record %d is missing or already finished", id)
	}
	return nil
}

const recordColumns = `id,source,destination,cwd,config_path,known_hosts,key_path,force,started_at,finished_at,status,bytes,error`

func scanRecord(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var started int64
	var finished sql.NullInt64
	err := row.Scan(&r.ID, &r.Source, &r.Destination, &r.Cwd, &r.ConfigPath, &r.KnownHosts, &r.KeyPath, &r.Force, &started, &finished, &r.Status, &r.Bytes, &r.Error)
	if err != nil {
		return Record{}, err
	}
	r.StartedAt = time.Unix(0, started).UTC()
	if finished.Valid {
		r.FinishedAt = time.Unix(0, finished.Int64).UTC()
	}
	return r, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Record, error) {
	r, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM copy_history WHERE id=?`, id))
	if err != nil {
		return Record{}, fmt.Errorf("get history record %d: %w", id, err)
	}
	return r, nil
}

// Search ANDs whitespace-separated, case-insensitive literal substrings across
// paths, status and error. Results are newest first; limit must be between 1 and 1000.
func (s *Store) Search(ctx context.Context, query string, limit int) (records []Record, err error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("history limit must be between 1 and 1000")
	}
	statement := `SELECT ` + recordColumns + ` FROM copy_history WHERE 1=1`
	args := []any{}
	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	for _, token := range strings.Fields(strings.ToLower(query)) {
		statement += ` AND (search_paths LIKE ? ESCAPE '\' OR status LIKE ? ESCAPE '\' OR search_error LIKE ? ESCAPE '\')`
		pattern := "%" + escape.Replace(token) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	statement += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("search history: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		r, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("read history result: %w", scanErr)
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history results: %w", err)
	}
	return records, nil
}
