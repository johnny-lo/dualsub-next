# Design: daemon-reported install dir for the disconnected start command

Date: 2026-05-23

## Problem

When the daemon is offline, the extension popup
(`extension/src/popup/App.tsx`) shows a copyable command to start it. Today
that command is broken in two ways:

- **Linux/macOS**: the command is the bare `./dualsub-watch.sh` with no `cd`,
  so pasting it into any terminal that isn't already in the repo directory
  fails with "No such file or directory".
- **Windows**: the command does `cd` first, but to a **hardcoded** path
  (`DAEMON_DIR_WINDOWS = 'c:\\Users\\j4503\\repos\\dualsub-next'`) that is wrong
  for anyone who installed the repo elsewhere.

A Chrome extension is sandboxed and cannot read the filesystem to discover where
the daemon/scripts live. But the daemon process *does* know its own location.
We reverse-utilize the one channel that already exists between them — the
`/healthz` HTTP endpoint — to carry the install directory to the extension while
the daemon is reachable, so the extension can cache it and use it later when the
daemon goes offline.

## Goals

1. Fix the missing `cd` on the Linux/macOS start command.
2. Remove the hardcoded `DAEMON_DIR_WINDOWS` constant.
3. Have the daemon report its install directory; the extension caches it and
   builds a correct, cross-platform start command with no per-machine source
   edits.

## Non-goals

- The options page error message (`Run ./dualsub serve first` in
  `extension/src/options/App.tsx`). Out of scope — this work targets the popup's
  copyable start-command field.
- Robust shell-escaping of arbitrary paths (single quotes in the path, etc.).
  We keep the existing single-quote style.

## Component 1 — Daemon: expose `install_dir` on `/healthz`

File: `daemon/internal/server/handlers.go`

`handleHealthz` gains an `install_dir` field:

```go
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

`filepath.Dir(os.Executable())` is the directory that holds both the `dualsub`
binary and the `dualsub-watch.sh` / `dualsub-watch.ps1` scripts (the watch
scripts derive `SCRIPT_DIR` the same way). The response becomes:

```json
{ "status": "ok", "time": "...", "install_dir": "/home/johnny/Desktop/dualsub-next" }
```

If the path cannot be resolved, `install_dir` is an empty string. This never
affects the health status itself (still `200 ok`).

New imports: `os`, `path/filepath`.

## Component 2 — Extension: cache the path while connected

- `extension/src/shared/DaemonClient.ts`: extend the `health()` return type to
  `{ status: string; time: string; install_dir?: string }`.
- `extension/src/background/index.ts`: in `checkHealth()`, when the response is
  OK, parse the JSON and, if `install_dir` is a non-empty string, persist it via
  `chrome.storage.local.set({ daemonInstallDir: dir })`. The background worker
  polls every ~30s, so the path is cached passively whenever the daemon is
  alive — even if the user never opens the popup while connected.
- `extension/src/popup/App.tsx`: the popup's own `health()` check, on
  `connected`, also writes `daemonInstallDir` to storage (and local state) so a
  freshly connected session has it immediately.

Storage key: `daemonInstallDir` (string) in `chrome.storage.local`.

## Component 3 — Popup: build the command from the cached path

File: `extension/src/popup/App.tsx`

- Remove the `DAEMON_DIR_WINDOWS` constant.
- Add `installDir: string | null` state, loaded from
  `chrome.storage.local.get('daemonInstallDir')` on mount.
- Replace the no-arg `detectStartCommand()` with a function that takes the
  cached dir:

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

- `startCommand = useMemo(() => buildStartCommand(installDir), [installDir])`.

Resulting command:

| Platform    | Cached dir present              | Cold start (no cache)   |
|-------------|---------------------------------|-------------------------|
| Linux/macOS | `cd '<dir>' && ./dualsub-watch.sh` | `./dualsub-watch.sh`    |
| Windows     | `cd '<dir>'; .\dualsub-watch.ps1`  | `.\dualsub-watch.ps1`   |

- Cold start (no cached dir): the disconnected panel shows an extra secondary
  hint line — "找不到腳本的話，請先 cd 到 dualsub 安裝目錄" — rendered only when
  `installDir` is null.

## Error handling

- `install_dir` empty (daemon) or no stored value (extension) → fall back to the
  cold-start bare command + hint. Nothing breaks.
- This is the genuine catch-22: the path is only learnable while the daemon is
  reachable, so a machine that has never once connected has no cached path. The
  cold-start fallback covers exactly this case.

## Testing

- **Daemon (TDD)**: extend `daemon/internal/server/server_test.go` to assert the
  `/healthz` JSON body contains a non-empty `install_dir` string. Write the
  assertion first, watch it fail, then implement.
- **Extension**: no test framework present. Verify manually: `pnpm build`, load
  the unpacked extension, confirm the flow — connect → path cached → stop daemon
  → popup shows the start command with the correct `cd '<dir>'` prefix; and on a
  profile with no cached value, confirm the cold-start command + hint appear.
