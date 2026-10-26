package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type OpenAIOptions struct {
	APIKey       string
	BaseURL      string // default: https://api.openai.com/v1
	DefaultModel string // default: gpt-4o-mini
	Timeout      time.Duration
}

type openaiProvider struct {
	opts OpenAIOptions
	http *httpClient
}

func NewOpenAI(opts OpenAIOptions) Provider {
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.openai.com/v1"
	}
	if opts.DefaultModel == "" {
		opts.DefaultModel = "gpt-4o-mini"
	}
	return &openaiProvider{
		opts: opts,
		http: newHTTPClient("openai", opts.Timeout),
	}
}

func (p *openaiProvider) Name() string { return "openai" }

func (p *openaiProvider) DefaultModel() string { return p.opts.DefaultModel }

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (p *openaiProvider) Translate(ctx context.Context, in Request) (Response, error) {
	if p.opts.APIKey == "" {
		return Response{}, &Error{Code: CodeMissingConfig, Provider: "openai", Message: "API key not set"}
	}
	model := in.Model
	if model == "" {
		model = p.opts.DefaultModel
	}
	system, user := BuildPrompt(in)

	body := openaiRequest{
		Model: model,
		Messages: []openaiMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.3,
	}
	headers := map[string]string{"Authorization": "Bearer " + p.opts.APIKey}

	raw, err := p.http.postJSON(ctx, p.opts.BaseURL+"/chat/completions", headers, body)
	if err != nil {
		return Response{}, err
	}

	var parsed openaiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "openai", Message: fmt.Sprintf("decode response: %v", err), Cause: err}
	}
	if len(parsed.Choices) == 0 {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "openai", Message: "no choices in response"}
	}
	content := parsed.Choices[0].Message.Content
	lines, err := ParseResponse(content, in.Lines, "openai")
	if err != nil {
		return Response{}, err
	}
	return Response{Lines: lines, Raw: content}, nil
}

// openaiNonTextMarkers flag model ids that cannot do chat translation.
var openaiNonTextMarkers = []string{
	"embedding", "tts", "whisper", "dall-e", "image", "audio", "realtime", "moderation", "transcribe",
}

func parseOpenAIModels(raw []byte) ([]ModelInfo, error) {
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &Error{Code: CodeParseFailed, Provider: "openai", Message: fmt.Sprintf("decode models: %v", err), Cause: err}
	}
	out := make([]ModelInfo, 0, len(parsed.Data))
next:
	for _, m := range parsed.Data {
		for _, marker := range openaiNonTextMarkers {
			if strings.Contains(m.ID, marker) {
				continue next
			}
		}
		out = append(out, ModelInfo{ID: m.ID, Label: m.ID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (p *openaiProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	raw, err := p.http.getJSON(ctx, p.opts.BaseURL+"/models", map[string]string{
		"Authorization": "Bearer " + p.opts.APIKey,
	})
	if err != nil {
		return nil, err
	}
	return parseOpenAIModels(raw)
}
