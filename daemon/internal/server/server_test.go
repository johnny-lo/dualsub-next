package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/config"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
	"github.com/johnny/dualsub-next/daemon/internal/translate"
)

type mockProvider struct{}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) DefaultModel() string { return "mock-model" }

func (m *mockProvider) Translate(_ context.Context, in provider.Request) (provider.Response, error) {
	out := make([]provider.TranslatedLine, len(in.Lines))
	for i, l := range in.Lines {
		out[i] = provider.TranslatedLine{Index: l.Index, Text: "[t]" + l.Text}
	}
	return provider.Response{Lines: out}, nil
}

type testServerCtx struct {
	ts      *httptest.Server
	cache   *cache.Cache
	cfgPath string
	cfg     *config.Config
}

func newTestServer(t *testing.T) *testServerCtx {
	t.Helper()
	c, err := cache.Open(":memory:")
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	providers := map[string]provider.Provider{"mock": &mockProvider{}}
	orch := translate.New(providers, c, translate.Config{
		ChunkSize: 30, Concurrency: 1, MaxAttempts: 1,
	})

	cfg := &config.Config{}
	cfg.Server.Listen = "127.0.0.1:7878"
	cfg.Translate.ChunkSize = 30
	cfgPath := filepath.Join(t.TempDir(), "config.toml")

	s := New(Options{
		Orchestrator: orch,
		Providers:    providers,
		Cache:        c,
		Config:       cfg,
		ConfigPath:   cfgPath,
	})
	return &testServerCtx{
		ts:      httptest.NewServer(s.http.Handler),
		cache:   c,
		cfgPath: cfgPath,
		cfg:     cfg,
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
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("body = %s", body)
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
