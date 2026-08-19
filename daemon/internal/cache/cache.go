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

const (
	currentCacheKeyVersion     = "2"
	currentSyncBackfillVersion = "1"
)

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

CREATE TABLE IF NOT EXISTS sync_outbox (
	cache_key TEXT PRIMARY KEY,
	queued_at INTEGER NOT NULL,
	FOREIGN KEY(cache_key) REFERENCES translations(cache_key) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS cache_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
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
	c := &Cache{db: db}
	if err := c.migrateCacheKeys(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate cache keys: %w", err)
	}
	return c, nil
}

func (c *Cache) Close() error { return c.db.Close() }

// Key derives a provider-independent key so all translation methods share
// results. Provider and model remain parameters for call-site compatibility.
func Key(_, _, sourceLang, targetLang, originalText string) string {
	h := sha256.New()
	fmt.Fprintf(h, "shared-v%s|%s|%s|%s",
		currentCacheKeyVersion, sourceLang, targetLang, normalize(originalText))
	return hex.EncodeToString(h.Sum(nil))
}

// LegacyKey derives the provider-specific key used before cache key version 2.
// The shared server accepts it only for compatibility with older clients.
func LegacyKey(provider, model, sourceLang, targetLang, originalText string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s",
		provider, model, sourceLang, targetLang, normalize(originalText))
	return hex.EncodeToString(h.Sum(nil))
}

func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

type migrationTranslation struct {
	key            string
	provider       string
	model          string
	sourceLang     string
	targetLang     string
	originalText   string
	translatedText string
	createdAt      int64
	queuedAt       sql.NullInt64
}

