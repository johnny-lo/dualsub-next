package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func modelIDs(ms []ModelInfo) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestOpenAIListModelsFiltersNonTextAndSorts(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		auth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"object":"list","data":[
			{"id":"gpt-4o-mini"},{"id":"text-embedding-3-small"},{"id":"tts-1"},{"id":"whisper-1"},
			{"id":"dall-e-3"},{"id":"gpt-image-1"},{"id":"gpt-4o-audio-preview"},
			{"id":"gpt-4o-realtime-preview"},{"id":"omni-moderation-latest"},
			{"id":"gpt-4o-transcribe"},{"id":"gpt-4.1"}]}`)
	}))
	defer srv.Close()

	got, err := NewOpenAI(OpenAIOptions{APIKey: "k", BaseURL: srv.URL}).(ModelLister).ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"gpt-4.1", "gpt-4o-mini"}; !reflect.DeepEqual(modelIDs(got), want) {
		t.Errorf("ids: got %v, want %v", modelIDs(got), want)
	}
	for _, m := range got {
		if m.Label != m.ID || len(m.Efforts) != 0 {
			t.Errorf("unexpected model info %+v", m)
		}
	}
	if auth != "Bearer k" {
		t.Errorf("Authorization: got %q", auth)
	}
}

func TestOpenAIListModelsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := NewOpenAI(OpenAIOptions{APIKey: "k", BaseURL: srv.URL}).(ModelLister).ListModels(context.Background())
	if pe, ok := err.(*Error); !ok || pe.Code != CodeInvalidKey {
		t.Fatalf("want INVALID_KEY, got %T: %v", err, err)
	}
}

func TestOllamaListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tags" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"qwen2.5:7b","size":1},{"name":"llama3.2:3b","size":2}]}`)
	}))
	defer srv.Close()

	got, err := NewOllama(OllamaOptions{BaseURL: srv.URL}).(ModelLister).ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []ModelInfo{{ID: "qwen2.5:7b", Label: "qwen2.5:7b"}, {ID: "llama3.2:3b", Label: "llama3.2:3b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOllamaListModelsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := NewOllama(OllamaOptions{BaseURL: url}).(ModelLister).ListModels(context.Background())
	if pe, ok := err.(*Error); !ok || pe.Code != CodeNetwork {
		t.Fatalf("want NETWORK, got %T: %v", err, err)
	}
}
