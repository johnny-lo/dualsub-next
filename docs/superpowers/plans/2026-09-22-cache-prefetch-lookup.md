# Cache-Only Prefetch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On page load the extension finds translations that already exist in the local daemon cache or on the central node and shows them, without translating anything.

**Architecture:** A cache-only `POST /v1/lookup` is added in two places — the central node's authenticated shared listener (`internal/sharedcache`) and the local daemon's browser-facing API (`internal/server`). The local route consults SQLite, then asks the central for the misses through a new `Client.Lookup()`, stores what it finds locally without queueing an upload, and returns per-line hits. The content script extracts the transcript on load, calls the route directly, patches the overlay, and only then decides whether sticky auto-translate still has work to do.

**Tech Stack:** Go 1.26 (`net/http`, `modernc.org/sqlite`, `httptest`), TypeScript, Chrome MV3, vitest 2 + jsdom 25.

**Spec:** `docs/superpowers/specs/2026-09-21-cache-prefetch-lookup-design.md`

## Global Constraints

- Lookup routes never call a provider, never create a job row, never write `sync_outbox`.
- Cache keys: `cache.Key("", "", source_lang, target_lang, text)` — provider-independent shared-v2 keys.
- Local `/v1/lookup` caps `lines` at **2000**; over the cap returns `400`.
- `remote_status` values are exactly `disabled`, `ok`, `unsupported`, `unavailable`.
- A `404` from the central means "too old", returns `sharedcache.ErrLookupUnsupported`, and **must not** open the client's circuit breaker; any other failure does.
- Central lookup request timeout: **10 seconds**.
- Extension language pair lives in `extension/src/shared/langs.ts`: `PREFERRED_SOURCE = 'en'`, `TARGET_LANG = '繁體中文'`.
- Extension: `vitest` stays on 2.x, `jsdom` on 25.x (vite 5 + CRXJS). `npm test`, `npm run typecheck`, `npm run build` must all pass.
- Daemon: `gofmt`, `go vet ./...`, `go test -race ./...` must pass from `daemon/`.
- Commit messages end with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Deploy on this machine is `install` over `./dualsub` + `systemctl --user restart dualsub.service` (the daemon is the central node, bound to `100.108.126.11:7879`). Never `pkill`.

---

## File structure

| File | Responsibility |
|---|---|
| `daemon/internal/sharedcache/protocol.go` | Add `lookupRequest` / `lookupResponse` wire types (modify). |
| `daemon/internal/sharedcache/server.go` | Central `POST /v1/lookup` handler + `shared_lookup` log event (modify). |
| `daemon/internal/sharedcache/client.go` | `ErrLookupUnsupported`, `httpStatusError`, `Client.Lookup()` (modify). |
| `daemon/internal/sharedcache/sharedcache_test.go` | Tests for the above (modify). |
| `daemon/internal/server/server.go` | `RemoteLookup` interface, `Options.RemoteLookup`, route registration (modify). |
| `daemon/internal/server/lookup.go` | Local `POST /v1/lookup` handler, remote merge, local store, `lookup` log event (create). |
| `daemon/internal/server/server_test.go` | Counting mock provider, `fakeRemote`, lookup tests (modify). |
| `daemon/cmd/dualsub/main.go` | Pass the shared-cache client as `RemoteLookup` (modify). |
| `extension/src/shared/langs.ts` | Language-pair constants (create). |
| `extension/src/shared/DaemonClient.ts` | `LookupRequest`, `LookupResponse`, `lookup()` (modify). |
| `extension/src/shared/DaemonClient.test.ts` | Tests `lookup()` against a stubbed `fetch` (create). |
| `extension/src/content/prefetch.ts` | Pure prefetch flow with injected deps (create). |
| `extension/src/content/prefetch.test.ts` | Tests for the flow (create). |
| `extension/src/content/index.ts` | Wire prefetch into bootstrap / SPA nav; coverage-based auto-translate (modify). |
| `extension/src/popup/App.tsx` | Import constants from `langs.ts` (modify). |
| `README.md`, `codebase.md` | Routes, events, file map (modify). |

---

### Task 1: Central shared listener `POST /v1/lookup`

**Files:**
- Modify: `daemon/internal/sharedcache/protocol.go`
- Modify: `daemon/internal/sharedcache/server.go`
- Test: `daemon/internal/sharedcache/sharedcache_test.go`

**Interfaces:**
- Consumes: `cache.Key`, `cache.LookupTranslations`, `recordingLogger` / `newLoggedTestServer` helpers already in the test file.
- Produces: route `POST /v1/lookup` on the shared listener; wire types `lookupRequest{SourceLang, TargetLang, Lines}` and `lookupResponse{Translations map[string]string, CacheHits int}` (Task 2 reuses both); log event `shared_lookup`.

Note: the spec says the central keeps resolve's legacy-key aliasing. Legacy keys need provider+model, which a lookup request does not carry, and every lookup client is new code using shared-v2 keys — so no aliasing here. Step 6 records that in the spec.

- [ ] **Step 1: Write the failing tests**

Append to `daemon/internal/sharedcache/sharedcache_test.go`:

```go
func TestSharedLookupReturnsHitsWithoutTranslating(t *testing.T) {
	ctx := context.Background()
	central := newTestCache(t)
	p := &mockProvider{}
	lg := &recordingLogger{}
	ts := newLoggedTestServer(t, central, p, lg)
	entry := cache.TranslationEntry{
		SourceLang: "en", TargetLang: "zh-TW", OriginalText: "Hello", TranslatedText: "你好",
	}
	entry.Key = cache.Key("", "", entry.SourceLang, entry.TargetLang, entry.OriginalText)
	if err := central.StoreTranslations(ctx, []cache.TranslationEntry{entry}); err != nil {
		t.Fatal(err)
	}
	client := newTestClient(t, ts.URL, "test-token")

	hits, err := client.Lookup(ctx, "en", "zh-TW", []provider.Line{
		{Index: 1, Text: "Hello"}, {Index: 2, Text: "World"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[entry.Key] != "你好" {
		t.Fatalf("hits = %v, want only the cached line", hits)
	}
	if calls := p.callCount(); calls != 0 {
		t.Fatalf("provider calls = %d, want 0: lookup must never translate", calls)
	}
	events := lg.byKind("shared_lookup")
	if len(events) != 1 || events[0]["lines"] != 2 || events[0]["cache_hits"] != 1 || events[0]["status"] != http.StatusOK {
		t.Fatalf("shared_lookup events = %v", events)
	}
}

func TestSharedLookupRequiresToken(t *testing.T) {
	ts := newTestServer(t, newTestCache(t), &mockProvider{})
	body, _ := json.Marshal(lookupRequest{SourceLang: "en", TargetLang: "zh-TW", Lines: []provider.Line{{Index: 1, Text: "Hello"}}})
	res, err := http.Post(ts.URL+"/v1/lookup", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/sharedcache/ -run 'TestSharedLookup' 2>&1 | head`
