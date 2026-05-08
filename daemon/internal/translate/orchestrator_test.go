package translate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

// ─── mock provider ──────────────────────────────────────────────────────────

type mockResponse struct {
	err   error
	lines []provider.TranslatedLine
}

type mockProvider struct {
	name  string
	queue []mockResponse
	calls atomic.Int32
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Translate(_ context.Context, _ provider.Request) (provider.Response, error) {
	n := int(m.calls.Add(1)) - 1
	if n >= len(m.queue) {
		return provider.Response{}, errors.New("mock: out of responses")
	}
	r := m.queue[n]
	if r.err != nil {
		return provider.Response{}, r.err
	}
	return provider.Response{Lines: r.lines}, nil
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
			Key:            cache.Key("mock", "", "en", "zh-TW", lines[i].Text),
			Provider:       "mock",
			SourceLang:     "en", TargetLang: "zh-TW",
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
