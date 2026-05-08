// Package cache persists translations, transcripts, and jobs to SQLite.
// It is line-granular for translations so that retries do not waste calls
// on lines that already succeeded.
package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Cache struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS translations (
	cache_key       TEXT PRIMARY KEY,
	provider        TEXT NOT NULL,
	model           TEXT NOT NULL,
	source_lang     TEXT NOT NULL,
	target_lang     TEXT NOT NULL,
	original_text   TEXT NOT NULL,
	translated_text TEXT NOT NULL,
	created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_translations_provider
	ON translations(provider, target_lang);

CREATE TABLE IF NOT EXISTS transcripts (
	video_key    TEXT PRIMARY KEY,
	site         TEXT NOT NULL,
	title        TEXT,
	raw_json     TEXT NOT NULL,
	extracted_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS jobs (
	job_id           TEXT PRIMARY KEY,
	video_key        TEXT,
	provider         TEXT,
	model            TEXT,
	status           TEXT NOT NULL,
	total_chunks     INTEGER,
	completed_chunks INTEGER,
	failed_chunks    INTEGER,
	error_summary    TEXT,
	created_at       INTEGER NOT NULL,
	completed_at     INTEGER
);
`

// Open returns a Cache backed by the given SQLite path.
// Empty or ":memory:" gives an in-memory database (useful for tests).
func Open(path string) (*Cache, error) {
	dsn := path
	if path == "" {
		dsn = ":memory:"
	} else if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("ensure cache dir: %w", err)
		}
		dsn = path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite single-writer; keep it simple
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Cache{db: db}, nil
}

func (c *Cache) Close() error { return c.db.Close() }

// Key derives a deterministic cache key for a translation lookup.
// Source text is normalized (lowercase + collapsed whitespace) so that
// minor whitespace differences across runs still hit the cache.
func Key(provider, model, sourceLang, targetLang, originalText string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s",
		provider, model, sourceLang, targetLang, normalize(originalText))
	return hex.EncodeToString(h.Sum(nil))
}

func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// ─── translations ───────────────────────────────────────────────────────────

type TranslationEntry struct {
	Key            string
	Provider       string
	Model          string
	SourceLang     string
	TargetLang     string
	OriginalText   string
	TranslatedText string
}

// LookupTranslations returns a map of cache_key → translated_text for hits.
// Misses are simply absent from the map.
func (c *Cache) LookupTranslations(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	placeholders := strings.Repeat("?,", len(keys))
	placeholders = placeholders[:len(placeholders)-1]
	query := "SELECT cache_key, translated_text FROM translations WHERE cache_key IN (" + placeholders + ")"

	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("lookup translations: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string, len(keys))
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// StoreTranslations bulk-inserts entries, ignoring conflicts on cache_key.
func (c *Cache) StoreTranslations(ctx context.Context, entries []TranslationEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO translations
		(cache_key, provider, model, source_lang, target_lang, original_text, translated_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx,
			e.Key, e.Provider, e.Model, e.SourceLang, e.TargetLang,
			e.OriginalText, e.TranslatedText, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ─── transcripts ────────────────────────────────────────────────────────────

type Transcript struct {
	VideoKey    string
	Site        string
	Title       string
	RawJSON     string
	ExtractedAt time.Time
}

func (c *Cache) SaveTranscript(ctx context.Context, t Transcript) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO transcripts (video_key, site, title, raw_json, extracted_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(video_key) DO UPDATE SET
			site=excluded.site,
			title=excluded.title,
			raw_json=excluded.raw_json,
			extracted_at=excluded.extracted_at`,
		t.VideoKey, t.Site, t.Title, t.RawJSON, time.Now().Unix())
	return err
}

func (c *Cache) GetTranscript(ctx context.Context, videoKey string) (*Transcript, error) {
	row := c.db.QueryRowContext(ctx,
		`SELECT video_key, site, title, raw_json, extracted_at FROM transcripts WHERE video_key = ?`,
		videoKey)
	var t Transcript
	var ts int64
	if err := row.Scan(&t.VideoKey, &t.Site, &t.Title, &t.RawJSON, &ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	t.ExtractedAt = time.Unix(ts, 0)
	return &t, nil
}

// ─── jobs ───────────────────────────────────────────────────────────────────

type Job struct {
	ID              string
	VideoKey        string
	Provider        string
	Model           string
	Status          string
	TotalChunks     int
	CompletedChunks int
	FailedChunks    int
	ErrorSummary    string
	CreatedAt       time.Time
	CompletedAt     *time.Time
}

func (c *Cache) CreateJob(ctx context.Context, j Job) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO jobs (job_id, video_key, provider, model, status,
			total_chunks, completed_chunks, failed_chunks, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?)`,
		j.ID, j.VideoKey, j.Provider, j.Model, j.Status,
		j.TotalChunks, time.Now().Unix())
	return err
}

func (c *Cache) UpdateJob(ctx context.Context, id, status string, completed, failed int, summary string) error {
	var completedAt any
	if status == "completed" || status == "partial" || status == "failed" {
		completedAt = time.Now().Unix()
	}
	_, err := c.db.ExecContext(ctx, `
		UPDATE jobs SET status=?, completed_chunks=?, failed_chunks=?,
			error_summary=?, completed_at=?
		WHERE job_id=?`,
		status, completed, failed, summary, completedAt, id)
	return err
}

// ListJobs returns the most recent jobs first, capped at limit.
func (c *Cache) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT job_id, video_key, provider, model, status,
			total_chunks, completed_chunks, failed_chunks,
			error_summary, created_at, completed_at
		FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		var j Job
		var summary sql.NullString
		var createdAt int64
		var completedAt sql.NullInt64
		var videoKey, prov, model sql.NullString
		if err := rows.Scan(&j.ID, &videoKey, &prov, &model, &j.Status,
			&j.TotalChunks, &j.CompletedChunks, &j.FailedChunks,
			&summary, &createdAt, &completedAt); err != nil {
			return nil, err
		}
		j.VideoKey = videoKey.String
		j.Provider = prov.String
		j.Model = model.String
		j.ErrorSummary = summary.String
		j.CreatedAt = time.Unix(createdAt, 0)
		if completedAt.Valid {
			t := time.Unix(completedAt.Int64, 0)
			j.CompletedAt = &t
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (c *Cache) GetJob(ctx context.Context, id string) (*Job, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT job_id, video_key, provider, model, status,
			total_chunks, completed_chunks, failed_chunks,
			error_summary, created_at, completed_at
		FROM jobs WHERE job_id=?`, id)
	var j Job
	var summary sql.NullString
	var createdAt int64
	var completedAt sql.NullInt64
	if err := row.Scan(&j.ID, &j.VideoKey, &j.Provider, &j.Model, &j.Status,
		&j.TotalChunks, &j.CompletedChunks, &j.FailedChunks,
		&summary, &createdAt, &completedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	j.ErrorSummary = summary.String
	j.CreatedAt = time.Unix(createdAt, 0)
	if completedAt.Valid {
		t := time.Unix(completedAt.Int64, 0)
		j.CompletedAt = &t
	}
	return &j, nil
}

// ─── stats ──────────────────────────────────────────────────────────────────

type Stats struct {
	Translations int
	Transcripts  int
	Jobs         int
}

func (c *Cache) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM translations`).Scan(&s.Translations); err != nil {
		return s, err
	}
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcripts`).Scan(&s.Transcripts); err != nil {
		return s, err
	}
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&s.Jobs); err != nil {
		return s, err
	}
	return s, nil
}

func (c *Cache) Clear(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM translations; DELETE FROM transcripts; DELETE FROM jobs;`)
	return err
}
