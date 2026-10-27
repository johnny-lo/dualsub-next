package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

type listResult struct {
	models []provider.ModelInfo
	err    error
}

// scriptedLister is a provider whose ListModels returns results in order,
// repeating the last one.
type scriptedLister struct {
	mockProvider
	listMu    sync.Mutex
	listCalls int
	results   []listResult
}

func (l *scriptedLister) ListModels(context.Context) ([]provider.ModelInfo, error) {
	l.listMu.Lock()
	defer l.listMu.Unlock()
	r := l.results[min(l.listCalls, len(l.results)-1)]
	l.listCalls++
	return r.models, r.err
}

func (l *scriptedLister) calls() int {
	l.listMu.Lock()
	defer l.listMu.Unlock()
	return l.listCalls
}

var solModels = []provider.ModelInfo{{ID: "gpt-5.6-sol", Label: "GPT-5.6-Sol", Efforts: []string{"low", "medium"}, DefaultEffort: "low"}}

type modelsServer struct {
	ts  *httptest.Server
	now time.Time
}

func newModelsServer(t *testing.T, providers map[string]provider.Provider) *modelsServer {
	t.Helper()
	ms := &modelsServer{now: time.Date(2026, 10, 27, 1, 2, 3, 0, time.UTC)}
	s := New(Options{Providers: providers})
	s.models.now = func() time.Time { return ms.now }
	ms.ts = httptest.NewServer(s.http.Handler)
	t.Cleanup(ms.ts.Close)
	return ms
}

func (ms *modelsServer) get(t *testing.T, path string) (int, string) {
	t.Helper()
	res, err := http.Get(ms.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func TestProviderModelsListsAndCaches(t *testing.T) {
	l := &scriptedLister{results: []listResult{{models: solModels}}}
	ms := newModelsServer(t, map[string]provider.Provider{"codex": l})

	for i := 0; i < 2; i++ {
		status, body := ms.get(t, "/v1/providers/codex/models")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, body)
		}
		var got struct {
			Models    []provider.ModelInfo `json:"models"`
			FetchedAt string               `json:"fetched_at"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Models) != 1 || got.Models[0].ID != "gpt-5.6-sol" || got.Models[0].DefaultEffort != "low" {
			t.Errorf("models = %+v", got.Models)
		}
		if got.FetchedAt != "2026-10-27T01:02:03Z" {
			t.Errorf("fetched_at = %q", got.FetchedAt)
		}
		ms.now = ms.now.Add(4 * time.Minute)
	}
	if n := l.calls(); n != 1 {
		t.Errorf("ListModels calls = %d, want 1 (second request within 5 min is cached)", n)
	}
}

func TestProviderModelsCacheExpires(t *testing.T) {
	l := &scriptedLister{results: []listResult{{models: solModels}}}
	ms := newModelsServer(t, map[string]provider.Provider{"codex": l})
	ms.get(t, "/v1/providers/codex/models")
	ms.now = ms.now.Add(5*time.Minute + time.Second)
	ms.get(t, "/v1/providers/codex/models")
	if n := l.calls(); n != 2 {
		t.Errorf("ListModels calls = %d, want 2", n)
	}
}

func TestProviderModelsRefreshBypassesCache(t *testing.T) {
	l := &scriptedLister{results: []listResult{
		{models: solModels},
		{models: []provider.ModelInfo{{ID: "gpt-5.6-terra", Label: "GPT-5.6-Terra"}}},
	}}
	ms := newModelsServer(t, map[string]provider.Provider{"codex": l})
	ms.get(t, "/v1/providers/codex/models")
	_, refreshed := ms.get(t, "/v1/providers/codex/models?refresh=1")
	_, cached := ms.get(t, "/v1/providers/codex/models")
	if n := l.calls(); n != 2 {
		t.Errorf("ListModels calls = %d, want 2", n)
	}
	for _, body := range []string{refreshed, cached} {
		if !strings.Contains(body, "gpt-5.6-terra") {
			t.Errorf("body after refresh = %s, want the refreshed list", body)
		}
	}
}

func TestProviderModelsErrorNotCached(t *testing.T) {
	l := &scriptedLister{results: []listResult{
		{err: &provider.Error{Code: provider.CodeMissingConfig, Provider: "agy", Message: "agy is not signed in"}},
		{models: solModels},
	}}
	ms := newModelsServer(t, map[string]provider.Provider{"agy": l})
	status, body := ms.get(t, "/v1/providers/agy/models")
	if status != http.StatusBadGateway || !strings.Contains(body, `"error"`) || !strings.Contains(body, "agy is not signed in") {
		t.Fatalf("first: %d %s", status, body)
	}
	status, body = ms.get(t, "/v1/providers/agy/models")
	if status != http.StatusOK || !strings.Contains(body, "gpt-5.6-sol") {
		t.Fatalf("second: %d %s", status, body)
	}
}

func TestProviderModelsUnknownProvider(t *testing.T) {
	ms := newModelsServer(t, map[string]provider.Provider{})
	status, body := ms.get(t, "/v1/providers/gemini/models")
	if status != http.StatusNotFound || !strings.Contains(body, "unknown provider: gemini") {
		t.Errorf("%d %s", status, body)
	}
}

func TestProviderModelsUnsupported(t *testing.T) {
	ms := newModelsServer(t, map[string]provider.Provider{"mock": &mockProvider{}})
	status, body := ms.get(t, "/v1/providers/mock/models")
	if status != http.StatusNotImplemented || !strings.Contains(body, provider.ErrListUnsupported.Error()) {
		t.Errorf("%d %s", status, body)
	}
}

// unsupportedLister mirrors sharedcache.FallbackProvider wrapping a provider
// that cannot list.
type unsupportedLister struct{ mockProvider }

func (*unsupportedLister) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return nil, provider.ErrListUnsupported
}

func TestProviderModelsWrappedUnsupported(t *testing.T) {
	ms := newModelsServer(t, map[string]provider.Provider{"mock": &unsupportedLister{}})
	if status, body := ms.get(t, "/v1/providers/mock/models"); status != http.StatusNotImplemented {
		t.Errorf("%d %s", status, body)
	}
}

func TestProviderModelsEmptyListIsArray(t *testing.T) {
	l := &scriptedLister{results: []listResult{{models: nil}}}
	ms := newModelsServer(t, map[string]provider.Provider{"ollama": l})
	status, body := ms.get(t, "/v1/providers/ollama/models")
	if status != http.StatusOK || !strings.Contains(body, `"models":[]`) {
		t.Errorf("%d %s", status, body)
	}
}

func TestTranslateForwardsEffort(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()
	body := strings.NewReader(`{
		"site":"test","video_key":"v1","provider":"mock","model":"gpt-5.6-sol","effort":"low",
		"source_lang":"en","target_lang":"zh-TW",
		"lines":[{"index":1,"text":"Effort line"}]
	}`)
	res, err := http.Post(ctx.ts.URL+"/v1/translate", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body) // wait for the stream to finish
	res.Body.Close()
	ctx.mock.mu.Lock()
	defer ctx.mock.mu.Unlock()
	if len(ctx.mock.efforts) != 1 || ctx.mock.efforts[0] != "low" {
		t.Fatalf("provider saw efforts %v, want [low]", ctx.mock.efforts)
	}
}
