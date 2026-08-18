// Package translate orchestrates chunked, parallel, retrying translation jobs
// against a configurable Provider, persisting line-level results in the cache
// so that retries do not waste API calls.
package translate

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

type Config struct {
	ChunkSize   int           // default 30
	Concurrency int           // default 3
	MaxAttempts int           // default 3 (initial + 2 retries)
	BaseBackoff time.Duration // default 1s
}

func (c *Config) defaults() {
	if c.ChunkSize <= 0 {
		c.ChunkSize = 30
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 3
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 1 * time.Second
	}
}

type Orchestrator struct {
	providers map[string]provider.Provider
	cache     *cache.Cache
	cfg       Config

	// sleep is exposed for tests; defaults to time.Sleep.
	sleep func(d time.Duration)
}

func New(providers map[string]provider.Provider, c *cache.Cache, cfg Config) *Orchestrator {
	cfg.defaults()
	return &Orchestrator{
		providers: providers,
		cache:     c,
		cfg:       cfg,
		sleep:     time.Sleep,
	}
}

type Input struct {
	VideoKey   string
	Title      string
	Site       string
	Provider   string
	Model      string
	SourceLang string
	TargetLang string
	Lines      []provider.Line
}

const (
	codeBadInput        = "BAD_INPUT"
	codeUnknownProvider = "UNKNOWN_PROVIDER"
	codeCacheLookupFail = "CACHE_LOOKUP_FAILED"
	codeJobCreateFail   = "JOB_CREATE_FAILED"
	codeJobUpdateFail   = "JOB_UPDATE_FAILED"
	codeCacheStoreFail  = "CACHE_STORE_FAILED"
)

// Translate runs the chunked translation flow and emits events on out.
// The caller owns reading from out; Translate closes the channel before returning.
// Translate itself returns nil for normal completion (including partial). It only
// returns a non-nil error for setup failures (unknown provider, bad input).
func (o *Orchestrator) Translate(ctx context.Context, in Input, out chan<- Event) error {
	defer close(out)

	if len(in.Lines) == 0 {
		err := errors.New("no lines to translate")
		emitFatal(ctx, out, codeBadInput, err)
		return err
	}
	prov, ok := o.providers[in.Provider]
	if !ok {
		err := fmt.Errorf("unknown provider %q", in.Provider)
		emitFatal(ctx, out, codeUnknownProvider, err)
		return err
	}
	model := canonicalModel(prov, in.Model)

	jobID := uuid.NewString()

	// 1. Cache lookup, line-by-line.
	cachedLines, todoLines, err := o.partitionByCache(ctx, in, model)
	if err != nil {
		err = fmt.Errorf("cache lookup: %w", err)
		emitFatal(ctx, out, codeCacheLookupFail, err)
		return err
	}

	// 2. Chunk the to-do lines.
	chunks := chunkLines(todoLines, o.cfg.ChunkSize)
	totalChunks := len(chunks)

	// 3. Persist a job record.
	if err := o.cache.CreateJob(ctx, cache.Job{
		ID: jobID, VideoKey: in.VideoKey, Provider: in.Provider, Model: model,
		Status: "running", TotalChunks: totalChunks,
	}); err != nil {
		err = fmt.Errorf("create job: %w", err)
		emitFatal(ctx, out, codeJobCreateFail, err)
		return err
	}

	// 4. Emit job-created.
	out <- Event{Type: EventJobCreated, Payload: JobCreatedPayload{
		JobID:       jobID,
		TotalChunks: totalChunks,
		TotalLines:  len(in.Lines),
		CacheHits:   len(cachedLines),
	}}

	// 5. Emit cache hits as chunk 0 (if any).
	if len(cachedLines) > 0 {
		out <- Event{Type: EventChunkDone, Payload: ChunkDonePayload{
			Chunk: 0, Source: "cache", Lines: cachedLines,
		}}
	}

	// 6. Run worker pool over LLM chunks.
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		completed int
		failed    int
	)

	sem := make(chan struct{}, o.cfg.Concurrency)
	for i, ch := range chunks {
		i, ch := i, ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			ok := o.runChunk(ctx, prov, in, model, i+1, ch, out)
			mu.Lock()
			if ok {
				completed++
			} else {
				failed++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 7. Final status + done event.
	status := "completed"
	summary := ""
	if err := ctx.Err(); err != nil {
		failed = totalChunks - completed
		summary = err.Error()
	}
	if failed > 0 && completed > 0 {
		status = "partial"
	} else if failed > 0 && completed == 0 {
		status = "failed"
	}
	if err := o.cache.UpdateJob(context.WithoutCancel(ctx), jobID, status, completed, failed, summary); err != nil {
		emitFatal(ctx, out, codeJobUpdateFail, fmt.Errorf("update job: %w", err))
	}

	out <- Event{Type: EventDone, Payload: DonePayload{
		JobID:     jobID,
		Total:     totalChunks,
		Completed: completed,
		Failed:    failed,
		CacheHits: len(cachedLines),
	}}
	return nil
}

func canonicalModel(prov provider.Provider, requested string) string {
	if requested != "" {
		return requested
	}
	return prov.DefaultModel()
}

func emitFatal(ctx context.Context, out chan<- Event, code string, err error) {
	select {
	case out <- Event{Type: EventFatal, Payload: FatalPayload{Code: code, Message: err.Error()}}:
	case <-ctx.Done():
	}
}

func (o *Orchestrator) partitionByCache(ctx context.Context, in Input, model string) (cached []provider.TranslatedLine, todo []provider.Line, err error) {
	keys := make([]string, len(in.Lines))
	for i, l := range in.Lines {
		keys[i] = cache.Key(in.Provider, model, in.SourceLang, in.TargetLang, l.Text)
	}
	hits, err := o.cache.LookupTranslations(ctx, keys)
	if err != nil {
		return nil, nil, err
	}
	for i, l := range in.Lines {
		if t, ok := hits[keys[i]]; ok {
			cached = append(cached, provider.TranslatedLine{Index: l.Index, Text: t})
		} else {
			todo = append(todo, l)
		}
	}
	return
}

func chunkLines(lines []provider.Line, size int) [][]provider.Line {
	if len(lines) == 0 {
		return nil
	}
	var out [][]provider.Line
	for i := 0; i < len(lines); i += size {
		end := i + size
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, lines[i:end])
	}
	return out
}