Expected: build failure — `undefined: lookupRequest`, `client.Lookup undefined`.

- [ ] **Step 3: Add the wire types**

Append to `daemon/internal/sharedcache/protocol.go`:

```go
// lookupRequest asks the central cache for existing translations only; the
// central never translates for it. Keys are provider-independent, so no
// provider or model is carried.
type lookupRequest struct {
	SourceLang string          `json:"source_lang"`
	TargetLang string          `json:"target_lang"`
	Lines      []provider.Line `json:"lines"`
}

type lookupResponse struct {
	Translations map[string]string `json:"translations"`
	CacheHits    int               `json:"cache_hits"`
}
```

- [ ] **Step 4: Add the handler**

In `daemon/internal/sharedcache/server.go`:

Add the constant next to `maxImportEntries`:

```go
	maxLookupLines   = 2000
```

Register the route in `NewServer` after the `/v1/import` line:

```go
	mux.HandleFunc("/v1/lookup", s.auth(s.handleLookup))
```

Add after `handleImport`:

```go
// handleLookup answers "which of these lines do you already have?" from the
// cache alone. Unlike resolve it never falls through to a provider, so a
// client can call it on every page load without risking spend.
func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	r.Body = http.MaxBytesReader(w, r.Body, maxResolveBody)
	var req lookupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.logLookup(r, req, 0, start, http.StatusBadRequest, err)
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	reject := func(message string) {
		s.logLookup(r, req, 0, start, http.StatusBadRequest, errors.New(message))
		http.Error(w, message, http.StatusBadRequest)
	}
	if req.SourceLang == "" || req.TargetLang == "" || len(req.Lines) == 0 {
		reject("languages and lines are required")
		return
	}
	if len(req.Lines) > maxLookupLines {
		reject("too many lines in one lookup request")
		return
	}
	keys := make([]string, 0, len(req.Lines))
	for _, line := range req.Lines {
		if line.Text == "" {
			reject("line text cannot be empty")
			return
		}
		keys = append(keys, cache.Key("", "", req.SourceLang, req.TargetLang, line.Text))
	}
	hits, err := s.cache.LookupTranslations(r.Context(), keys)
	if err != nil {
		s.logLookup(r, req, 0, start, http.StatusInternalServerError, err)
		http.Error(w, "lookup translations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.logLookup(r, req, len(hits), start, http.StatusOK, nil)
	writeJSON(w, http.StatusOK, lookupResponse{Translations: hits, CacheHits: len(hits)})
}

func (s *Server) logLookup(r *http.Request, req lookupRequest, hits int, start time.Time, status int, err error) {
	fields := map[string]any{
		"remote": remoteIP(r), "lines": len(req.Lines), "cache_hits": hits, "status": status,
		"duration_ms": time.Since(start).Milliseconds(),
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	s.log.Event("shared_lookup", fields)
}
```

- [ ] **Step 5: Run the token test to verify it passes**

Run: `cd daemon && go test ./internal/sharedcache/ -run 'TestSharedLookupRequiresToken' -v 2>&1 | tail -3`
Expected: `PASS`. (`TestSharedLookupReturnsHitsWithoutTranslating` still fails to build until Task 2 adds `Client.Lookup` — that is expected; do not commit yet if the package does not build. Proceed to Task 2 and commit both together in Task 2 Step 6.)

- [ ] **Step 6: Record the aliasing deviation in the spec**

```bash
cd /home/johnny/Desktop/dualsub-next
python3 - <<'EOF'
p = "docs/superpowers/specs/2026-09-21-cache-prefetch-lookup-design.md"
s = open(p).read()
old = "Keeps resolve's legacy-key aliasing so a mid-rollout client still matches.\nRequires the bearer token like every other route on that listener."
new = "No legacy-key aliasing: legacy keys need provider+model, which lookup does not\ncarry, and every lookup client is new code using shared-v2 keys. Requires the\nbearer token like every other route on that listener."
assert old in s
open(p, "w").write(s.replace(old, new))
EOF
```

---

### Task 2: Shared-cache client `Lookup()` with 404-as-unsupported

**Files:**
- Modify: `daemon/internal/sharedcache/client.go`
- Test: `daemon/internal/sharedcache/sharedcache_test.go`

**Interfaces:**
- Consumes: `lookupRequest` / `lookupResponse` from Task 1; existing `c.post`, `c.available`, `c.markUnavailable`, `c.markAvailable`.
- Produces: `var ErrLookupUnsupported error`; `func (c *Client) Lookup(ctx context.Context, sourceLang, targetLang string, lines []provider.Line) (map[string]string, error)` returning `cache_key → translated_text`. Task 3 depends on both.

- [ ] **Step 1: Write the failing tests**

Add `"errors"` to the test file's import block, then append:

```go
func TestClientLookupTreatsNotFoundAsUnsupported(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	client, err := NewClient(ClientOptions{
		BaseURL: ts.URL, Token: "test-token", ConnectTimeout: 50 * time.Millisecond,
		RequestTimeout: time.Second, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Lookup(context.Background(), "en", "zh-TW", []provider.Line{{Index: 1, Text: "Hello"}})
	if !errors.Is(err, ErrLookupUnsupported) {
		t.Fatalf("err = %v, want ErrLookupUnsupported", err)
	}
	if err := client.available(); err != nil {
		t.Fatalf("an old central must not open the circuit, got %v", err)
	}
}

func TestClientLookupOutageOpensCircuit(t *testing.T) {
	client, err := NewClient(ClientOptions{
		BaseURL: "http://127.0.0.1:1", Token: "test-token", ConnectTimeout: 50 * time.Millisecond,
		RequestTimeout: time.Second, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Lookup(context.Background(), "en", "zh-TW", []provider.Line{{Index: 1, Text: "Hello"}})
	if err == nil || errors.Is(err, ErrLookupUnsupported) {
		t.Fatalf("err = %v, want a network failure", err)
	}
	if err := client.available(); !errors.Is(err, errCircuitOpen) {
		t.Fatalf("circuit should be open after an outage, got %v", err)
	}
}

func TestClientLookupEmptyLinesSkipsNetwork(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1:1", "test-token")
	hits, err := client.Lookup(context.Background(), "en", "zh-TW", nil)
	if err != nil || len(hits) != 0 {
		t.Fatalf("hits=%v err=%v, want empty and nil", hits, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/sharedcache/ -run 'TestClientLookup|TestSharedLookup' 2>&1 | head`
Expected: build failure — `client.Lookup undefined`, `undefined: ErrLookupUnsupported`.

- [ ] **Step 3: Implement**

In `daemon/internal/sharedcache/client.go`:

Replace the single `var errCircuitOpen = ...` line with:

