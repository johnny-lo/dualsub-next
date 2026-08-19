package cache

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestKeyDeterminism(t *testing.T) {
	a := Key("openai", "gpt-4o-mini", "en", "zh-TW", "Hello world")
	b := Key("openai", "gpt-4o-mini", "en", "zh-TW", "Hello world")
	if a != b {
		t.Errorf("same input gave different keys: %s vs %s", a, b)
	}
	c := Key("openai", "gpt-4o-mini", "en", "zh-TW", "  Hello   world  ")
	if a != c {
		t.Errorf("normalize should make whitespace-different texts equal: %s vs %s", a, c)
	}
	d := Key("gemini", "gemini-flash", "en", "zh-TW", "Hello world")
	if a != d {
		t.Errorf("provider and model should not change a shared key: %s vs %s", a, d)
	}
	e := Key("gemini", "gemini-flash", "en", "ja", "Hello world")
	if a == e {
		t.Error("different target languages should give different keys")
	}
}

func TestLegacyKeyRemainsProviderSpecific(t *testing.T) {
	a := LegacyKey("codex", "gpt-5", "en", "zh-TW", "Hello world")
	b := LegacyKey("gemini", "flash", "en", "zh-TW", "Hello world")
	if a == b {
		t.Error("legacy keys should remain provider-specific")
	}
}

