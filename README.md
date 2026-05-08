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
   the player as chunks come back from the daemon.
5. Toggle **Live mode** to translate cues on the fly without a full pass.
6. **Extract & Copy** copies the original transcript to the clipboard as a
   fallback for pasting into a web LLM.

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
| GET    | `/v1/config`         | current daemon config                          |
| PUT    | `/v1/config`         | persist config to TOML (restart to apply)      |
