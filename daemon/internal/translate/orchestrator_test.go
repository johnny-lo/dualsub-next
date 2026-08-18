package translate

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

// ─── mock provider ──────────────────────────────────────────────────────────

type mockResponse struct {
	err          error
	lines        []provider.TranslatedLine
	queueForSync bool
}

type mockProvider struct {
	name         string
	defaultModel string
	queue        []mockResponse
	calls        atomic.Int32
	modelsMu     sync.Mutex
	models       []string
	block        chan struct{}
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) DefaultModel() string { return m.defaultModel }

func (m *mockProvider) Translate(_ context.Context, in provider.Request) (provider.Response, error) {
	m.modelsMu.Lock()
	m.models = append(m.models, in.Model)
	m.modelsMu.Unlock()
	if m.block != nil {
		<-m.block
	}

	n := int(m.calls.Add(1)) - 1
	if n >= len(m.queue) {
		return provider.Response{}, errors.New("mock: out of responses")
	}
	r := m.queue[n]
	if r.err != nil {
		return provider.Response{}, r.err
	}
	return provider.Response{Lines: r.lines, QueueForSync: r.queueForSync}, nil
}

// ─── helpers ────────────────────────────────────────────────────────────────

func newOrch(t *testing.T, m *mockProvider) (*Orchestrator, *cache.Cache) {
	t.Helper()
	c, err := cache.Open(":memory:")
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	o := New(map[string]provider.Provider{m.name: m}, c, Config{
		ChunkSize:   30,
		Concurrency: 2,
		MaxAttempts: 3,
	})
	o.sleep = func(_ time.Duration) {} // no-op for tests
	return o, c
}

func collect(t *testing.T, run func(out chan<- Event) error) []Event {
	t.Helper()
	out := make(chan Event, 64)
	errCh := make(chan error, 1)
	go func() { errCh <- run(out) }()
	var events []Event
	for e := range out {
		events = append(events, e)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("translate returned error: %v", err)
	}
	return events
}

func countByType(events []Event, t EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == t {
			n++
		}
	}
	return n
}

func mkLines(n int) []provider.Line {
	out := make([]provider.Line, n)
	for i := range out {
		out[i] = provider.Line{Index: i + 1, Text: textFor(i + 1)}
	}
	return out
}

