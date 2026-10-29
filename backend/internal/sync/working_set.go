package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
	_ "github.com/mattn/go-sqlite3"
)

// workingSet holds inventory-sized, disposable state on disk, independently of
// the application database dialect. Its SQLite page cache is bounded to 2 MiB.
// Application rows remain authoritative; this file never commits sync progress.
type workingSet struct {
	ctx context.Context
	db  *sql.DB
	dir string
}

func newWorkingSet(ctx context.Context) (*workingSet, error) {
	dir, err := os.MkdirTemp("", "hindsight-scan-")
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(filepath.Join(dir, "work.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	path := (&url.URL{Scheme: "file", Path: uriPath}).String()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	w := &workingSet{ctx: ctx, db: db, dir: dir}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF; PRAGMA cache_size=-2048; PRAGMA mmap_size=0;
 CREATE TABLE work (bucket TEXT NOT NULL, id TEXT NOT NULL, value BLOB, PRIMARY KEY(bucket,id)) WITHOUT ROWID;`)
	if err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func (w *workingSet) close() {
	if w != nil {
		_ = w.db.Close()
		_ = os.RemoveAll(w.dir)
	}
}

func (w *workingSet) put(bucket, id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = w.db.ExecContext(w.ctx, "INSERT INTO work(bucket,id,value) VALUES(?,?,?) ON CONFLICT(bucket,id) DO UPDATE SET value=excluded.value", bucket, id, data)
	return err
}

func (w *workingSet) get(bucket, id string, value any) (bool, error) {
	var data []byte
	err := w.db.QueryRowContext(w.ctx, "SELECT value FROM work WHERE bucket=? AND id=?", bucket, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if value == nil {
		return true, nil
	}
	return true, json.Unmarshal(data, value)
}

// mark returns true only on the first observation of an ID.
func (w *workingSet) mark(id string) (bool, error) {
	res, err := w.db.ExecContext(w.ctx, "INSERT INTO work(bucket,id) VALUES('handled',?) ON CONFLICT DO NOTHING", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n != 0, err
}

func (w *workingSet) ledgerRows(rows []models.TaskItem) error {
	tx, err := w.db.BeginTx(w.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(w.ctx, "INSERT INTO work(bucket,id,value) VALUES('ledger',?,?) ON CONFLICT(bucket,id) DO UPDATE SET value=excluded.value")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err = stmt.ExecContext(w.ctx, row.SourceItemID, data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (w *workingSet) count(bucket string) (int, error) {
	var n int
	err := w.db.QueryRowContext(w.ctx, "SELECT COUNT(*) FROM work WHERE bucket=?", bucket).Scan(&n)
	return n, err
}

// each closes each page before invoking callbacks, allowing callbacks to write
// into other buckets without holding a cursor or growing a result slice.
func (w *workingSet) each(bucket string, visit func(string, []byte) error) error {
	last := ""
	first := true
	for {
		query := "SELECT id,value FROM work WHERE bucket=?"
		args := []any{bucket}
		if !first {
			query += " AND id>?"
			args = append(args, last)
		}
		query += " ORDER BY id LIMIT 100"
		rows, err := w.db.QueryContext(w.ctx, query, args...)
		if err != nil {
			return err
		}
		type record struct {
			id   string
			data []byte
		}
		page := make([]record, 0, 100)
		for rows.Next() {
			var r record
			if err = rows.Scan(&r.id, &r.data); err != nil {
				break
			}
			page = append(page, r)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for _, r := range page {
			if err := visit(r.id, r.data); err != nil {
				return err
			}
		}
		if len(page) < 100 {
			return nil
		}
		first = false
		last = page[len(page)-1].id
	}
}

func (r *runner) ledgerItem(id string) (models.TaskItem, bool, error) {
	var row models.TaskItem
	ok, err := r.work.get("ledger", id, &row)
	return row, ok, err
}

func (r *runner) queueDelete(row models.TaskItem) error {
	return r.work.put("delete", row.SourceItemID, row)
}

// Keep only a few bounded messages; the run counter records every failure.
func (r *runner) recordFailure(id, message string) {
	if len(r.failed) < 3 {
		r.failed[id] = strings.Clone(truncate(message, 2000))
	}
}
