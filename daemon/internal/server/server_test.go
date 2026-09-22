package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/config"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
	"github.com/johnny/dualsub-next/daemon/internal/sharedcache"
	"github.com/johnny/dualsub-next/daemon/internal/translate"
)

type mockProvider struct {
	mu    sync.Mutex
	calls int
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) DefaultModel() string { return "mock-model" }

func (m *mockProvider) Translate(_ context.Context, in provider.Request) (provider.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	out := make([]provider.TranslatedLine, len(in.Lines))
	for i, l := range in.Lines {
		out[i] = provider.TranslatedLine{Index: l.Index, Text: "[t]" + l.Text}
	}
	return provider.Response{Lines: out}, nil
}

func (m *mockProvider) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// fakeRemote stands in for the central node's lookup.
type fakeRemote struct {
	hits     map[string]string
	err      error
	calls    int
	received []provider.Line
}

func (f *fakeRemote) Lookup(_ context.Context, _, _ string, lines []provider.Line) (map[string]string, error) {
	f.calls++
	f.received = append(f.received, lines...)
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

type testServerCtx struct {
	ts      *httptest.Server
	cache   *cache.Cache
	cfgPath string
	cfg     *config.Config
	mock    *mockProvider
}

func newTestServer(t *testing.T) *testServerCtx {
	t.Helper()
	return newTestServerWith(t, nil)
}

func newTestServerWith(t *testing.T, remote RemoteLookup) *testServerCtx {
	t.Helper()
	c, err := cache.Open(":memory:")
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	mock := &mockProvider{}
	providers := map[string]provider.Provider{"mock": mock}
	orch := translate.New(providers, c, translate.Config{
		ChunkSize: 30, Concurrency: 1, MaxAttempts: 1,
	})

	cfg := &config.Config{}
	cfg.Server.Listen = "127.0.0.1:7878"
	cfg.Translate.ChunkSize = 30
	cfg.Sync.Token = "existing-sync-secret"
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	s := New(Options{
		Orchestrator: orch,
		Providers:    providers,
		Cache:        c,
		Config:       cfg,
		ConfigPath:   cfgPath,
		RemoteLookup: remote,
	})
	return &testServerCtx{
		ts:      httptest.NewServer(s.http.Handler),
		cache:   c,
		cfgPath: cfgPath,
		cfg:     cfg,
		mock:    mock,
	}
}

func TestHealthz(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()

	res, err := http.Get(ctx.ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}

	var body struct {
		Status     string `json:"status"`
		Time       string `json:"time"`
		InstallDir string `json:"install_dir"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if body.InstallDir == "" {
		t.Errorf("install_dir is empty; want the test binary's directory")
	}
}

func TestProvidersListing(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()

	res, err := http.Get(ctx.ts.URL + "/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var list []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["name"] != "mock" {
		t.Errorf("got %v", list)
	}
	if list[0]["default_model"] != "mock-model" {
		t.Errorf("default_model = %v, want mock-model", list[0]["default_model"])
	}
}

func TestTranslateSSE(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()

	body := strings.NewReader(`{
		"site":"test","video_key":"v1","provider":"mock",
		"source_lang":"en","target_lang":"zh-TW",
		"lines":[{"index":1,"text":"Hello"},{"index":2,"text":"World"}]
	}`)
	res, err := http.Post(ctx.ts.URL+"/v1/translate", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %s", ct)
	}

	var eventTypes []string
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventTypes = append(eventTypes, strings.TrimPrefix(line, "event: "))
		}
	}
	want := []string{"job-created", "chunk-done", "done"}
	if len(eventTypes) != len(want) {
		t.Fatalf("got %v, want %v", eventTypes, want)
	}
	for i, w := range want {
		if eventTypes[i] != w {
			t.Errorf("event[%d] = %s, want %s", i, eventTypes[i], w)
		}
	}
}

func TestTranslateSSEFatalFromOrchestrator(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()
	if err := ctx.cache.Close(); err != nil {
		t.Fatal(err)
	}

	body := strings.NewReader(`{
		"site":"test","video_key":"v1","provider":"mock",
		"source_lang":"en","target_lang":"zh-TW",
		"lines":[{"index":1,"text":"Hello"}]
	}`)
	res, err := http.Post(ctx.ts.URL+"/v1/translate", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}

	events := readSSEEvents(t, res.Body)
	if len(events) != 1 {
		t.Fatalf("got events %+v, want exactly one fatal event", events)
	}
	if events[0].name != "fatal" {
		t.Fatalf("event = %s, want fatal", events[0].name)
	}
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(events[0].data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "CACHE_LOOKUP_FAILED" {
		t.Fatalf("fatal code = %s, want CACHE_LOOKUP_FAILED", payload.Code)
	}
	if !strings.Contains(payload.Message, "cache lookup") {
		t.Fatalf("fatal message = %q, want cache lookup context", payload.Message)
	}
}

func TestTranslateUnknownProviderRejected(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()

	body := strings.NewReader(`{
		"site":"test","video_key":"v1","provider":"nonexistent",
		"source_lang":"en","target_lang":"zh-TW",
		"lines":[{"index":1,"text":"Hello"}]
	}`)
	res, err := http.Post(ctx.ts.URL+"/v1/translate", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Errorf("status %d, want 400", res.StatusCode)
	}
}

func TestJobsListing(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()

	// Run a translate to populate jobs table.
	body := strings.NewReader(`{
		"site":"test","video_key":"vid-A","provider":"mock",
		"source_lang":"en","target_lang":"zh-TW",
		"lines":[{"index":1,"text":"Hello"}]
	}`)
	res, _ := http.Post(ctx.ts.URL+"/v1/translate", "application/json", body)
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	// Hit /v1/jobs.
	jobsRes, err := http.Get(ctx.ts.URL + "/v1/jobs?limit=5")
	if err != nil {
		t.Fatal(err)
	}
	defer jobsRes.Body.Close()
	var list []map[string]any
	if err := json.NewDecoder(jobsRes.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d jobs, want 1", len(list))
	}
	if list[0]["video_key"] != "vid-A" {
		t.Errorf("video_key = %v", list[0]["video_key"])
	}
	if list[0]["status"] != "completed" {
		t.Errorf("status = %v", list[0]["status"])
	}
}

func TestJobsDeleteClearsOnlyJobs(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()

	if err := ctx.cache.StoreTranslations(context.Background(), []cache.TranslationEntry{{
		Key: "k1", Provider: "mock", Model: "mock-model", SourceLang: "en", TargetLang: "zh-TW",
		OriginalText: "Hello", TranslatedText: "[t]Hello",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := ctx.cache.SaveTranscript(context.Background(), cache.Transcript{
		VideoKey: "vid-A", Site: "test", Title: "A", RawJSON: "[]",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ctx.cache.CreateJob(context.Background(), cache.Job{
		ID: "job-A", VideoKey: "vid-A", Provider: "mock", Model: "mock-model",
		Status: "running", TotalChunks: 2,
	}); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodDelete, ctx.ts.URL+"/v1/jobs", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}

	stats, err := ctx.cache.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Jobs != 0 || stats.Translations != 1 || stats.Transcripts != 1 {
		t.Fatalf("unexpected stats after delete: %+v", stats)
	}
}

func TestConfigGetPut(t *testing.T) {
	ctx := newTestServer(t)
	defer ctx.ts.Close()

	// GET returns the in-memory config we seeded.
	getRes, err := http.Get(ctx.ts.URL + "/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	defer getRes.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(getRes.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	server := got["server"].(map[string]any)
	if server["listen"] != "127.0.0.1:7878" {
		t.Errorf("unexpected listen: %v", server["listen"])
	}

	// PUT writes the new config to disk.
	putBody := strings.NewReader(`{
		"server":{"listen":"127.0.0.1:9000"},
		"translate":{"chunk_size":15,"concurrency":2,"max_attempts":4},
		"cache":{"path":"/tmp/x.db"},
		"providers":{"gemini":{"api_key":"newkey","base_url":"","default_model":""}}
	}`)
	req, _ := http.NewRequest(http.MethodPut, ctx.ts.URL+"/v1/config", putBody)
	req.Header.Set("Content-Type", "application/json")
	putRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer putRes.Body.Close()
	if putRes.StatusCode != 200 {
		b, _ := io.ReadAll(putRes.Body)
		t.Fatalf("status %d: %s", putRes.StatusCode, b)
	}

	// File on disk should now have the new value.
	loaded, err := config.Load(ctx.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Listen != "127.0.0.1:9000" {
		t.Errorf("file listen = %q", loaded.Server.Listen)
	}
	if loaded.Translate.ChunkSize != 15 {
		t.Errorf("chunk_size lost: %d", loaded.Translate.ChunkSize)
	}
	if loaded.Providers.Gemini == nil || loaded.Providers.Gemini.APIKey != "newkey" {
		t.Errorf("gemini key not persisted: %+v", loaded.Providers.Gemini)
	}
	if loaded.Sync.Token != "existing-sync-secret" {
		t.Errorf("sync token was lost during JSON config update: %q", loaded.Sync.Token)
	}
}

type sseEvent struct {
	name string
	data string
}

func readSSEEvents(t *testing.T, r io.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent
	var current sseEvent
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			current.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if current.data != "" {
				current.data += "\n"
			}
			current.data += strings.TrimPrefix(line, "data: ")
		case line == "":
			if current.name != "" || current.data != "" {
				events = append(events, current)
				current = sseEvent{}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

const (
	lookupSrc = "en"
	lookupTgt = "繁體中文"
)

func seedTranslation(t *testing.T, c *cache.Cache, original, translated string) string {
	t.Helper()
	key := cache.Key("", "", lookupSrc, lookupTgt, original)
	err := c.StoreTranslations(context.Background(), []cache.TranslationEntry{{
		Key: key, Provider: "gemini", Model: "flash", SourceLang: lookupSrc, TargetLang: lookupTgt,
		OriginalText: original, TranslatedText: translated,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func postLookup(t *testing.T, ts *httptest.Server, body lookupRequest) (int, lookupResponse) {
	t.Helper()
	raw, _ := json.Marshal(body)
	res, err := http.Post(ts.URL+"/v1/lookup", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var payload lookupResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
	}
	return res.StatusCode, payload
}

func twoLines() []provider.Line {
	return []provider.Line{{Index: 1, Text: "Hello"}, {Index: 2, Text: "World"}}
}

func TestLookupReturnsCachedLinesWithoutTranslating(t *testing.T) {
	ctx := newTestServer(t)
	seedTranslation(t, ctx.cache, "Hello", "你好")

	status, res := postLookup(t, ctx.ts, lookupRequest{
		SourceLang: lookupSrc, TargetLang: lookupTgt, Lines: twoLines(), IncludeRemote: true,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if res.Hits != 1 || res.Total != 2 || res.RemoteStatus != "disabled" || res.RemoteHits != 0 {
		t.Fatalf("response = %+v", res)
	}
	if len(res.Translations) != 1 || res.Translations[0].Index != 1 || res.Translations[0].Text != "你好" {
		t.Fatalf("translations = %+v", res.Translations)
	}
	if calls := ctx.mock.callCount(); calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
	jobs, _ := ctx.cache.ListJobs(context.Background(), 10)
	if len(jobs) != 0 {
		t.Fatalf("lookup created %d job rows", len(jobs))
	}
}

func TestLookupMergesRemoteHitsAndStoresThemLocally(t *testing.T) {
	remote := &fakeRemote{hits: map[string]string{
		cache.Key("", "", lookupSrc, lookupTgt, "World"): "世界",
	}}
	ctx := newTestServerWith(t, remote)
	seedTranslation(t, ctx.cache, "Hello", "你好")

	_, res := postLookup(t, ctx.ts, lookupRequest{
		SourceLang: lookupSrc, TargetLang: lookupTgt, Lines: twoLines(), IncludeRemote: true,
	})
	if res.Hits != 2 || res.RemoteHits != 1 || res.RemoteStatus != "ok" {
		t.Fatalf("response = %+v", res)
	}
	if remote.calls != 1 {
		t.Fatalf("remote calls = %d, want 1 (only the misses)", remote.calls)
	}
	key := cache.Key("", "", lookupSrc, lookupTgt, "World")
	local, _ := ctx.cache.LookupTranslations(context.Background(), []string{key})
	if local[key] != "世界" {
		t.Fatalf("remote hit was not stored locally: %v", local)
	}
	if pending, _ := ctx.cache.PendingSyncCount(context.Background()); pending != 0 {
		t.Fatalf("remote hits must not be queued for upload, outbox = %d", pending)
	}
	if calls := ctx.mock.callCount(); calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

func TestLookupSkipsRemoteWhenNothingIsMissing(t *testing.T) {
	remote := &fakeRemote{}
	ctx := newTestServerWith(t, remote)
	seedTranslation(t, ctx.cache, "Hello", "你好")

	_, res := postLookup(t, ctx.ts, lookupRequest{
		SourceLang: lookupSrc, TargetLang: lookupTgt,
		Lines: []provider.Line{{Index: 1, Text: "Hello"}}, IncludeRemote: true,
	})
	if res.Hits != 1 || res.RemoteStatus != "ok" || remote.calls != 0 {
		t.Fatalf("response = %+v, remote calls = %d", res, remote.calls)
	}
}

func TestLookupRespectsIncludeRemoteFalse(t *testing.T) {
	remote := &fakeRemote{hits: map[string]string{cache.Key("", "", lookupSrc, lookupTgt, "World"): "世界"}}
	ctx := newTestServerWith(t, remote)

	_, res := postLookup(t, ctx.ts, lookupRequest{
		SourceLang: lookupSrc, TargetLang: lookupTgt, Lines: twoLines(), IncludeRemote: false,
	})
	if res.Hits != 0 || res.RemoteStatus != "disabled" || remote.calls != 0 {
		t.Fatalf("response = %+v, remote calls = %d", res, remote.calls)
	}
}

func TestLookupTreatsOldCentralAsUnsupported(t *testing.T) {
	ctx := newTestServerWith(t, &fakeRemote{err: sharedcache.ErrLookupUnsupported})
	seedTranslation(t, ctx.cache, "Hello", "你好")

	status, res := postLookup(t, ctx.ts, lookupRequest{
		SourceLang: lookupSrc, TargetLang: lookupTgt, Lines: twoLines(), IncludeRemote: true,
	})
	if status != http.StatusOK || res.Hits != 1 || res.RemoteStatus != "unsupported" {
		t.Fatalf("status=%d response=%+v", status, res)
	}
}

func TestLookupReportsRemoteOutage(t *testing.T) {
	ctx := newTestServerWith(t, &fakeRemote{err: errors.New("dial tcp: connection refused")})
	seedTranslation(t, ctx.cache, "Hello", "你好")

	status, res := postLookup(t, ctx.ts, lookupRequest{
		SourceLang: lookupSrc, TargetLang: lookupTgt, Lines: twoLines(), IncludeRemote: true,
	})
	if status != http.StatusOK || res.Hits != 1 || res.RemoteStatus != "unavailable" {
		t.Fatalf("status=%d response=%+v", status, res)
	}
}

func TestLookupRejectsBadRequests(t *testing.T) {
	ctx := newTestServer(t)
	tooMany := make([]provider.Line, maxLookupLines+1)
	for i := range tooMany {
		tooMany[i] = provider.Line{Index: i, Text: "x"}
	}
	cases := []struct {
		name string
		body lookupRequest
	}{
		{"missing langs", lookupRequest{Lines: twoLines()}},
		{"no lines", lookupRequest{SourceLang: lookupSrc, TargetLang: lookupTgt}},
		{"too many lines", lookupRequest{SourceLang: lookupSrc, TargetLang: lookupTgt, Lines: tooMany}},
	}
	for _, tc := range cases {
		if status, _ := postLookup(t, ctx.ts, tc.body); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, status)
		}
	}
	res, err := http.Get(ctx.ts.URL + "/v1/lookup")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", res.StatusCode)
	}
}

func TestLookupSkipsEmptyLinesAndNeverForwardsThem(t *testing.T) {
	remote := &fakeRemote{hits: map[string]string{
		cache.Key("", "", lookupSrc, lookupTgt, "World"): "世界",
	}}
	ctx := newTestServerWith(t, remote)
	seedTranslation(t, ctx.cache, "Hello", "你好")

	status, res := postLookup(t, ctx.ts, lookupRequest{
		SourceLang: lookupSrc, TargetLang: lookupTgt,
		Lines: []provider.Line{
			{Index: 1, Text: "Hello"},
			{Index: 2, Text: ""},
			{Index: 3, Text: "World"},
		},
		IncludeRemote: true,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if res.Hits != 2 || res.Total != 3 {
		t.Fatalf("response = %+v, want Hits=2 Total=3", res)
	}
	if remote.calls != 1 {
		t.Fatalf("remote calls = %d, want 1", remote.calls)
	}
	if len(remote.received) != 1 || remote.received[0].Text != "World" {
		t.Fatalf("remote received = %+v, want exactly one line with text World", remote.received)
	}
}

func TestLookupAcceptsMaxLinesBody(t *testing.T) {
	ctx := newTestServer(t)
	lines := make([]provider.Line, 2000)
	for i := range lines {
		lines[i] = provider.Line{Index: i, Text: strings.Repeat("x", 200)}
	}
	status, res := postLookup(t, ctx.ts, lookupRequest{
		SourceLang: lookupSrc, TargetLang: lookupTgt, Lines: lines,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if res.Total != 2000 {
		t.Fatalf("total = %d, want 2000", res.Total)
	}
}