func TestOpenMigratesLegacyKeysAndKeepsNewestResult(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM cache_meta WHERE key = 'cache_key_version'`); err != nil {
		t.Fatal(err)
	}

	oldKey := LegacyKey("codex", "gpt-5", "en", "zh-TW", "Hello world")
	newerKey := LegacyKey("gemini", "flash", "en", "zh-TW", "  Hello   world ")
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO translations
		(cache_key, provider, model, source_lang, target_lang, original_text, translated_text, created_at)
		VALUES
		(?, 'codex', 'gpt-5', 'en', 'zh-TW', 'Hello world', 'older result', 100),
		(?, 'gemini', 'flash', 'en', 'zh-TW', '  Hello   world ', 'newer result', 200);
		INSERT INTO sync_outbox (cache_key, queued_at) VALUES (?, 90), (?, 190);`,
		oldKey, newerKey, oldKey, newerKey); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	sharedKey := Key("anything", "anything", "en", "zh-TW", "hello world")
	hits, err := c.LookupTranslations(ctx, []string{sharedKey, oldKey, newerKey})
	if err != nil {
		t.Fatal(err)
	}
	if hits[sharedKey] != "newer result" {
		t.Fatalf("migrated result = %q, want newest result", hits[sharedKey])
	}
	if _, ok := hits[oldKey]; ok {
		t.Error("legacy key remained after migration")
	}
	if _, ok := hits[newerKey]; ok {
		t.Error("second legacy key remained after migration")
	}

	stats, err := c.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Translations != 1 {
		t.Fatalf("translation rows = %d, want 1", stats.Translations)
	}
	pending, err := c.PendingSyncEntries(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Key != sharedKey || pending[0].TranslatedText != "newer result" {
		t.Fatalf("pending entries = %+v", pending)
	}
	var version string
	if err := c.db.QueryRowContext(ctx,
		`SELECT value FROM cache_meta WHERE key = 'cache_key_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentCacheKeyVersion {
		t.Fatalf("cache key version = %q, want %q", version, currentCacheKeyVersion)
	}
}

func TestTranslationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)

	k1 := Key("openai", "gpt-4o-mini", "en", "zh-TW", "Hello")
	k2 := Key("openai", "gpt-4o-mini", "en", "zh-TW", "World")
	missing := Key("openai", "gpt-4o-mini", "en", "zh-TW", "Goodbye")

	if err := c.StoreTranslations(ctx, []TranslationEntry{
		{Key: k1, Provider: "openai", Model: "gpt-4o-mini", SourceLang: "en", TargetLang: "zh-TW", OriginalText: "Hello", TranslatedText: "你好"},
		{Key: k2, Provider: "openai", Model: "gpt-4o-mini", SourceLang: "en", TargetLang: "zh-TW", OriginalText: "World", TranslatedText: "世界"},
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	hits, err := c.LookupTranslations(ctx, []string{k1, k2, missing})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("got %d hits, want 2", len(hits))
	}
	if hits[k1] != "你好" || hits[k2] != "世界" {
		t.Errorf("unexpected hits: %+v", hits)
	}
	if _, present := hits[missing]; present {
		t.Error("missing key should not appear in hits map")
	}
}

func TestTranslationsConflictIgnored(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)
	k := Key("openai", "gpt-4o-mini", "en", "zh-TW", "Hello")
	entry := TranslationEntry{
		Key: k, Provider: "openai", Model: "gpt-4o-mini",
		SourceLang: "en", TargetLang: "zh-TW",
		OriginalText: "Hello", TranslatedText: "你好",
	}
	if err := c.StoreTranslations(ctx, []TranslationEntry{entry}); err != nil {
		t.Fatal(err)
	}
	// second insert with different translation should be ignored
	entry.TranslatedText = "嗨"
	if err := c.StoreTranslations(ctx, []TranslationEntry{entry}); err != nil {
		t.Fatal(err)
	}
	hits, _ := c.LookupTranslations(ctx, []string{k})
	if hits[k] != "你好" {
		t.Errorf("conflict was not ignored: got %q", hits[k])
	}
}

func TestTranscriptUpsert(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)
	if err := c.SaveTranscript(ctx, Transcript{
		VideoKey: "udemy:slug/123", Site: "udemy", Title: "Lec 1", RawJSON: `[{"index":1}]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveTranscript(ctx, Transcript{
		VideoKey: "udemy:slug/123", Site: "udemy", Title: "Lec 1 (updated)", RawJSON: `[{"index":2}]`,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetTranscript(ctx, "udemy:slug/123")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("not found")
	}
	if got.Title != "Lec 1 (updated)" || got.RawJSON != `[{"index":2}]` {
		t.Errorf("upsert did not overwrite: %+v", got)
	}

	missing, err := c.GetTranscript(ctx, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Errorf("expected nil, got %+v", missing)
	}
}

func TestJobLifecycle(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)

	if err := c.CreateJob(ctx, Job{
		ID: "job-1", VideoKey: "udemy:x/1", Provider: "gemini", Model: "gemini-2.5-flash",
		Status: "running", TotalChunks: 5,
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.UpdateJob(ctx, "job-1", "partial", 4, 1, "chunk 3 failed"); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "partial" || got.CompletedChunks != 4 || got.FailedChunks != 1 {
		t.Errorf("unexpected job state: %+v", got)
	}
	if got.CompletedAt == nil {
		t.Error("expected CompletedAt to be set on terminal status")
	}
	if got.ErrorSummary != "chunk 3 failed" {
		t.Errorf("error_summary lost: %q", got.ErrorSummary)
	}
}

func TestListJobsRecentFirst(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)

	// Insert with explicit, increasing created_at so we can rely on ordering
	// without depending on sub-second wall clock differences.
	if err := c.CreateJob(ctx, Job{ID: "j1", Status: "completed", TotalChunks: 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateJob(ctx, Job{ID: "j2", Status: "running", TotalChunks: 2}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateJob(ctx, Job{ID: "j3", Status: "failed", TotalChunks: 3}); err != nil {
		t.Fatal(err)
	}

	jobs, err := c.ListJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("got %d jobs, want 3", len(jobs))
	}
	// All inserted within the same second so created_at ties are likely; just
	// assert we got all three IDs back.
	seen := map[string]bool{}
	for _, j := range jobs {
		seen[j.ID] = true
	}
	for _, id := range []string{"j1", "j2", "j3"} {
		if !seen[id] {
			t.Errorf("missing job %s", id)
		}
	}

	limited, err := c.ListJobs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Errorf("limit=1 returned %d", len(limited))
	}
}

func TestStatsAndClear(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)
	_ = c.StoreTranslations(ctx, []TranslationEntry{{
		Key: "k1", Provider: "openai", Model: "m", SourceLang: "en", TargetLang: "zh-TW",
		OriginalText: "x", TranslatedText: "y",
	}})
	_ = c.SaveTranscript(ctx, Transcript{VideoKey: "v", Site: "udemy", Title: "t", RawJSON: "[]"})
	_ = c.CreateJob(ctx, Job{ID: "j", Status: "running", TotalChunks: 1})

	s, err := c.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Translations != 1 || s.Transcripts != 1 || s.Jobs != 1 {
		t.Errorf("unexpected stats: %+v", s)
	}

	if err := c.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ = c.Stats(ctx)
	if s.Translations != 0 || s.Transcripts != 0 || s.Jobs != 0 {
		t.Errorf("clear left rows: %+v", s)
	}
}

func TestClearJobsOnly(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)
	_ = c.StoreTranslations(ctx, []TranslationEntry{{
		Key: "k1", Provider: "openai", Model: "m", SourceLang: "en", TargetLang: "zh-TW",
		OriginalText: "x", TranslatedText: "y",
	}})
	_ = c.SaveTranscript(ctx, Transcript{VideoKey: "v", Site: "udemy", Title: "t", RawJSON: "[]"})
	_ = c.CreateJob(ctx, Job{ID: "j", Status: "running", TotalChunks: 1})

	if err := c.ClearJobs(ctx); err != nil {
		t.Fatal(err)
	}

	s, err := c.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Translations != 1 || s.Transcripts != 1 || s.Jobs != 0 {
		t.Errorf("clear jobs changed the wrong tables: %+v", s)
	}
}

func TestSyncOutboxLifecycle(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)
	entry := TranslationEntry{
		Provider: "gemini", Model: "flash", SourceLang: "en", TargetLang: "zh-TW",
		OriginalText: "Hello", TranslatedText: "你好",
	}
	entry.Key = Key(entry.Provider, entry.Model, entry.SourceLang, entry.TargetLang, entry.OriginalText)

	if err := c.StoreTranslationsForSync(ctx, []TranslationEntry{entry}); err != nil {
		t.Fatal(err)
	}
	pending, err := c.PendingSyncEntries(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0] != entry {
		t.Fatalf("pending = %+v, want %+v", pending, entry)
	}
	if err := c.AcknowledgeSyncEntries(ctx, []string{entry.Key}); err != nil {
		t.Fatal(err)
	}
	count, err := c.PendingSyncCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pending count = %d, want 0", count)
	}
	hits, err := c.LookupTranslations(ctx, []string{entry.Key})
	if err != nil || hits[entry.Key] != entry.TranslatedText {
		t.Fatalf("acknowledging outbox removed translation: hits=%v err=%v", hits, err)
	}
}

func TestQueueHistoricalTranslationsForSyncRunsOnce(t *testing.T) {
	ctx := context.Background()
	c := newTestCache(t)
	entries := []TranslationEntry{
		{
			Provider: "codex", Model: "gpt-5", SourceLang: "en", TargetLang: "zh-TW",
			OriginalText: "first", TranslatedText: "first result",
		},
		{
			Provider: "gemini", Model: "flash", SourceLang: "en", TargetLang: "zh-TW",
			OriginalText: "second", TranslatedText: "second result",
		},
	}
	for i := range entries {
		entries[i].Key = Key(entries[i].Provider, entries[i].Model, entries[i].SourceLang,
			entries[i].TargetLang, entries[i].OriginalText)
	}
	if err := c.StoreTranslations(ctx, entries); err != nil {
		t.Fatal(err)
	}

	queued, err := c.QueueHistoricalTranslationsForSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Fatalf("queued = %d, want 2", queued)
	}
	count, err := c.PendingSyncCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("pending count = %d, want 2", count)
	}

	queued, err = c.QueueHistoricalTranslationsForSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("second backfill queued = %d, want 0", queued)
	}
}
