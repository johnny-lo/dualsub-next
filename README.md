# DualSub Next

Bilingual subtitle assistant for Netflix and Udemy. A local Go daemon handles
LLM translation against multiple providers (OpenAI, Google Gemini, Ollama; Claude
reserved); a Chrome extension extracts transcripts from the player and renders
the bilingual overlay.

## Layout

- `daemon/` — Go translation daemon (HTTP + SSE, SQLite line-level cache,
  multi-provider adapters, chunked retry orchestrator)
- `extension/` — Chrome extension (Vite + CRXJS + React 18 + Antd v5)

## Quick start

### 1. Build and configure the daemon

```bash
cd daemon
go build -o ../dualsub ./cmd/dualsub
../dualsub config init   # writes ~/.config/dualsub/config.toml
```

Edit `~/.config/dualsub/config.toml` and uncomment at least one provider, for
example:

```toml
[providers.gemini]
api_key = "AIza..."
default_model = "gemini-2.5-flash"
```

(You can also do this from the extension's Options page once the daemon is
running.)

Then start the daemon:

```bash
./dualsub serve
```

On Windows, you can run it without keeping a terminal open:

```powershell
.\dualsub-bg.ps1 start
.\dualsub-bg.ps1 status
.\dualsub-bg.ps1 stop
```

To start the daemon automatically when Windows signs in:

```powershell
.\dualsub-bg.ps1 install-startup
```

Logs are written under `%LOCALAPPDATA%\DualSub Next\`.

On Linux, the equivalent helper is:

```bash
chmod +x ./dualsub-bg.sh
./dualsub-bg.sh start
./dualsub-bg.sh status
./dualsub-bg.sh stop
```

For a user-level systemd service:

```bash
./dualsub-bg.sh install-systemd
```

Logs are written under `${XDG_STATE_HOME:-~/.local/state}/dualsub/`.

### 2. Load the extension

```bash
cd extension
npm install
npm run build
```

In Chrome, open `chrome://extensions/`, enable Developer Mode, click
**Load unpacked**, and pick `extension/dist/`.

### 3. Use it

1. Open a Udemy lecture or Netflix `/watch/<id>` page.
2. Make sure the player's subtitles are enabled.
3. Click the DualSub Next toolbar icon.
4. Pick a provider and click **Translate**. The bilingual overlay appears on
   the player as chunks come back from the daemon. The translate stream runs in
   the background service worker, so it keeps translating if the popup closes.
5. Toggle **Live mode** to translate cues on the fly without a full pass.
6. Drag the bilingual overlay to reposition it; the position is remembered
   (stored as normalized coordinates) and stays stable across normal,
   fullscreen, and resized windows.
7. Use **Clear** in Recent Jobs to clear job history without deleting cached
   translations.
8. **Extract & Copy** copies the original transcript to the clipboard as a
   fallback for pasting into a web LLM.
9. If subtitles aren't detected (Udemy occasionally renames its caption DOM),
   open the popup's **Caption DOM Snapshot (debug)** panel and click
   **Dump caption DOM** to capture a diagnostic snapshot of the page's caption
   elements.

## Development

```bash
# daemon: tests
cd daemon && go test ./...

# daemon: live integration tests against your real API keys
GEMINI_API_KEY=...   go test -tags=integration ./internal/provider/ -run TestGeminiLive -v
OPENAI_API_KEY=...   go test -tags=integration ./internal/provider/ -run TestOpenAILive -v
OLLAMA_TEST=1        go test -tags=integration ./internal/provider/ -run TestOllamaLive -v

# extension: typecheck + bundle
cd extension && npm run build
```

## Daemon HTTP API

| Method | Path                 | Notes                                          |
|--------|----------------------|------------------------------------------------|
| GET    | `/healthz`           | liveness                                       |
| GET    | `/v1/providers`      | list configured providers                      |
| POST   | `/v1/translate`      | streams chunked translation as SSE             |
| GET    | `/v1/jobs?limit=N`   | recent jobs (most recent first)                |
| DELETE | `/v1/jobs`           | clear job history only; keeps translation cache |
| GET    | `/v1/config`         | current daemon config                          |
| PUT    | `/v1/config`         | persist config to TOML (restart to apply)      |

## Acknowledgements

The Udemy three-tier subtitle extraction strategy was inspired by
[ChenYCL/chrome-extension-udemy-translate](https://github.com/ChenYCL/chrome-extension-udemy-translate)
(MIT). This repo is a clean-room rewrite in Go + TypeScript with a different
architecture (separate translation daemon); no source was copied directly.

## License

MIT — see [LICENSE](./LICENSE).