func (o *Orchestrator) runChunk(
	ctx context.Context,
	prov provider.Provider,
	in Input,
	model string,
	chunkNum int,
	chunk []provider.Line,
	out chan<- Event,
) (success bool) {
	for attempt := 1; attempt <= o.cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return false
		}
		req := provider.Request{
			Lines: chunk, SourceLang: in.SourceLang, TargetLang: in.TargetLang, Model: model,
		}
		res, err := prov.Translate(ctx, req)
		if err == nil {
			if err := o.persistChunk(ctx, in, model, prov.Name(), chunk, res.Lines, res.QueueForSync); err != nil && ctx.Err() == nil {
				out <- Event{Type: EventChunkError, Payload: ChunkErrorPayload{
					Chunk: chunkNum, Code: codeCacheStoreFail,
					Message:   "translation succeeded but cache write failed: " + err.Error(),
					Retryable: false, Attempt: attempt, Final: true,
				}}
			}
			out <- Event{Type: EventChunkDone, Payload: ChunkDonePayload{
				Chunk: chunkNum, Source: "llm", Lines: res.Lines,
			}}
			return true
		}

		var pe *provider.Error
		retryable := errors.As(err, &pe) && pe.Retryable
		final := !retryable || attempt >= o.cfg.MaxAttempts

		code, msg := "UNKNOWN", err.Error()
		if pe != nil {
			code = pe.Code
			msg = pe.Message
		}
		out <- Event{Type: EventChunkError, Payload: ChunkErrorPayload{
			Chunk: chunkNum, Code: code, Message: msg,
			Retryable: retryable, Attempt: attempt, Final: final,
		}}

		if final {
			return false
		}
		o.sleep(o.backoff(attempt))
	}
	return false
}

func (o *Orchestrator) backoff(attempt int) time.Duration {
	// Exponential backoff with up to 25% jitter.
	d := o.cfg.BaseBackoff << (attempt - 1)
	jitter := time.Duration(rand.Int63n(int64(d) / 4))
	return d + jitter
}

func (o *Orchestrator) persistChunk(
	ctx context.Context,
	in Input,
	model string,
	provName string,
	originals []provider.Line,
	translated []provider.TranslatedLine,
	queueForSync bool,
) error {
	byIdx := make(map[int]string, len(translated))
	for _, t := range translated {
		byIdx[t.Index] = t.Text
	}
	entries := make([]cache.TranslationEntry, 0, len(originals))
	for _, l := range originals {
		text, ok := byIdx[l.Index]
		if !ok {
			continue
		}
		entries = append(entries, cache.TranslationEntry{
			Key:            cache.Key(in.Provider, model, in.SourceLang, in.TargetLang, l.Text),
			Provider:       provName,
			Model:          model,
			SourceLang:     in.SourceLang,
			TargetLang:     in.TargetLang,
			OriginalText:   l.Text,
			TranslatedText: text,
		})
	}
	if queueForSync {
		return o.cache.StoreTranslationsForSync(ctx, entries)
	}
	return o.cache.StoreTranslations(ctx, entries)
}