func textFor(i int) string { return "line " + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

func translatedFor(lines []provider.Line) []provider.TranslatedLine {
	out := make([]provider.TranslatedLine, len(lines))
	for i, l := range lines {
		out[i] = provider.TranslatedLine{Index: l.Index, Text: "[t]" + l.Text}
	}
	return out
}

// ─── tests ──────────────────────────────────────────────────────────────────

func TestHappyPath(t *testing.T) {
	lines := mkLines(3)
	m := &mockProvider{name: "mock", queue: []mockResponse{
		{lines: translatedFor(lines)},
	}}
	o, c := newOrch(t, m)

	events := collect(t, func(out chan<- Event) error {
		return o.Translate(context.Background(), Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})

	if got := countByType(events, EventJobCreated); got != 1 {
		t.Errorf("job-created count = %d, want 1", got)
	}
	if got := countByType(events, EventChunkDone); got != 1 {
		t.Errorf("chunk-done count = %d, want 1", got)
	}
	if got := countByType(events, EventDone); got != 1 {
		t.Errorf("done count = %d, want 1", got)
	}

	// Cache should now have 3 entries.
	stats, _ := c.Stats(context.Background())
	if stats.Translations != 3 {
		t.Errorf("cache has %d entries, want 3", stats.Translations)
	}
}

func TestCacheHits(t *testing.T) {
	ctx := context.Background()
	lines := mkLines(5)

	// Pre-populate cache for indices 1, 2, 3 under "mock" provider.
	c, _ := cache.Open(":memory:")
	defer c.Close()
	pre := make([]cache.TranslationEntry, 3)
	for i := 0; i < 3; i++ {
		pre[i] = cache.TranslationEntry{
			Key:        cache.Key("mock", "", "en", "zh-TW", lines[i].Text),
			Provider:   "mock",
			SourceLang: "en", TargetLang: "zh-TW",
			OriginalText:   lines[i].Text,
			TranslatedText: "[cached]" + lines[i].Text,
		}
	}
	if err := c.StoreTranslations(ctx, pre); err != nil {
		t.Fatal(err)
	}

	m := &mockProvider{name: "mock", queue: []mockResponse{
		{lines: translatedFor(lines[3:])}, // only the 2 uncached lines
	}}
	o := New(map[string]provider.Provider{"mock": m}, c, Config{
		ChunkSize: 30, Concurrency: 1, MaxAttempts: 3,
	})
	o.sleep = func(_ time.Duration) {}

	events := collect(t, func(out chan<- Event) error {
		return o.Translate(ctx, Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})

	// Provider should have been called exactly once.
	if got := m.calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}

	// Should have job-created (with cache_hits=3, total_chunks=1),
	// 1 chunk-done for cache, 1 chunk-done for LLM, 1 done.
	if got := countByType(events, EventChunkDone); got != 2 {
		t.Errorf("chunk-done count = %d, want 2", got)
	}
	for _, e := range events {
		if e.Type == EventJobCreated {
			p := e.Payload.(JobCreatedPayload)
			if p.CacheHits != 3 || p.TotalChunks != 1 || p.TotalLines != 5 {
				t.Errorf("unexpected job-created: %+v", p)
			}
		}
	}
}

func TestDefaultModelUsedForProviderRequestAndCacheKey(t *testing.T) {
	ctx := context.Background()
	lines := mkLines(1)
	m := &mockProvider{name: "mock", defaultModel: "model-a", queue: []mockResponse{
		{lines: translatedFor(lines)},
	}}
	o, c := newOrch(t, m)

	events := collect(t, func(out chan<- Event) error {
		return o.Translate(ctx, Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})

	if got := countByType(events, EventChunkDone); got != 1 {
		t.Fatalf("chunk-done count = %d, want 1", got)
	}
	if got := m.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	if len(m.models) != 1 || m.models[0] != "model-a" {
		t.Fatalf("provider saw models %v, want [model-a]", m.models)
	}

	modelKey := cache.Key("mock", "model-a", "en", "zh-TW", lines[0].Text)
	modelHits, err := c.LookupTranslations(ctx, []string{modelKey})
	if err != nil {
		t.Fatal(err)
	}
	if got := modelHits[modelKey]; got == "" {
		t.Fatal("translation was not cached under the provider default model")
	}

	blankKey := cache.Key("mock", "", "en", "zh-TW", lines[0].Text)
	blankHits, err := c.LookupTranslations(ctx, []string{blankKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := blankHits[blankKey]; ok {
		t.Fatal("translation was also cached under an empty model")
	}
}

func TestAllCacheHitsDoNotCallProviderAndReportZeroChunks(t *testing.T) {
	ctx := context.Background()
	lines := mkLines(3)
	model := "model-a"

	c, err := cache.Open(":memory:")
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	defer c.Close()

	entries := make([]cache.TranslationEntry, len(lines))
	for i, line := range lines {
		entries[i] = cache.TranslationEntry{
			Key:            cache.Key("mock", model, "en", "zh-TW", line.Text),
			Provider:       "mock",
			Model:          model,
			SourceLang:     "en",
			TargetLang:     "zh-TW",
			OriginalText:   line.Text,
			TranslatedText: "[cached]" + line.Text,
		}
	}
	if err := c.StoreTranslations(ctx, entries); err != nil {
		t.Fatal(err)
	}

	m := &mockProvider{name: "mock", defaultModel: model}
	o := New(map[string]provider.Provider{"mock": m}, c, Config{
		ChunkSize: 30, Concurrency: 1, MaxAttempts: 1,
	})
	o.sleep = func(_ time.Duration) {}

	events := collect(t, func(out chan<- Event) error {
		return o.Translate(ctx, Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})

	if got := m.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	if got := countByType(events, EventChunkDone); got != 1 {
		t.Fatalf("chunk-done count = %d, want 1 cache event", got)
	}

	var sawCache bool
	for _, e := range events {
		switch e.Type {
		case EventJobCreated:
			p := e.Payload.(JobCreatedPayload)
			if p.TotalChunks != 0 || p.TotalLines != len(lines) || p.CacheHits != len(lines) {
				t.Errorf("job-created payload = %+v", p)
			}
		case EventChunkDone:
			p := e.Payload.(ChunkDonePayload)
			if p.Source == "cache" {
				sawCache = true
				if p.Chunk != 0 || len(p.Lines) != len(lines) {
					t.Errorf("cache chunk payload = %+v", p)
				}
			}
		case EventDone:
			p := e.Payload.(DonePayload)
			if p.Total != 0 || p.Completed != 0 || p.Failed != 0 || p.CacheHits != len(lines) {
				t.Errorf("done payload = %+v", p)
			}
		}
	}
	if !sawCache {
		t.Fatal("expected cache chunk-done event")
	}
}

func TestCacheLookupFailureEmitsFatal(t *testing.T) {
	ctx := context.Background()
	lines := mkLines(1)
	c, err := cache.Open(":memory:")
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close cache: %v", err)
	}

	m := &mockProvider{name: "mock", queue: []mockResponse{
		{lines: translatedFor(lines)},
	}}
	o := New(map[string]provider.Provider{"mock": m}, c, Config{
		ChunkSize: 30, Concurrency: 1, MaxAttempts: 1,
	})

	out := make(chan Event, 8)
	err = o.Translate(ctx, Input{
		VideoKey: "v1", Provider: "mock",
		SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
	}, out)
	if err == nil {
		t.Fatal("expected cache lookup error")
	}

	var fatal *FatalPayload
	for e := range out {
		if e.Type == EventFatal {
			p := e.Payload.(FatalPayload)
			fatal = &p
		}
	}
	if fatal == nil {
		t.Fatal("expected fatal event")
	}
	if fatal.Code != codeCacheLookupFail {
		t.Fatalf("fatal code = %s, want %s", fatal.Code, codeCacheLookupFail)
	}
}

func TestRetryableThenSuccess(t *testing.T) {
	lines := mkLines(2)
	m := &mockProvider{name: "mock", queue: []mockResponse{
		{err: &provider.Error{Code: provider.CodeRateLimit, Retryable: true, Provider: "mock"}},
		{lines: translatedFor(lines)},
	}}
	o, _ := newOrch(t, m)

	events := collect(t, func(out chan<- Event) error {
		return o.Translate(context.Background(), Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})

	if errs := countByType(events, EventChunkError); errs != 1 {
		t.Errorf("expected 1 chunk-error (retry attempt 1), got %d", errs)
	}
	if dones := countByType(events, EventChunkDone); dones != 1 {
		t.Errorf("expected 1 chunk-done (final success), got %d", dones)
	}
	for _, e := range events {
		if e.Type == EventChunkError {
			p := e.Payload.(ChunkErrorPayload)
			if p.Final {
				t.Error("intermediate retry should have Final=false")
			}
			if p.Attempt != 1 {
				t.Errorf("attempt = %d, want 1", p.Attempt)
			}
		}
		if e.Type == EventDone {
			p := e.Payload.(DonePayload)
			if p.Failed != 0 || p.Completed != 1 {
				t.Errorf("done payload wrong: %+v", p)
			}
		}
	}
}

func TestNonRetryableFailsImmediately(t *testing.T) {
	lines := mkLines(2)
	m := &mockProvider{name: "mock", queue: []mockResponse{
		{err: &provider.Error{Code: provider.CodeInvalidKey, Retryable: false, Provider: "mock"}},
	}}
	o, _ := newOrch(t, m)

	events := collect(t, func(out chan<- Event) error {
		return o.Translate(context.Background(), Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})

	if calls := m.calls.Load(); calls != 1 {
		t.Errorf("provider calls = %d, want 1 (no retries on non-retryable)", calls)
	}
	if errs := countByType(events, EventChunkError); errs != 1 {
		t.Errorf("chunk-error count = %d, want 1", errs)
	}
	for _, e := range events {
		if e.Type == EventChunkError {
			p := e.Payload.(ChunkErrorPayload)
			if !p.Final {
				t.Error("non-retryable error should be Final=true")
			}
		}
		if e.Type == EventDone {
			p := e.Payload.(DonePayload)
			if p.Failed != 1 || p.Completed != 0 {
				t.Errorf("done payload: %+v", p)
			}
		}
	}
}

func TestRetryExhausted(t *testing.T) {
	lines := mkLines(2)
	m := &mockProvider{name: "mock", queue: []mockResponse{
		{err: &provider.Error{Code: provider.CodeRateLimit, Retryable: true, Provider: "mock"}},
		{err: &provider.Error{Code: provider.CodeRateLimit, Retryable: true, Provider: "mock"}},
		{err: &provider.Error{Code: provider.CodeRateLimit, Retryable: true, Provider: "mock"}},
	}}
	o, _ := newOrch(t, m)

	events := collect(t, func(out chan<- Event) error {
		return o.Translate(context.Background(), Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})

	if calls := m.calls.Load(); calls != 3 {
		t.Errorf("provider calls = %d, want 3 (MaxAttempts)", calls)
	}
	// 3 chunk-error events; only the third should be Final.
	finals := 0
	for _, e := range events {
		if e.Type == EventChunkError && e.Payload.(ChunkErrorPayload).Final {
			finals++
		}
	}
	if finals != 1 {
		t.Errorf("Final chunk-error count = %d, want 1", finals)
	}
}

func TestPartialSuccess(t *testing.T) {
	// 60 lines → 2 chunks of 30. First call fails non-retryable, second succeeds.
	lines := mkLines(60)
	m := &mockProvider{name: "mock", queue: []mockResponse{
		{err: &provider.Error{Code: provider.CodeBadRequest, Retryable: false, Provider: "mock"}},
		{lines: translatedFor(lines[30:])},
	}}
	o, _ := newOrch(t, m)
	// Force serial execution to make ordering deterministic for the assertion above.
	o.cfg.Concurrency = 1

	events := collect(t, func(out chan<- Event) error {
		return o.Translate(context.Background(), Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})

	for _, e := range events {
		if e.Type == EventDone {
			p := e.Payload.(DonePayload)
			if p.Total != 2 || p.Completed != 1 || p.Failed != 1 {
				t.Errorf("done payload: %+v", p)
			}
		}
		if e.Type == EventJobCreated {
			p := e.Payload.(JobCreatedPayload)
			if p.TotalChunks != 2 {
				t.Errorf("total_chunks = %d, want 2", p.TotalChunks)
			}
		}
	}
}

func TestCanceledJobIsNotLeftRunning(t *testing.T) {
	lines := mkLines(60)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mockProvider{name: "mock", block: make(chan struct{}), queue: []mockResponse{
		{lines: translatedFor(lines[:30])},
		{lines: translatedFor(lines[30:])},
	}}
	o, c := newOrch(t, m)
	o.cfg.Concurrency = 1

	out := make(chan Event, 64)
	errCh := make(chan error, 1)
	go func() {
		errCh <- o.Translate(ctx, Input{
			VideoKey: "v1", Provider: "mock",
			SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	}()

	var jobID string
	for e := range out {
		if e.Type == EventJobCreated {
			jobID = e.Payload.(JobCreatedPayload).JobID
			cancel()
			close(m.block)
		}
	}
	if err := <-errCh; err != nil {
		t.Fatalf("translate returned error: %v", err)
	}
	if jobID == "" {
		t.Fatal("missing job-created event")
	}

	job, err := c.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("job not found")
	}
	if job.Status == "running" {
		t.Fatal("canceled job was left running")
	}
	if job.CompletedAt == nil {
		t.Fatal("canceled job should have completed_at")
	}
	if job.ErrorSummary == "" {
		t.Fatal("canceled job should include an error summary")
	}
}

func TestUnknownProvider(t *testing.T) {
	m := &mockProvider{name: "mock"}
	o, _ := newOrch(t, m)

	out := make(chan Event, 8)
	err := o.Translate(context.Background(), Input{
		VideoKey: "v1", Provider: "nonexistent",
		Lines: mkLines(1),
	}, out)
	if err == nil {
		t.Fatal("expected setup error")
	}
}

func TestLocalFallbackIsQueuedForSync(t *testing.T) {
	lines := mkLines(2)
	m := &mockProvider{name: "mock", defaultModel: "mock-model", queue: []mockResponse{{
		lines: translatedFor(lines), queueForSync: true,
	}}}
	o, c := newOrch(t, m)
	events := collect(t, func(out chan<- Event) error {
		return o.Translate(context.Background(), Input{
			VideoKey: "v1", Provider: "mock", SourceLang: "en", TargetLang: "zh-TW", Lines: lines,
		}, out)
	})
	if countByType(events, EventChunkDone) != 1 {
		t.Fatalf("translation did not complete: %+v", events)
	}
	pending, err := c.PendingSyncEntries(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != len(lines) {
		t.Fatalf("pending sync entries = %d, want %d", len(pending), len(lines))
	}
}
