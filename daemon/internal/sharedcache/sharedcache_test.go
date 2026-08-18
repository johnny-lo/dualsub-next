package sharedcache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

type mockProvider struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
}

func (p *mockProvider) Name() string         { return "mock" }
func (p *mockProvider) DefaultModel() string { return "mock-model" }
func (p *mockProvider) Translate(ctx context.Context, in provider.Request) (provider.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return provider.Response{}, ctx.Err()
		}
	}
	lines := make([]provider.TranslatedLine, len(in.Lines))
	for i, line := range in.Lines {
		lines[i] = provider.TranslatedLine{Index: line.Index, Text: "[shared]" + line.Text}
	}
	return provider.Response{Lines: lines}, nil
}

func (p *mockProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func newTestCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newTestServer(t *testing.T, c *cache.Cache, p provider.Provider) *httptest.Server {
	t.Helper()
	s := NewServer(ServerOptions{
		Token: "test-token", Cache: c, Providers: map[string]provider.Provider{"mock": p},
	})
	ts := httptest.NewServer(s.http.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func newTestClient(t *testing.T, baseURL, token string) *Client {
	t.Helper()
	c, err := NewClient(ClientOptions{
		BaseURL: baseURL, Token: token, ConnectTimeout: 50 * time.Millisecond,
		RequestTimeout: 2 * time.Second, RetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResolveUsesCentralCache(t *testing.T) {
	central := newTestCache(t)
	p := &mockProvider{}
	ts := newTestServer(t, central, p)
	client := newTestClient(t, ts.URL, "test-token")
	req := provider.Request{
		Lines:      []provider.Line{{Index: 1, Text: "Hello"}},
		SourceLang: "en", TargetLang: "zh-TW",
	}

	for i := 0; i < 2; i++ {
		res, err := client.Resolve(context.Background(), "mock", "mock-model", req)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Lines) != 1 || res.Lines[0].Text != "[shared]Hello" {
			t.Fatalf("unexpected response: %+v", res)
		}
	}
	if calls := p.callCount(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestConcurrentResolveCoalescesProviderCall(t *testing.T) {
	central := newTestCache(t)
	p := &mockProvider{delay: 75 * time.Millisecond}
	ts := newTestServer(t, central, p)
	client := newTestClient(t, ts.URL, "test-token")
	req := provider.Request{
		Lines:      []provider.Line{{Index: 1, Text: "same line"}},
		SourceLang: "en", TargetLang: "zh-TW",
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := client.Resolve(context.Background(), "mock", "mock-model", req)
			errs <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if calls := p.callCount(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestServerRequiresToken(t *testing.T) {
	ts := newTestServer(t, newTestCache(t), &mockProvider{})
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

func TestFallbackProviderMarksLocalResultForSync(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1:1", "test-token")
	local := &mockProvider{}
	p := NewFallbackProvider(local, client)
	res, err := p.Translate(context.Background(), provider.Request{
		Lines:      []provider.Line{{Index: 1, Text: "offline"}},
		SourceLang: "en", TargetLang: "zh-TW", Model: "mock-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.QueueForSync || len(res.Lines) != 1 {
		t.Fatalf("fallback response = %+v", res)
	}
}

func TestSyncOutboxOnceImportsAndAcknowledges(t *testing.T) {
	ctx := context.Background()
	local := newTestCache(t)
	central := newTestCache(t)
	ts := newTestServer(t, central, &mockProvider{})
	client := newTestClient(t, ts.URL, "test-token")
	entry := cache.TranslationEntry{
		Provider: "mock", Model: "mock-model", SourceLang: "en", TargetLang: "zh-TW",
		OriginalText: "offline", TranslatedText: "離線",
	}
	entry.Key = cache.Key(entry.Provider, entry.Model, entry.SourceLang, entry.TargetLang, entry.OriginalText)
	if err := local.StoreTranslationsForSync(ctx, []cache.TranslationEntry{entry}); err != nil {
		t.Fatal(err)
	}

	uploaded, err := SyncOutboxOnce(ctx, local, client)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded != 1 {
		t.Fatalf("uploaded = %d, want 1", uploaded)
	}
	count, _ := local.PendingSyncCount(ctx)
	if count != 0 {
		t.Fatalf("outbox count = %d, want 0", count)
	}
	hits, err := central.LookupTranslations(ctx, []string{entry.Key})
	if err != nil || hits[entry.Key] != "離線" {
		t.Fatalf("central hits=%v err=%v", hits, err)
	}
}

func TestImportRejectsForgedCacheKey(t *testing.T) {
	ts := newTestServer(t, newTestCache(t), &mockProvider{})
	body, err := json.Marshal(importRequest{Entries: []cache.TranslationEntry{{
		Key: "forged", Provider: "mock", Model: "mock-model", SourceLang: "en",
		TargetLang: "zh-TW", OriginalText: "Hello", TranslatedText: "你好",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/import", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}
