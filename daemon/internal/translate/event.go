package translate

import "github.com/johnny/dualsub-next/daemon/internal/provider"

type EventType string

const (
	EventJobCreated EventType = "job-created"
	EventChunkDone  EventType = "chunk-done"
	EventChunkError EventType = "chunk-error"
	EventDone       EventType = "done"
	EventFatal      EventType = "fatal"
)

type Event struct {
	Type    EventType
	Payload any
}

type JobCreatedPayload struct {
	JobID       string `json:"job_id"`
	TotalChunks int    `json:"total_chunks"` // LLM chunks only; cache hits are reported separately
	TotalLines  int    `json:"total_lines"`
	CacheHits   int    `json:"cache_hits"`
}

// ChunkDonePayload is emitted both for cache hits (Source="cache", Chunk=0)
// and for successful LLM chunks (Source="llm", Chunk=1..TotalChunks).
type ChunkDonePayload struct {
	Chunk  int                       `json:"chunk"`
	Source string                    `json:"source"`
	Lines  []provider.TranslatedLine `json:"lines"`
}

type ChunkErrorPayload struct {
	Chunk     int    `json:"chunk"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Attempt   int    `json:"attempt"`
	Final     bool   `json:"final"`
}

type DonePayload struct {
	JobID     string `json:"job_id"`
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	CacheHits int    `json:"cache_hits"`
}

type FatalPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
