// Package store keeps the SQLite metadata index and implements the
// WebDAV virtual filesystem on top of Telegram storage.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Entry is one row of the virtual filesystem (file or folder).
type Entry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Folder   string    `json:"folder,omitempty"`
	Size     int64     `json:"size"`
	Mime     string    `json:"mime"`
	Kind     string    `json:"kind"`
	MsgID    int64     `json:"msg_id"`
	Modified time.Time `json:"modified"`
	IsDir    bool      `json:"is_dir"`
}

// ErrNotExist is returned when a path is not indexed.
var ErrNotExist = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open creates/opens the metadata database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// single connection keeps sqlite perfectly happy and is fast enough
	// for metadata ops (µs level)
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS files (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	path       TEXT NOT NULL UNIQUE,
	folder     TEXT NOT NULL,
	name       TEXT NOT NULL,
	msg_id     INTEGER NOT NULL DEFAULT 0,
	size       INTEGER NOT NULL DEFAULT 0,
	mime       TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT 'file',
	caption    TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_folder ON files(folder);
CREATE UNIQUE INDEX IF NOT EXISTS idx_files_folder_name ON files(folder, name);
CREATE TABLE IF NOT EXISTS folders (
	path       TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`)
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Normalize cleans a virtual path: always leading "/", no trailing "/",
// no "..", root is "/". Returns "" when the path is invalid.
func Normalize(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if strings.ContainsRune(p, 0) {
		return ""
	}
	p = path.Clean("/" + strings.TrimLeft(p, "/"))
	if p == "." || p == "//" {
		return "/"
	}
	// literal ".." segments that survived cleaning are still invalid names
	if p == ".." || strings.HasSuffix(p, "/..") || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

// Parent returns the parent folder of a normalized path.
func Parent(p string) string {
	if p == "/" {
		return "/"
	}
	return path.Dir(p)
}

// Base returns the last element of a normalized path.
func Base(p string) string {
	if p == "/" {
		return "/"
	}
	return path.Base(p)
}

// EnsureFolder creates a folder (and parents) if missing.
func (s *Store) EnsureFolder(ctx context.Context, p string) error {
	p = Normalize(p)
	if p == "" {
		return ErrNotExist
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for cur := p; ; cur = Parent(cur) {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO folders(path, created_at) VALUES(?, ?)`, cur, now); err != nil {
			return err
		}
		if cur == "/" {
			break
		}
	}
	return tx.Commit()
}