func (c *Cache) migrateCacheKeys(ctx context.Context) error {
	var version string
	err := c.db.QueryRowContext(ctx,
		`SELECT value FROM cache_meta WHERE key = 'cache_key_version'`).Scan(&version)
	if err == nil {
		if version != currentCacheKeyVersion {
			return fmt.Errorf("unsupported cache key version %q", version)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	rows, err := c.db.QueryContext(ctx, `
		SELECT t.cache_key, t.provider, t.model, t.source_lang, t.target_lang,
		       t.original_text, t.translated_text, t.created_at, o.queued_at
		FROM translations t
		LEFT JOIN sync_outbox o ON o.cache_key = t.cache_key
		ORDER BY t.created_at DESC, t.cache_key DESC`)
	if err != nil {
		return err
	}
	var existing []migrationTranslation
	for rows.Next() {
		var row migrationTranslation
		if err := rows.Scan(&row.key, &row.provider, &row.model, &row.sourceLang,
			&row.targetLang, &row.originalText, &row.translatedText, &row.createdAt,
			&row.queuedAt); err != nil {
			_ = rows.Close()
			return err
		}
		existing = append(existing, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(existing) == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cache_meta (key, value) VALUES ('cache_key_version', ?)`,
			currentCacheKeyVersion); err != nil {
			return err
		}
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE translations_v2 (
			cache_key       TEXT PRIMARY KEY,
			provider        TEXT NOT NULL,
			model           TEXT NOT NULL,
			source_lang     TEXT NOT NULL,
			target_lang     TEXT NOT NULL,
			original_text   TEXT NOT NULL,
			translated_text TEXT NOT NULL,
			created_at      INTEGER NOT NULL
		)`); err != nil {
		return err
	}

	insert, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO translations_v2
		(cache_key, provider, model, source_lang, target_lang, original_text, translated_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	queued := make(map[string]int64)
	for _, row := range existing {
		key := Key(row.provider, row.model, row.sourceLang, row.targetLang, row.originalText)
		if _, err := insert.ExecContext(ctx, key, row.provider, row.model, row.sourceLang,
			row.targetLang, row.originalText, row.translatedText, row.createdAt); err != nil {
			_ = insert.Close()
			return err
		}
		if row.queuedAt.Valid {
			if current, ok := queued[key]; !ok || row.queuedAt.Int64 < current {
				queued[key] = row.queuedAt.Int64
			}
		}
	}
	if err := insert.Close(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		DROP TABLE sync_outbox;
		DROP TABLE translations;
		ALTER TABLE translations_v2 RENAME TO translations;
		CREATE INDEX idx_translations_provider ON translations(provider, target_lang);
		CREATE TABLE sync_outbox (
			cache_key TEXT PRIMARY KEY,
			queued_at INTEGER NOT NULL,
			FOREIGN KEY(cache_key) REFERENCES translations(cache_key) ON DELETE CASCADE
		);`); err != nil {
		return err
	}
	for key, queuedAt := range queued {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sync_outbox (cache_key, queued_at) VALUES (?, ?)`, key, queuedAt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cache_meta (key, value) VALUES ('cache_key_version', ?)`,
		currentCacheKeyVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// ─── translations ───────────────────────────────────────────────────────────

type TranslationEntry struct {
	Key            string `json:"cache_key"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	SourceLang     string `json:"source_lang"`
	TargetLang     string `json:"target_lang"`
	OriginalText   string `json:"original_text"`
	TranslatedText string `json:"translated_text"`
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
	return c.storeTranslations(ctx, entries, false)
}

// StoreTranslationsForSync atomically saves local fallback results and marks
// them for eventual upload to the shared cache.
func (c *Cache) StoreTranslationsForSync(ctx context.Context, entries []TranslationEntry) error {
	return c.storeTranslations(ctx, entries, true)
}

// QueueHistoricalTranslationsForSync marks translations created before shared
// caching was enabled for one-time upload. Later fallback writes use the normal
// atomic outbox path.
func (c *Cache) QueueHistoricalTranslationsForSync(ctx context.Context) (int, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var version string
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM cache_meta WHERE key = 'sync_backfill_version'`).Scan(&version)
	if err == nil && version == currentSyncBackfillVersion {
		return 0, tx.Commit()
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO sync_outbox (cache_key, queued_at)
		SELECT cache_key, ? FROM translations`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cache_meta (key, value) VALUES ('sync_backfill_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		currentSyncBackfillVersion); err != nil {
		return 0, err
	}
	queued, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(queued), nil
}

func (c *Cache) storeTranslations(ctx context.Context, entries []TranslationEntry, queue bool) error {
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
	var outbox *sql.Stmt
	if queue {
		outbox, err = tx.PrepareContext(ctx, `
			INSERT OR IGNORE INTO sync_outbox (cache_key, queued_at) VALUES (?, ?)`)
		if err != nil {
			return err
		}
		defer outbox.Close()
	}

	now := time.Now().Unix()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx,
			e.Key, e.Provider, e.Model, e.SourceLang, e.TargetLang,
			e.OriginalText, e.TranslatedText, now); err != nil {
			return err
		}
		if outbox != nil {
			if _, err := outbox.ExecContext(ctx, e.Key, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// PendingSyncEntries returns local fallback translations waiting for upload.
func (c *Cache) PendingSyncEntries(ctx context.Context, limit int) ([]TranslationEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT t.cache_key, t.provider, t.model, t.source_lang, t.target_lang,
		       t.original_text, t.translated_text
		FROM sync_outbox o
		JOIN translations t ON t.cache_key = o.cache_key
		ORDER BY o.queued_at ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list sync outbox: %w", err)
	}
	defer rows.Close()

	var entries []TranslationEntry
	for rows.Next() {
		var e TranslationEntry
		if err := rows.Scan(&e.Key, &e.Provider, &e.Model, &e.SourceLang, &e.TargetLang,
			&e.OriginalText, &e.TranslatedText); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// AcknowledgeSyncEntries removes successfully uploaded entries from the outbox.
func (c *Cache) AcknowledgeSyncEntries(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}
	_, err := c.db.ExecContext(ctx, "DELETE FROM sync_outbox WHERE cache_key IN ("+placeholders+")", args...)
	return err
}

func (c *Cache) PendingSyncCount(ctx context.Context) (int, error) {
	var count int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_outbox`).Scan(&count)
	return count, err
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
	_, err := c.db.ExecContext(ctx, `DELETE FROM sync_outbox; DELETE FROM translations; DELETE FROM transcripts; DELETE FROM jobs;`)
	return err
}

func (c *Cache) ClearJobs(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM jobs;`)
	return err
}
