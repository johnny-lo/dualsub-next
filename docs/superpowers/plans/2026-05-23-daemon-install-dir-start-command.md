# Daemon-Reported Install Dir Start Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the extension's "daemon offline" popup show a correct, cross-platform start command by having the daemon report its own install directory via `/healthz`, which the extension caches while connected.

**Architecture:** The Go daemon adds an `install_dir` field to its `/healthz` JSON (`filepath.Dir(os.Executable())`). The extension's background worker and popup cache that value in `chrome.storage.local` whenever the daemon is reachable. When offline, the popup builds the start command from the cached path (with `cd`), falling back to a bare command + hint when no path was ever cached.

**Tech Stack:** Go (daemon, `net/http`, `testing`), React 18 + Ant Design + Vite + TypeScript (extension), Chrome MV3 storage/alarms APIs.

Spec: `docs/superpowers/specs/2026-05-23-daemon-install-dir-start-command-design.md`

---

## File Structure

- `daemon/internal/server/handlers.go` — add `installDir()` helper + `install_dir` field on `/healthz`. (modify)
- `daemon/internal/server/server_test.go` — assert `/healthz` returns non-empty `install_dir`. (modify)
- `extension/src/shared/DaemonClient.ts` — widen `health()` return type. (modify)
- `extension/src/background/index.ts` — cache `install_dir` to `chrome.storage.local` on healthy poll. (modify)
- `extension/src/popup/App.tsx` — remove hardcoded path, load cached dir, build command from it, show cold-start hint. (modify)

---

## Task 1: Daemon reports `install_dir` on `/healthz`

**Files:**
- Modify: `daemon/internal/server/handlers.go:1-22`
- Test: `daemon/internal/server/server_test.go:74-90`

- [ ] **Step 1: Write the failing test**

Replace the existing `TestHealthz` (lines 74-90) in `daemon/internal/server/server_test.go` with this version that also asserts `install_dir`:

```go
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
```

Note: `encoding/json` is already imported in this test file (line 6). `strings` may become unused — see Step 4.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd daemon && go test ./internal/server/ -run TestHealthz -v`
Expected: FAIL — `install_dir is empty` (the handler does not emit the field yet).

- [ ] **Step 3: Implement `installDir()` and emit the field**

In `daemon/internal/server/handlers.go`, add `os` and `path/filepath` to the import block (lines 3-13):

```go
import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/config"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
	"github.com/johnny/dualsub-next/daemon/internal/translate"
)
```

Then replace `handleHealthz` (lines 17-22) with:

```go
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"time":        time.Now().UTC().Format(time.RFC3339),
		"install_dir": installDir(),
	})
}