// FolderExists reports whether the folder path is indexed.
func (s *Store) FolderExists(ctx context.Context, p string) (bool, error) {
	p = Normalize(p)
	if p == "" {
		return false, ErrNotExist
	}
	if p == "/" {
		return true, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM folders WHERE path = ?`, p).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// List returns the folders and files directly inside dir.
func (s *Store) List(ctx context.Context, dir string) ([]Entry, error) {
	dir = Normalize(dir)
	if dir == "" {
		return nil, ErrNotExist
	}
	if _, err := s.FolderExists(ctx, dir); err != nil {
		return nil, err
	}
	prefix := dir
	if dir != "/" {
		prefix += "/"
	}

	out := []Entry{}
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, created_at FROM folders WHERE path != '/' ORDER BY path`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		var created int64
		if err := rows.Scan(&p, &created); err != nil {
			rows.Close()
			return nil, err
		}
		if Parent(p) == dir {
			out = append(out, Entry{Name: Base(p), Path: p, IsDir: true, Modified: time.Unix(created, 0)})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	frows, err := s.db.QueryContext(ctx, `
		SELECT name, path, size, mime, kind, msg_id, updated_at
		FROM files WHERE folder = ? ORDER BY name COLLATE NOCASE`, dir)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		e := Entry{Folder: dir}
		var updated int64
		if err := frows.Scan(&e.Name, &e.Path, &e.Size, &e.Mime, &e.Kind, &e.MsgID, &updated); err != nil {
			return nil, err
		}
		e.Modified = time.Unix(updated, 0)
		out = append(out, e)
	}
	return out, frows.Err()
}

// Stat returns the entry for path (file or folder).
func (s *Store) Stat(ctx context.Context, p string) (*Entry, error) {
	p = Normalize(p)
	if p == "" {
		return nil, ErrNotExist
	}
	if p == "/" {
		return &Entry{Name: "", Path: "/", IsDir: true}, nil
	}
	ok, err := s.FolderExists(ctx, p)
	if err != nil {
		return nil, err
	}
	if ok {
		return &Entry{Name: Base(p), Path: p, IsDir: true, Modified: time.Now()}, nil
	}
	e := &Entry{}
	var updated int64
	err = s.db.QueryRowContext(ctx, `
		SELECT name, folder, path, size, mime, kind, msg_id, updated_at
		FROM files WHERE path = ?`, p).
		Scan(&e.Name, &e.Folder, &e.Path, &e.Size, &e.Mime, &e.Kind, &e.MsgID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	e.Modified = time.Unix(updated, 0)
	return e, nil
}

// PutFileUnique inserts a file; when the name already exists inside the
// folder it is renamed "name (2).ext", "name (3).ext", ...
func (s *Store) PutFileUnique(ctx context.Context, folder, name string, msgID, size int64, mime, kind, caption string) (Entry, error) {
	folder = Normalize(folder)
	base := Base(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	candidate := base
	for i := 2; ; i++ {
		e, err := s.insertFile(ctx, folder, candidate, msgID, size, mime, kind, caption)
		if err == nil {
			return e, nil
		}
		if !isUniqueErr(err) {
			return Entry{}, err
		}
		if i > 999 {
			return Entry{}, fmt.Errorf("too many collisions for %s", base)
		}
		candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
	}
}

// PutFileOverwrite inserts or replaces a file at an exact path.
// Returns the stored entry plus the message id of the replaced file (0 if none).
func (s *Store) PutFileOverwrite(ctx context.Context, folder, name string, msgID, size int64, mime, kind, caption string) (Entry, int64, error) {
	folder = Normalize(folder)
	p := Normalize(folder + "/" + name)
	var oldMsg int64
	err := s.db.QueryRowContext(ctx, `SELECT msg_id FROM files WHERE path = ?`, p).Scan(&oldMsg)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Entry{}, 0, err
	}
	if oldMsg != 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, p); err != nil {
			return Entry{}, 0, err
		}
	}
	e, err := s.insertFile(ctx, folder, name, msgID, size, mime, kind, caption)
	return e, oldMsg, err
}

func (s *Store) insertFile(ctx context.Context, folder, name string, msgID, size int64, mime, kind, caption string) (Entry, error) {
	now := time.Now().Unix()
	p := Normalize(folder + "/" + name)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO files(path, folder, name, msg_id, size, mime, kind, caption, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		p, folder, name, msgID, size, mime, kind, caption, now, now)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		Name: name, Path: p, Size: size, Mime: mime, Kind: kind,
		MsgID: msgID, Modified: time.Unix(now, 0),
	}, nil
}

func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "unique")
}