```go
var (
	errCircuitOpen = errors.New("shared cache temporarily unavailable")
	// ErrLookupUnsupported means the central node predates /v1/lookup. Callers
	// treat it as "no remote hits", never as an outage.
	ErrLookupUnsupported = errors.New("shared cache node does not support lookup")
)

// lookupTimeout bounds a cache read; translate-sized timeouts do not apply.
const lookupTimeout = 10 * time.Second

type httpStatusError struct {
	status  int
	message string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("shared cache HTTP %d: %s", e.status, e.message)
}
```

In `post`, replace the non-2xx branch:

```go
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return &httpStatusError{status: res.StatusCode, message: strings.TrimSpace(string(message))}
	}
```

Add after `Import`:

```go
// Lookup returns cache_key → translated_text for lines the central already
// has. It never causes the central to translate.
func (c *Client) Lookup(ctx context.Context, sourceLang, targetLang string, lines []provider.Line) (map[string]string, error) {
	if len(lines) == 0 {
		return map[string]string{}, nil
	}
	if err := c.available(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	var payload lookupResponse
	err := c.post(ctx, "/v1/lookup", lookupRequest{SourceLang: sourceLang, TargetLang: targetLang, Lines: lines}, &payload)
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) && httpErr.status == http.StatusNotFound {
		return nil, ErrLookupUnsupported
	}
	if err != nil {
		c.markUnavailable()
		return nil, err
	}
	c.markAvailable()
	if payload.Translations == nil {
		payload.Translations = map[string]string{}
	}
	return payload.Translations, nil
}
```

- [ ] **Step 4: Run the whole package**

Run: `cd daemon && gofmt -l ./internal/sharedcache && go vet ./internal/sharedcache && go test -race ./internal/sharedcache 2>&1 | tail -3`
Expected: no gofmt output, `ok`.

- [ ] **Step 5: Confirm the error-string format is unchanged**

Run: `cd daemon && grep -rn 'shared cache HTTP' internal/ | grep -v client.go`
Expected: no output (nothing else depends on the message format), or only tests that still pass.

- [ ] **Step 6: Commit Tasks 1 and 2**

```bash
cd /home/johnny/Desktop/dualsub-next
git add daemon/internal/sharedcache/ docs/superpowers/specs/2026-09-21-cache-prefetch-lookup-design.md
git commit -F - <<'EOF'
feat(sharedcache): cache-only /v1/lookup on the central node

Clients could only ask the central via /v1/resolve, which translates on a
miss. Add POST /v1/lookup: derive shared keys, return hits, never touch a
provider. Client.Lookup treats a 404 as "central too old" without opening
the circuit breaker; real failures still do.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 3: Local daemon `POST /v1/lookup`

**Files:**
- Modify: `daemon/internal/server/server.go`
- Create: `daemon/internal/server/lookup.go`
- Test: `daemon/internal/server/server_test.go`

**Interfaces:**
- Consumes: `sharedcache.ErrLookupUnsupported` (Task 2); `cache.Key`, `cache.LookupTranslations`, `cache.StoreTranslations`, `cache.PendingSyncCount`; `s.log` (`*logger.Logger`, may be nil — guard like `handleTranslate` does).
- Produces: `type RemoteLookup interface { Lookup(ctx context.Context, sourceLang, targetLang string, lines []provider.Line) (map[string]string, error) }` and `Options.RemoteLookup RemoteLookup` (Task 4 wires it); route `POST /v1/lookup` with request `{video_key?, source_lang, target_lang, lines, include_remote}` and response `{translations: [{index,text}], hits, total, remote_hits, remote_status}` (Task 5 mirrors these in TypeScript).

- [ ] **Step 1: Give the mock provider a call counter and add a fake remote**

In `daemon/internal/server/server_test.go`, add `"errors"` and `"sync"` to the imports, and add `"github.com/johnny/dualsub-next/daemon/internal/sharedcache"`.

Replace the `mockProvider` block (from `type mockProvider struct{}` through the end of its `Translate` method) with:

```go
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
	hits  map[string]string
	err   error
	calls int
}

func (f *fakeRemote) Lookup(_ context.Context, _, _ string, _ []provider.Line) (map[string]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}
```

Change `testServerCtx` and `newTestServer`:

```go
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
```

Careful: `newTestServerWith(t, nil)` passes a nil **interface**, which is what the handler checks. In `main.go` (Task 4) the pointer must only be assigned when non-nil for the same reason.

- [ ] **Step 2: Write the failing lookup tests**

Append to `server_test.go`:

```go
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/server/ 2>&1 | head`
Expected: build failure — `undefined: RemoteLookup`, `unknown field RemoteLookup`, `undefined: lookupRequest`, `undefined: maxLookupLines`.

- [ ] **Step 4: Add the interface, option, and route**

In `daemon/internal/server/server.go`:

Add to `Options` after `Logger`:

```go
	// RemoteLookup asks the central node's cache without translating. Leave
	// nil when no central is configured. *sharedcache.Client satisfies it.
	RemoteLookup RemoteLookup
```

Add to `Server` after `log`:

```go
	remote    RemoteLookup
```

Set it in `New`: `remote: opts.RemoteLookup,` after `log: opts.Logger,`.

Register after the `/v1/config` line:

```go
	mux.HandleFunc("/v1/lookup", s.handleLookup)
```

Add the interface above `type Options struct`:

```go
// RemoteLookup is the cache-only question the daemon can ask the central node.
type RemoteLookup interface {
	Lookup(ctx context.Context, sourceLang, targetLang string, lines []provider.Line) (map[string]string, error)
}
```

- [ ] **Step 5: Write the handler**

Create `daemon/internal/server/lookup.go`:

```go
package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
	"github.com/johnny/dualsub-next/daemon/internal/sharedcache"
)

// ─── /v1/lookup ─────────────────────────────────────────────────────────────
// Cache-only: answers which lines already have a translation, locally or on
// the central node. Never calls a provider, never creates a job. The
// extension calls this on every page load, so a miss must be free.

const maxLookupLines = 2000

type lookupRequest struct {
	VideoKey      string          `json:"video_key,omitempty"`
	SourceLang    string          `json:"source_lang"`
	TargetLang    string          `json:"target_lang"`
	Lines         []provider.Line `json:"lines"`
	IncludeRemote bool            `json:"include_remote"`
}

type lookupResponse struct {
	Translations []provider.TranslatedLine `json:"translations"`
	Hits         int                       `json:"hits"`
	Total        int                       `json:"total"`
	RemoteHits   int                       `json:"remote_hits"`
	RemoteStatus string                    `json:"remote_status"` // disabled | ok | unsupported | unavailable
}