// installDir returns the directory that holds the dualsub binary, which is the
// same directory as the dualsub-watch.sh / dualsub-watch.ps1 helper scripts.
// Returns "" if the path cannot be resolved.
func installDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}
```

- [ ] **Step 4: Run the test to verify it passes (and fix unused import if needed)**

Run: `cd daemon && go test ./internal/server/ -run TestHealthz -v`
Expected: PASS.

If the build fails with `"strings" imported and not used`, remove the `"strings"` line from the test file's import block (line 11). Re-run; expected PASS.

- [ ] **Step 5: Verify the whole daemon still builds and tests pass**

Run: `cd daemon && go build ./... && go test ./...`
Expected: build succeeds; all packages PASS.

- [ ] **Step 6: Commit**

```bash
git add daemon/internal/server/handlers.go daemon/internal/server/server_test.go
git commit -m "feat(daemon): report install_dir on /healthz"
```

---

## Task 2: Extension caches `install_dir` from healthy polls

**Files:**
- Modify: `extension/src/shared/DaemonClient.ts:95-99`
- Modify: `extension/src/background/index.ts:38-49`

No automated test framework exists in `extension/`; verification is `npm run typecheck`.

- [ ] **Step 1: Widen the `health()` return type**

In `extension/src/shared/DaemonClient.ts`, replace the `health()` method (lines 95-99):

```ts
  async health(): Promise<{ status: string; time: string; install_dir?: string }> {
    const res = await fetch(`${this.baseURL}/healthz`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.json()
  }
```

- [ ] **Step 2: Cache `install_dir` in the background health poll**

In `extension/src/background/index.ts`, replace `checkHealth` (lines 38-49):

```ts
async function checkHealth(): Promise<void> {
  let ok = false
  try {
    const res = await fetch(`${DAEMON_URL}/healthz`, {
      signal: AbortSignal.timeout(HEALTH_TIMEOUT_MS),
    })
    ok = res.ok
    if (ok) {
      const body = (await res.json().catch(() => null)) as { install_dir?: unknown } | null
      const dir = body?.install_dir
      if (typeof dir === 'string' && dir) {
        await chrome.storage.local.set({ daemonInstallDir: dir })
      }
    }
  } catch {
    ok = false
  }
  await applyConnectionState(ok)
}
```

- [ ] **Step 3: Typecheck the extension**

Run: `cd extension && npm run typecheck`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add extension/src/shared/DaemonClient.ts extension/src/background/index.ts
git commit -m "feat(extension): cache daemon install_dir from /healthz polls"
```

---

## Task 3: Popup builds the start command from the cached dir

**Files:**
- Modify: `extension/src/popup/App.tsx` — lines 79-80 (remove constant), 102-110 (replace builder), 145-161 (state + memo), 194-211 (cache on connect), and the offline panel (~456-477, hint line).

- [ ] **Step 1: Remove the hardcoded Windows path constant**

In `extension/src/popup/App.tsx`, delete these two lines (79-80):

```ts
// Path to the dualsub-next repo on this machine. Edit if you move the repo.
const DAEMON_DIR_WINDOWS = 'c:\\Users\\j4503\\repos\\dualsub-next'
```

- [ ] **Step 2: Replace `detectStartCommand` with a path-aware builder**

Replace `detectStartCommand` (lines 102-110) with:

```ts
function buildStartCommand(installDir: string | null): string {
  const ua = navigator.userAgent
  const uaPlatform = (navigator as { userAgentData?: { platform?: string } }).userAgentData?.platform ?? ''
  const isWindows = /windows/i.test(uaPlatform) || /windows/i.test(ua)
  if (isWindows) {
    const script = '.\\dualsub-watch.ps1'
    return installDir ? `cd '${installDir}'; ${script}` : script
  }
  const script = './dualsub-watch.sh'
  return installDir ? `cd '${installDir}' && ${script}` : script
}
```

- [ ] **Step 3: Add `installDir` state, load it on mount, and memoize the command**

In the `App` component, replace the `startCommand` memo line (160):

```ts
  const startCommand = useMemo(() => detectStartCommand(), [])
```

with:

```ts
  const [installDir, setInstallDir] = useState<string | null>(null)
  const startCommand = useMemo(() => buildStartCommand(installDir), [installDir])
```

Then add a mount effect to load the cached value. Insert it right before the existing mount effect at line 213 (`useEffect(() => { void refreshDaemon() ...`):

```ts
  useEffect(() => {
    chrome.storage.local.get('daemonInstallDir').then((r) => {
      const dir = r.daemonInstallDir
      if (typeof dir === 'string' && dir) setInstallDir(dir)
    })
  }, [])
```

- [ ] **Step 4: Cache the dir when the popup itself connects**

In `refreshDaemon` (lines 194-211), after the successful `health()` call, persist and apply `install_dir`. Replace the `try` body's first two lines (197-198):

```ts
      const h = await client.health()
      setDaemon({ state: 'connected', serverTime: h.time })
```

with:

```ts
      const h = await client.health()
      setDaemon({ state: 'connected', serverTime: h.time })
      if (h.install_dir) {
        setInstallDir(h.install_dir)
        void chrome.storage.local.set({ daemonInstallDir: h.install_dir })
      }
```

- [ ] **Step 5: Show a cold-start hint when no path is cached**

In the offline panel (the `daemon.state === 'offline'` block, ~lines 456-477), add a hint after the `<Space.Compact>...</Space.Compact>` element and before the `{copiedStart && (...)}` block:

```tsx
            {!installDir && (
              <Typography.Text type="secondary" style={{ display: 'block', fontSize: 11, marginTop: 4 }}>
                找不到腳本的話，請先 cd 到 dualsub 安裝目錄。
              </Typography.Text>
            )}
```

- [ ] **Step 6: Typecheck and build the extension**

Run: `cd extension && npm run typecheck && npm run build`
Expected: no type errors; Vite build succeeds.

- [ ] **Step 7: Commit**

```bash
git add extension/src/popup/App.tsx
git commit -m "feat(extension): build offline start command from cached install_dir"
```

---

## Task 4: Manual end-to-end verification

No commit. Confirm the real behavior in Chrome.

- [ ] **Step 1: Build artifacts**

Run: `cd daemon && go build -o ../dualsub ./cmd/dualsub` and `cd extension && npm run build`.
Expected: both succeed; `dualsub` binary and `extension/dist` exist.

- [ ] **Step 2: Load and exercise the connected path**

1. Start the daemon: `./dualsub serve` (from the repo root).
2. In Chrome: `chrome://extensions` → Developer mode → Load unpacked → select `extension/dist`.
3. Open the popup; confirm it shows connected. In the service-worker console (or DevTools → Application → Storage), confirm `chrome.storage.local` has `daemonInstallDir` set to the repo directory.

- [ ] **Step 3: Verify the offline command uses the cached path**

1. Stop the daemon (Ctrl+C).
2. Reopen the popup; it should show "Daemon not reachable".
3. Confirm the command field reads `cd '<repo dir>' && ./dualsub-watch.sh` (Linux/macOS) and that copying + pasting it into a fresh terminal starts the daemon.

- [ ] **Step 4: Verify the cold-start fallback**

1. In `chrome://extensions`, remove the extension's stored data (or use a fresh Chrome profile) so `daemonInstallDir` is unset, with the daemon stopped.
2. Open the popup; confirm the command is the bare `./dualsub-watch.sh` and the hint line "找不到腳本的話，請先 cd 到 dualsub 安裝目錄。" is shown.

---

## Self-Review

**Spec coverage:**
- Goal 1 (fix Linux missing `cd`) → Task 3 Step 2 (`cd '${installDir}' && ./dualsub-watch.sh`). ✓
- Goal 2 (remove hardcoded `DAEMON_DIR_WINDOWS`) → Task 3 Step 1. ✓
- Goal 3 (daemon reports dir, extension caches, cross-platform) → Task 1 (report), Task 2 (background cache) + Task 3 Step 4 (popup cache), Task 3 Step 2 (build). ✓
- Cold-start fallback + hint → Task 3 Steps 2 & 5. ✓
- Daemon TDD test → Task 1. Extension manual verification → Task 4. ✓
- Non-goal (options page) correctly untouched. ✓

**Placeholder scan:** No TBD/TODO/vague steps; every code step shows full code and exact commands. ✓

**Type consistency:** `installDir` (TS state, string|null), `install_dir` (JSON/Go field + TS optional), `daemonInstallDir` (storage key) used consistently across Tasks 1-3. `buildStartCommand(installDir)` signature matches its memo call. ✓