// Rename moves/renames a file or folder (folders cascade).
func (s *Store) Rename(ctx context.Context, oldPath, newPath string) error {
	oldPath, newPath = Normalize(oldPath), Normalize(newPath)
	if oldPath == "" || newPath == "" || oldPath == "/" || newPath == "/" {
		return ErrNotExist
	}
	if oldPath == newPath {
		return nil
	}
	if strings.HasPrefix(newPath, oldPath+"/") {
		return errors.New("cannot move a folder into itself")
	}
	if _, err := s.Stat(ctx, newPath); err == nil {
		return os.ErrExist
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// folder rename: cascade
	res, err := tx.ExecContext(ctx, `UPDATE folders SET path = ? WHERE path = ?`, newPath, oldPath)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// plain file rename
		_, err = tx.ExecContext(ctx, `
			UPDATE files SET path = ?, folder = ?, name = ?, updated_at = ? WHERE path = ?`,
			newPath, Parent(newPath), Base(newPath), time.Now().Unix(), oldPath)
		if err != nil {
			return err
		}
		return tx.Commit()
	}

	// children folders
	if _, err := tx.ExecContext(ctx, `
		UPDATE folders SET path = ? || substr(path, ?)
		WHERE path LIKE ? || '/%'`, newPath, len(oldPath)+1, oldPath); err != nil {
		return err
	}
	// direct children files keep folder = newPath
	if _, err := tx.ExecContext(ctx, `
		UPDATE files SET folder = ?, path = ? || '/' || name, updated_at = ?
		WHERE folder = ?`, newPath, newPath, time.Now().Unix(), oldPath); err != nil {
		return err
	}
	// deeper files
	if _, err := tx.ExecContext(ctx, `
		UPDATE files SET folder = ? || substr(folder, ?),
		                  path = ? || substr(path, ?),
		                  updated_at = ?
		WHERE folder LIKE ? || '/%'`,
		newPath, len(oldPath)+1, newPath, len(oldPath)+1, time.Now().Unix(), oldPath); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes a file or folder (cascade). Returns the telegram message
// ids that belonged to the deleted entries.
func (s *Store) Delete(ctx context.Context, p string) ([]int64, error) {
	p = Normalize(p)
	if p == "" || p == "/" {
		return nil, errors.New("cannot delete root")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var msgIDs []int64
	// folder?
	res, err := tx.ExecContext(ctx, `DELETE FROM folders WHERE path = ?`, p)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		rows, err := tx.QueryContext(ctx, `
			SELECT msg_id FROM files
			WHERE folder = ? OR folder LIKE ? || '/%'`, p, p)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if id != 0 {
				msgIDs = append(msgIDs, id)
			}
		}
		rows.Close()
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM files WHERE folder = ? OR folder LIKE ? || '/%'`, p, p); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return msgIDs, nil
	}

	// plain file
	var msgID int64
	if err := tx.QueryRowContext(ctx, `SELECT msg_id FROM files WHERE path = ?`, p).Scan(&msgID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	r2, err := tx.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, p)
	if err != nil {
		return nil, err
	}
	if n, _ := r2.RowsAffected(); n == 0 {
		return nil, ErrNotExist
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if msgID != 0 {
		msgIDs = append(msgIDs, msgID)
	}
	return msgIDs, nil
}

// SetSetting stores a key/value pair.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetSetting reads a setting; returns "" when missing.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetDefaultFolder updates the ingest default folder.
func (s *Store) SetDefaultFolder(ctx context.Context, folder string) error {
	return s.SetSetting(ctx, "default_folder", Normalize("/"+strings.Trim(folder, "/")))
}

// DefaultFolder returns the ingest default folder (with fallback).
func (s *Store) DefaultFolder(ctx context.Context, fallback string) (string, error) {
	v, err := s.GetSetting(ctx, "default_folder")
	if err != nil {
		return "", err
	}
	if v == "" {
		v = Normalize("/" + strings.Trim(fallback, "/"))
	}
	return v, nil
}

// SeedDefaultFolder stores the default folder setting only when unset.
func (s *Store) SeedDefaultFolder(ctx context.Context, fallback string) error {
	cur, err := s.GetSetting(ctx, "default_folder")
	if err != nil || cur != "" {
		return err
	}
	return s.SetDefaultFolder(ctx, fallback)
}

// Stats returns total files, bytes and folders.
func (s *Store) Stats(ctx context.Context) (files int, bytes int64, folders int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size),0) FROM files`).Scan(&files, &bytes)
	if err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM folders WHERE path != '/'`).Scan(&folders)
	return
}

// AllMsgIDs returns every telegram message id in the index (used to purge).
func (s *Store) AllMsgIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT msg_id FROM files WHERE msg_id != 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