func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTranslateBody)
	var req lookupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SourceLang == "" || req.TargetLang == "" || len(req.Lines) == 0 {
		http.Error(w, "source_lang, target_lang, and lines are required", http.StatusBadRequest)
		return
	}
	if len(req.Lines) > maxLookupLines {
		http.Error(w, "too many lines in one lookup request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	keys := make([]string, len(req.Lines))
	for i, l := range req.Lines {
		keys[i] = cache.Key("", "", req.SourceLang, req.TargetLang, l.Text)
	}
	hits, err := s.cache.LookupTranslations(ctx, keys)
	if err != nil {
		http.Error(w, "lookup translations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	remoteStatus := "disabled"
	remoteKeys := map[string]struct{}{}
	if req.IncludeRemote && s.remote != nil {
		remoteStatus = "ok"
		missing := missingLines(req.Lines, keys, hits)
		if len(missing) > 0 {
			found, err := s.remote.Lookup(ctx, req.SourceLang, req.TargetLang, missing)
			switch {
			case errors.Is(err, sharedcache.ErrLookupUnsupported):
				remoteStatus = "unsupported"
			case err != nil:
				remoteStatus = "unavailable"
			default:
				entries := make([]cache.TranslationEntry, 0, len(found))
				for _, l := range missing {
					key := cache.Key("", "", req.SourceLang, req.TargetLang, l.Text)
					translated, ok := found[key]
					if !ok || translated == "" {
						continue
					}
					if _, dup := hits[key]; dup {
						continue
					}
					hits[key] = translated
					remoteKeys[key] = struct{}{}
					entries = append(entries, cache.TranslationEntry{
						Key: key, Provider: "central", SourceLang: req.SourceLang, TargetLang: req.TargetLang,
						OriginalText: l.Text, TranslatedText: translated,
					})
				}
				// StoreTranslations does not touch sync_outbox: these rows came
				// from the central, so uploading them back would be a no-op.
				if len(entries) > 0 {
					if err := s.cache.StoreTranslations(ctx, entries); err != nil && s.log != nil {
						s.log.Event("lookup_store_failed", map[string]any{"error": err.Error(), "entries": len(entries)})
					}
				}
			}
		}
	}

	res := lookupResponse{Total: len(req.Lines), RemoteStatus: remoteStatus}
	for i, l := range req.Lines {
		translated, ok := hits[keys[i]]
		if !ok {
			continue
		}
		res.Hits++
		if _, fromRemote := remoteKeys[keys[i]]; fromRemote {
			res.RemoteHits++
		}
		res.Translations = append(res.Translations, provider.TranslatedLine{Index: l.Index, Text: translated})
	}
	if res.Translations == nil {
		res.Translations = []provider.TranslatedLine{}
	}
	if s.log != nil {
		s.log.Event("lookup", map[string]any{
			"video_key": req.VideoKey, "lines": res.Total, "hits": res.Hits,
			"remote_hits": res.RemoteHits, "remote_status": res.RemoteStatus,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// missingLines returns one line per distinct key that the local cache lacks.
func missingLines(lines []provider.Line, keys []string, hits map[string]string) []provider.Line {
	seen := make(map[string]struct{}, len(lines))
	var missing []provider.Line
	for i, l := range lines {
		if _, ok := hits[keys[i]]; ok {
			continue
		}
		if _, dup := seen[keys[i]]; dup {
			continue
		}
		seen[keys[i]] = struct{}{}
		missing = append(missing, l)
	}
	return missing
}
```

- [ ] **Step 6: Run the package**

Run: `cd daemon && gofmt -l ./internal/server && go vet ./internal/server && go test -race ./internal/server 2>&1 | tail -3`
Expected: `gofmt -l` prints only the pre-existing `internal/server/server.go` if it printed it before you touched it (check with `git stash`-free means: `git diff --quiet HEAD -- daemon/internal/server/server.go` is false now, so instead run `gofmt -d internal/server/server.go | head` and make sure the only diffs are outside your edits — or simply run `gofmt -w internal/server/server.go`, which is acceptable as part of this task). Tests: `ok`.

- [ ] **Step 7: Run everything**

Run: `cd daemon && go test -race ./... 2>&1 | tail -3`
Expected: all packages `ok`.

- [ ] **Step 8: Commit**

```bash
cd /home/johnny/Desktop/dualsub-next
git add daemon/internal/server/
git commit -F - <<'EOF'
feat(server): cache-only POST /v1/lookup with central fallthrough

The extension had no way to ask "do we already have this?" — /v1/translate
translates on a miss. /v1/lookup checks SQLite, asks the central node for
the misses, stores what it finds locally (no outbox: it came from the
central), and reports hits per line plus a remote_status that separates a
miss from a failure.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 4: Wire the daemon, document the routes

**Files:**
- Modify: `daemon/cmd/dualsub/main.go:147-155`
- Modify: `README.md` (routes table near line 181; events table near line 200)
- Modify: `codebase.md` (file map rows for `internal/server/` and `internal/sharedcache/`)

**Interfaces:**
- Consumes: `server.Options.RemoteLookup` (Task 3), `*sharedcache.Client` (Task 2).
- Produces: a running daemon that serves `/v1/lookup` on `127.0.0.1:7878` and (on the central) `/v1/lookup` on `100.108.126.11:7879`.

- [ ] **Step 1: Pass the client through**

In `daemon/cmd/dualsub/main.go`, immediately before `srv := server.New(server.Options{`, add:

```go
	// A nil *Client stored in an interface is non-nil; only assign when set.
	var remoteLookup server.RemoteLookup
	if remoteCache != nil {
		remoteLookup = remoteCache
	}
```

and add `RemoteLookup: remoteLookup,` to the `server.Options{...}` literal after `Logger: lg,`.

- [ ] **Step 2: Build and run the existing main tests**

Run: `cd daemon && go build ./... && go test ./cmd/... 2>&1 | tail -2`
Expected: builds, `ok`.

- [ ] **Step 3: Document the routes and events**

In `README.md`, add to the browser-facing routes table (after the `PUT /v1/config` row):

```
| POST   | `/v1/lookup`         | cache-only: which lines already have a translation (local + central); never translates |
```

Add to the shared-listener routes table (after the `/v1/import` row):

```
| POST   | `/v1/lookup`  | cache-only lookup for clients; never translates          |
```

Add to the events table (after the `shared_history_queued` row):

```
| local   | `lookup`                | page-load prefetch: `hits`/`lines` found locally, `remote_hits`, `remote_status` |
| central | `shared_lookup`         | a client asked which lines exist; `cache_hits` of `lines`     |
```

In `codebase.md`, extend the `internal/server/` row: append ` \`lookup.go\` is the cache-only \`POST /v1/lookup\` (local SQLite, then \`Options.RemoteLookup\` for the misses; remote hits are stored locally without touching \`sync_outbox\`).` and extend the `internal/sharedcache/` row: append ` \`/v1/lookup\` + \`Client.Lookup()\` are the cache-only path; a 404 from an older central is \`ErrLookupUnsupported\` and does not trip the circuit breaker.`

- [ ] **Step 4: Commit**

```bash
cd /home/johnny/Desktop/dualsub-next
git add daemon/cmd/dualsub/main.go README.md codebase.md
git commit -F - <<'EOF'
feat(daemon): wire central lookup into serve; document /v1/lookup

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 5: Extension shared constants and `DaemonClient.lookup()`

**Files:**
- Create: `extension/src/shared/langs.ts`
- Modify: `extension/src/popup/App.tsx:50-51`
- Modify: `extension/src/shared/DaemonClient.ts`
- Test: `extension/src/shared/DaemonClient.test.ts`

**Interfaces:**
- Produces: `PREFERRED_SOURCE`, `TARGET_LANG` from `@/shared/langs`; `LookupRequest`, `LookupResponse`, `RemoteStatus` types and `DaemonClient.lookup(req: LookupRequest): Promise<LookupResponse>` from `@/shared/DaemonClient`. Tasks 6 and 7 depend on all of these.

- [ ] **Step 1: Create the constants module and switch the popup to it**

Create `extension/src/shared/langs.ts`:

```ts
// The language pair every cache key is derived from. The content script's
// prefetch and the popup's translate MUST agree, or prefetch silently misses
// every row the popup wrote.
export const PREFERRED_SOURCE = 'en'
export const TARGET_LANG = '繁體中文'
```

In `extension/src/popup/App.tsx`, delete the two lines

```ts
const TARGET_LANG = '繁體中文'
const PREFERRED_SOURCE = 'en'
```

and add to the imports:

```ts
import { PREFERRED_SOURCE, TARGET_LANG } from '@/shared/langs'
```

- [ ] **Step 2: Write the failing client test**

Create `extension/src/shared/DaemonClient.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DaemonClient, type LookupResponse } from './DaemonClient'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('DaemonClient.lookup', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('posts a cache-only lookup and returns the parsed body', async () => {
    const payload: LookupResponse = {
      translations: [{ index: 1, text: '你好' }],
      hits: 1,
      total: 2,
      remote_hits: 0,
      remote_status: 'disabled',
    }
    const fetchMock = vi.fn(async () => jsonResponse(payload))
    vi.stubGlobal('fetch', fetchMock)

    const res = await new DaemonClient('http://daemon.test').lookup({
      video_key: 'udemy:course/1',
      source_lang: 'en',
      target_lang: '繁體中文',
      lines: [
        { index: 1, text: 'Hello' },
        { index: 2, text: 'World' },
      ],
      include_remote: true,
    })

    expect(res).toEqual(payload)
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('http://daemon.test/v1/lookup')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toMatchObject({ include_remote: true, total: undefined })
  })

  it('throws with the status on a non-2xx response', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('too many lines', { status: 400 })))
    await expect(
      new DaemonClient('http://daemon.test').lookup({
        source_lang: 'en',
        target_lang: '繁體中文',
        lines: [{ index: 1, text: 'x' }],
        include_remote: false,
      }),
    ).rejects.toThrow('HTTP 400')
  })
})
```

- [ ] **Step 3: Run it to verify it fails**

Run: `cd extension && npx vitest run src/shared 2>&1 | tail -8`
Expected: FAIL — `lookup is not a function` / type import error.

- [ ] **Step 4: Implement**

In `extension/src/shared/DaemonClient.ts`, add after `TranslateRequest`:

```ts
export type RemoteStatus = 'disabled' | 'ok' | 'unsupported' | 'unavailable'

export interface LookupRequest {
  video_key?: string
  source_lang: string
  target_lang: string
  lines: Array<{ index: number; text: string }>
  include_remote: boolean
}

export interface LookupResponse {
  translations: TranslatedLine[]
  hits: number
  total: number
  remote_hits: number
  remote_status: RemoteStatus
}
```

Add to the `DaemonClient` class after `putConfig`:

```ts
  /**
   * Cache-only: which of these lines already have a translation, locally or
   * on the central node. Never triggers a translation, so it is safe to call
   * on every page load.
   */
  async lookup(req: LookupRequest): Promise<LookupResponse> {
    const res = await fetch(`${this.baseURL}/v1/lookup`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    })
    if (!res.ok) {
      const text = await res.text().catch(() => '')
      throw new Error(`HTTP ${res.status}: ${text}`)
    }
    return res.json()
  }
```

- [ ] **Step 5: Run tests and typecheck**

Run: `cd extension && npx vitest run 2>&1 | tail -6 && npx tsc --noEmit && echo TYPECHECK OK`
Expected: all tests pass, `TYPECHECK OK`.

- [ ] **Step 6: Commit**

```bash
cd /home/johnny/Desktop/dualsub-next
git add extension/src/shared/langs.ts extension/src/shared/DaemonClient.ts extension/src/shared/DaemonClient.test.ts extension/src/popup/App.tsx
git commit -F - <<'EOF'
feat(extension): DaemonClient.lookup + shared language constants

The language pair moves out of the popup so the content script derives the
same cache keys the popup writes.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 6: Prefetch flow as a pure module

**Files:**
- Create: `extension/src/content/prefetch.ts`
- Test: `extension/src/content/prefetch.test.ts`

**Interfaces:**
- Consumes: `LookupRequest`, `LookupResponse`, `RemoteStatus` (Task 5); `TranscriptEntry` from `@/shared/transcript`.
- Produces:

```ts
export interface PrefetchDeps {
  videoKey: string
  sourceLang: string
  targetLang: string
  extract: () => Promise<TranscriptEntry[]>
  lookup: (req: LookupRequest) => Promise<LookupResponse>
  apply: (translations: Record<string, string>) => void
  log?: (message: string) => void
}
export interface PrefetchResult {
  entries: TranscriptEntry[]
  hits: number
  total: number
  remoteStatus: RemoteStatus | 'error'
}
export function needsTranslation(result: PrefetchResult | null): boolean
export async function prefetchCachedTranslations(deps: PrefetchDeps): Promise<PrefetchResult | null>
```

`null` means the transcript could not be extracted (caller falls back to today's behavior). `remoteStatus: 'error'` with `hits: 0` means the daemon lookup itself failed; `entries` is still populated so the caller can auto-translate without re-extracting. Task 7 depends on this contract.

- [ ] **Step 1: Write the failing tests**

Create `extension/src/content/prefetch.test.ts`:

```ts
import { describe, expect, it, vi } from 'vitest'
import type { LookupResponse } from '@/shared/DaemonClient'
import { needsTranslation, prefetchCachedTranslations, type PrefetchDeps } from './prefetch'

const entries = [
  { index: 1, originalText: 'Hello' },
  { index: 2, originalText: 'World' },
]

function deps(overrides: Partial<PrefetchDeps> = {}): PrefetchDeps & {
  apply: ReturnType<typeof vi.fn>
  lookup: ReturnType<typeof vi.fn>
} {
  const lookup = vi.fn(
    async (): Promise<LookupResponse> => ({
      translations: [{ index: 1, text: '你好' }],
      hits: 1,
      total: 2,
      remote_hits: 0,
      remote_status: 'disabled',
    }),
  )
  const apply = vi.fn()
  return {
    videoKey: 'udemy:course/1',
    sourceLang: 'en',
    targetLang: '繁體中文',
    extract: async () => entries,
    lookup,
    apply,
    ...overrides,
  }
}

describe('prefetchCachedTranslations', () => {
  it('applies hits keyed by original text and reports coverage', async () => {
    const d = deps()
    const result = await prefetchCachedTranslations(d)
    expect(d.apply).toHaveBeenCalledWith({ Hello: '你好' })
    expect(result).toEqual({ entries, hits: 1, total: 2, remoteStatus: 'disabled' })
    expect(d.lookup).toHaveBeenCalledWith({
      video_key: 'udemy:course/1',
      source_lang: 'en',
      target_lang: '繁體中文',
      lines: [
        { index: 1, text: 'Hello' },
        { index: 2, text: 'World' },
      ],
      include_remote: true,
    })
  })

  it('does not touch the overlay when nothing hits', async () => {
    const d = deps({
      lookup: vi.fn(async () => ({
        translations: [],
        hits: 0,
        total: 2,
        remote_hits: 0,
        remote_status: 'ok' as const,
      })),
    })
    const result = await prefetchCachedTranslations(d)
    expect(d.apply).not.toHaveBeenCalled()
    expect(result?.hits).toBe(0)
  })

  it('returns null and skips the lookup when extraction fails', async () => {
    const d = deps({
      extract: async () => {
        throw new Error('no transcript')
      },
    })
    expect(await prefetchCachedTranslations(d)).toBeNull()
    expect(d.lookup).not.toHaveBeenCalled()
  })

  it('skips the lookup for an empty transcript', async () => {
    const d = deps({ extract: async () => [] })
    expect(await prefetchCachedTranslations(d)).toEqual({
      entries: [],
      hits: 0,
      total: 0,
      remoteStatus: 'disabled',
    })
    expect(d.lookup).not.toHaveBeenCalled()
  })

  it('keeps the entries when the daemon lookup fails so auto-translate can still run', async () => {
    const log = vi.fn()
    const d = deps({
      lookup: vi.fn(async () => {
        throw new Error('HTTP 404')
      }),
      log,
    })
    const result = await prefetchCachedTranslations(d)
    expect(result).toEqual({ entries, hits: 0, total: 2, remoteStatus: 'error' })
    expect(d.apply).not.toHaveBeenCalled()
    expect(log).toHaveBeenCalledWith(expect.stringContaining('HTTP 404'))
  })
})

describe('needsTranslation', () => {
  it('is true when prefetch could not run', () => {
    expect(needsTranslation(null)).toBe(true)
  })
  it('is true when some lines are missing', () => {
    expect(needsTranslation({ entries, hits: 1, total: 2, remoteStatus: 'ok' })).toBe(true)
  })
  it('is false when every line was found', () => {
    expect(needsTranslation({ entries, hits: 2, total: 2, remoteStatus: 'ok' })).toBe(false)
  })
  it('is false for an empty transcript (nothing to translate)', () => {
    expect(needsTranslation({ entries: [], hits: 0, total: 0, remoteStatus: 'disabled' })).toBe(false)
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd extension && npx vitest run src/content/prefetch 2>&1 | tail -6`
Expected: FAIL — cannot resolve `./prefetch`.

- [ ] **Step 3: Implement**

Create `extension/src/content/prefetch.ts`:

```ts
import type { LookupRequest, LookupResponse, RemoteStatus } from '@/shared/DaemonClient'
import type { TranscriptEntry } from '@/shared/transcript'

/**
 * Page-load prefetch: find translations that already exist (locally or on the
 * central node) and show them. This path never translates; that decision is
 * left to the caller via needsTranslation().
 *
 * Dependencies are injected so the flow is testable without the content
 * script's module-level side effects.
 */
export interface PrefetchDeps {
  videoKey: string
  sourceLang: string
  targetLang: string
  extract: () => Promise<TranscriptEntry[]>
  lookup: (req: LookupRequest) => Promise<LookupResponse>
  /** Receives original text → translation for every hit. */
  apply: (translations: Record<string, string>) => void
  log?: (message: string) => void
}

export interface PrefetchResult {
  entries: TranscriptEntry[]
  hits: number
  total: number
  /** 'error' = the daemon lookup itself failed (offline, or too old for /v1/lookup). */
  remoteStatus: RemoteStatus | 'error'
}

/** True when sticky auto-translate still has lines to fill. */
export function needsTranslation(result: PrefetchResult | null): boolean {
  if (result === null) return true
  return result.hits < result.total
}

export async function prefetchCachedTranslations(deps: PrefetchDeps): Promise<PrefetchResult | null> {
  let entries: TranscriptEntry[]
  try {
    entries = await deps.extract()
  } catch (err) {
    deps.log?.(`prefetch: extract failed: ${err instanceof Error ? err.message : String(err)}`)
    return null
  }
  if (entries.length === 0) {
    return { entries, hits: 0, total: 0, remoteStatus: 'disabled' }
  }

  let res: LookupResponse
  try {
    res = await deps.lookup({
      video_key: deps.videoKey,
      source_lang: deps.sourceLang,
      target_lang: deps.targetLang,
      lines: entries.map((e) => ({ index: e.index, text: e.originalText })),
      include_remote: true,
    })
  } catch (err) {
    deps.log?.(`prefetch: lookup failed: ${err instanceof Error ? err.message : String(err)}`)
    return { entries, hits: 0, total: entries.length, remoteStatus: 'error' }
  }

  const byIndex = new Map<number, string>()
  for (const e of entries) byIndex.set(e.index, e.originalText)
  const translations: Record<string, string> = {}
  for (const t of res.translations) {
    const original = byIndex.get(t.index)
    if (original) translations[original] = t.text
  }
  if (Object.keys(translations).length > 0) deps.apply(translations)

  return { entries, hits: res.hits, total: res.total, remoteStatus: res.remote_status }
}
```

- [ ] **Step 4: Run tests and typecheck**

Run: `cd extension && npx vitest run 2>&1 | tail -6 && npx tsc --noEmit && echo TYPECHECK OK`
Expected: all pass, `TYPECHECK OK`.

- [ ] **Step 5: Commit**

```bash
cd /home/johnny/Desktop/dualsub-next
git add extension/src/content/prefetch.ts extension/src/content/prefetch.test.ts
git commit -F - <<'EOF'
feat(extension): cache-only prefetch flow with coverage result

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 7: Wire prefetch into the content script

**Files:**
- Modify: `extension/src/content/index.ts` (imports; `liveTargetLang` default; `autoTranslateCurrentLecture`; `bootstrap`; the Udemy SPA `handleNavigation` block)
- Modify: `codebase.md` (file map: add `src/content/prefetch.ts`, `src/shared/langs.ts`; extend `src/content/index.ts` row if present, else the `DaemonClient.ts` row)

**Interfaces:**
- Consumes: `prefetchCachedTranslations`, `needsTranslation`, `PrefetchResult` (Task 6); `DaemonClient.lookup` (Task 5); `PREFERRED_SOURCE`, `TARGET_LANG` (Task 5); existing `ensureOverlay`, `startCueObserver`, `writeStoredTranslations`, `startFullTranscriptTranslate`.
- Produces: on load and on SPA navigation the page prefetches; sticky auto-translate runs only when `needsTranslation(result)`.

- [ ] **Step 1: Imports and constants**

At the top of `extension/src/content/index.ts` add:

```ts
import { DaemonClient } from '@/shared/DaemonClient'
import { PREFERRED_SOURCE, TARGET_LANG } from '@/shared/langs'
import type { TranscriptEntry } from '@/shared/transcript'
import { needsTranslation, prefetchCachedTranslations, type PrefetchResult } from './prefetch'
```

Change `let liveTargetLang = '繁體中文'` to `let liveTargetLang = TARGET_LANG`.

After `const TRANSLATE_PORT = 'dualsub-daemon-translate'` add:

```ts
// One-shot lookups go straight to the daemon: host_permissions covers
// 127.0.0.1:7878 and the daemon sends Access-Control-Allow-Origin: *. Only
// the SSE translate stream needs the background relay.
const daemon = new DaemonClient()
```

- [ ] **Step 2: Add the prefetch runner**

Insert after `restoreOverlayFromStorage()`:

```ts
// Cache-only prefetch for the current lecture. Shows whatever the daemon (or
// the central node) already has; never translates. Returns null when the
// transcript could not be extracted.
async function prefetchCurrentLecture(): Promise<PrefetchResult | null> {
  if (!extractor) return null
  const videoKey = extractor.videoKey()
  const result = await prefetchCachedTranslations({
    videoKey,
    sourceLang: PREFERRED_SOURCE,
    targetLang: TARGET_LANG,
    extract: () => extractor.extractFullTranscript(PREFERRED_SOURCE),
    lookup: (req) => daemon.lookup(req),
    apply: (translations) => {
      // The user may have navigated away while the lookup was in flight.
      if (extractor.videoKey() !== videoKey) return
      currentVideoKey = videoKey
      ensureOverlay().patchTranslations(translations)
      startCueObserver()
      void writeStoredTranslations(videoKey, translations, 'merge')
    },
    log: (message) => console.warn(`[DualSub] ${message}`),
  })
  if (result) {
    console.log(
      `[DualSub] prefetch ${videoKey}: ${result.hits}/${result.total} cached (remote: ${result.remoteStatus})`,
    )
  }
  return result
}
```

- [ ] **Step 3: Make auto-translate coverage-driven**

Replace the head of `autoTranslateCurrentLecture` — from its signature through the `let entries ... catch ... return }` block — with:

```ts
async function autoTranslateCurrentLecture(prefetched?: TranscriptEntry[]): Promise<void> {
  if (!extractor || extractor.site !== 'udemy' || !autoTranslateConfig) return
  const videoKey = extractor.videoKey()
  if (isCurrentFullTranslate(videoKey) && fullTranslateStatus.status === 'running') return

  // Coverage is decided by the caller via needsTranslation(); the daemon
  // still skips every cached line, so only real misses reach the provider.
  fullTranslateStatus = {
    ok: true,
    active: true,
    jobId: null,
    videoKey,
    provider: autoTranslateConfig.provider,
    status: 'running',
    totalChunks: 0,
    completedChunks: 0,
    failedChunks: 0,
    totalLines: 0,
    translatedLines: 0,
    cacheHits: 0,
    startedAt: Date.now(),
    updatedAt: Date.now(),
  }

  let entries = prefetched
  if (!entries) {
    try {
      entries = await extractor.extractFullTranscript(autoTranslateConfig.sourceLang)
    } catch (err) {
      setFullTranslateStatus({
        status: 'failed',
        errorSummary: err instanceof Error ? err.message : String(err),
      })
      console.warn('[DualSub] auto-translate: extract failed:', err)
      return
    }
  }
```

The rest of the function (`if (entries.length === 0) return` onward) stays as is. The old `const cached = await readStoredTranslations(videoKey); if (cached) return` lines are gone.

Note `startFullTranscriptTranslate` calls `ensureOverlay().setTranslations({})`, which would wipe prefetched lines from the overlay until the daemon streams them back as cache hits. Change that line in `startFullTranscriptTranslate` to keep them:

```ts
  currentVideoKey = videoKey
  ensureOverlay()
  startCueObserver()
```

`SET_OVERLAY` and `START_TRANSLATE` from the popup still provide their own maps: `SET_OVERLAY` calls `setTranslations(msg.translations)` itself, and the popup's Translate flow sends `SET_OVERLAY` before `START_TRANSLATE`, so nothing else relied on the wipe.

- [ ] **Step 4: Bootstrap and SPA navigation**

Replace the body of `bootstrap()` after the `await new Promise(...)` block with:

```ts
  await restoreOverlayFromStorage()
  const coverage = await prefetchCurrentLecture()
  if (autoTranslateConfig && needsTranslation(coverage)) {
    await autoTranslateCurrentLecture(coverage?.entries)
  }
```

In the Udemy `handleNavigation` `setTimeout` callback, replace

```ts
      await restoreOverlayFromStorage()
      // If the new lecture wasn't cached but the user previously opted in to
      // translation (autoTranslateConfig present), auto-fire a fresh
      // translate so they don't have to click again on every lecture.
      if (!overlay && autoTranslateConfig) {
        await autoTranslateCurrentLecture()
      }
```

with

```ts
      await restoreOverlayFromStorage()
      const coverage = await prefetchCurrentLecture()
      // The user opted in to translation earlier (autoTranslateConfig); fill
      // whatever prefetch could not find so they never click per lecture.
      if (autoTranslateConfig && needsTranslation(coverage)) {
        await autoTranslateCurrentLecture(coverage?.entries)
      }
```

Update the comment above `bootstrap()` to:

```ts
// On load: restore the sticky auto-translate config first (so SPA nav after
// a content-script restart still inherits user intent), mount any cached
// overlay, prefetch what the daemon/central already have, and only then
// auto-translate the remainder if the user opted in.
```

- [ ] **Step 5: Typecheck, test, build**

Run: `cd extension && npx tsc --noEmit && npx vitest run 2>&1 | tail -4 && npm run build 2>&1 | tail -2`
Expected: no type errors, all tests pass, `✓ built`.

- [ ] **Step 6: Document**

In `codebase.md`'s extension file map add rows:

```
| `src/content/prefetch.ts` | Page-load prefetch with injected deps: extract transcript → `POST /v1/lookup` → `apply()` hits. Never translates. `needsTranslation(result)` is the single rule for whether sticky auto-translate still runs (`hits < total`, or prefetch could not run). |
| `src/shared/langs.ts` | `PREFERRED_SOURCE` / `TARGET_LANG`. Popup translate and content prefetch must derive the same cache keys — change both or neither. |
```

and extend the `src/shared/DaemonClient.ts` row with: `` `lookup(req)` is the one-shot cache-only POST, called directly from the content script. ``

- [ ] **Step 7: Commit**

```bash
cd /home/johnny/Desktop/dualsub-next
git add extension/src/content/index.ts codebase.md
git commit -F - <<'EOF'
feat(extension): prefetch cached translations on load, translate only the rest

On page load and Udemy SPA navigation the content script now asks the daemon
which lines already exist (locally or on the central) and shows them. Sticky
auto-translate runs only when coverage is incomplete, and no longer wipes
prefetched lines from the overlay when it starts.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 8: Deploy the central, verify end to end, push

**Files:** none (deployment + verification)

- [ ] **Step 1: Full daemon verification**

Run: `cd daemon && gofmt -l . ; go vet ./... && go test -race ./... 2>&1 | tail -3`
Expected: `gofmt -l` prints nothing new; all `ok`.

- [ ] **Step 2: Build and install with a backup**

```bash
cd /home/johnny/Desktop/dualsub-next/daemon && go build -o ../dualsub.new ./cmd/dualsub && cd .. \
  && cp -p dualsub "dualsub.pre-lookup-$(date +%Y%m%d-%H%M%S)" \
  && install -m 755 dualsub.new dualsub && rm dualsub.new \
  && systemctl --user restart dualsub.service && sleep 2 && systemctl --user is-active dualsub.service \
  && ss -ltnp | grep -E '7878|7879'
```
Expected: `active`; both ports listening.

- [ ] **Step 3: Exercise the local route against real data**

```bash
python3 - <<'EOF'
import sqlite3, os, json, urllib.request
db = sqlite3.connect("file:"+os.path.expanduser("~/.local/share/dualsub/cache.db")+"?mode=ro", uri=True)
orig, tr = db.execute("select original_text, translated_text from translations where source_lang='en' and target_lang='繁體中文' order by created_at desc limit 1").fetchone()
body = {"video_key": "manual-check", "source_lang": "en", "target_lang": "繁體中文",
        "lines": [{"index": 1, "text": orig}, {"index": 2, "text": "this line is definitely not cached zzz"}],
        "include_remote": True}
req = urllib.request.Request("http://127.0.0.1:7878/v1/lookup", json.dumps(body).encode(), {"Content-Type": "application/json"})
res = json.load(urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=15))
print(res)
assert res["hits"] == 1 and res["total"] == 2 and res["translations"][0]["text"] == tr
assert res["remote_status"] == "disabled"   # this machine IS the central; no central_url configured
print("local lookup OK")
EOF
grep '"kind":"lookup"' ~/.local/share/dualsub/daemon.log | tail -1
grep -c '"kind":"translate_start"' ~/.local/share/dualsub/daemon.log
```
Expected: `local lookup OK`, one `lookup` event with `hits:1`, and the `translate_start` count is unchanged from before this step (run the count once before and once after).

- [ ] **Step 4: Exercise the central route**

```bash
python3 - <<'EOF'
import os, json, urllib.request, urllib.error
token = open(os.path.expanduser("~/.config/dualsub/sync.token")).read().strip()
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
def post(tok):
    req = urllib.request.Request("http://100.108.126.11:7879/v1/lookup",
        json.dumps({"source_lang": "en", "target_lang": "繁體中文", "lines": [{"index": 1, "text": "hello"}]}).encode(),
        {"Content-Type": "application/json", "Authorization": "Bearer " + tok}, method="POST")
    try:
        with opener.open(req, timeout=15) as r: return r.status, json.load(r)
    except urllib.error.HTTPError as e: return e.code, None
print("good token:", post(token))
print("bad token:", post("wrong"))
EOF
grep '"kind":"shared_lookup"' ~/.local/share/dualsub/daemon.log | tail -1
```
Expected: good token → `200` with a `translations` map and `cache_hits`; bad token → `401`; a `shared_lookup` event logged.

- [ ] **Step 5: Reload the extension and verify in Chrome (user does this)**

Ask the user to: reload the unpacked extension at `chrome://extensions`, open a Udemy lecture that was translated before, and confirm subtitles appear (English only until clicked). Then run:

```bash
grep -E '"kind":"(lookup|translate_start)"' ~/.local/share/dualsub/daemon.log | tail -5
```
Expected: a `lookup` event for that lecture's `video_key` with `hits` equal to `lines`, and **no** new `translate_start` line for it.

- [ ] **Step 6: Push**

```bash
cd /home/johnny/Desktop/dualsub-next && git status -s | grep -v '^??' ; git log --oneline origin/main..HEAD && git push origin main
```
Expected: clean tree (only the untracked binary backups), all commits pushed.

---

## Self-review

**Spec coverage**
- §1 local `/v1/lookup` (keys, 2000 cap, `remote_status`, store without outbox) → Task 3.
- §2 central `/v1/lookup` (cache-only, token) → Task 1; legacy aliasing dropped and the spec amended in Task 1 Step 6.
- §3 `Client.Lookup` (404 → unsupported, no circuit trip; outage trips; 10s timeout) → Task 2.
- §4 `langs.ts`, `DaemonClient.lookup`, `prefetchCachedTranslations`, ordering, single in-flight per videoKey (the `apply` guard drops stale results), failure no-ops → Tasks 5–7.
- Observability `lookup` / `shared_lookup` → Tasks 1, 3, 4.
- Error-handling table → Tasks 2, 3, 6.
- Auto-translate after a partial hit (`hits < total`) → Task 6 `needsTranslation`, Task 7 call sites.
- Testing section → every listed case has a test in Tasks 1–3, 5, 6; manual check is Task 8.

**Placeholder scan:** none.

**Type consistency:** `lookupRequest`/`lookupResponse` exist in both `sharedcache` (map form, Task 1) and `server` (list form, Task 3) — different packages, no clash. `RemoteLookup.Lookup` signature matches `(*sharedcache.Client).Lookup`. TS `LookupResponse.remote_status: RemoteStatus` matches Go's four strings; `PrefetchResult.remoteStatus` adds `'error'` only on the extension side.
