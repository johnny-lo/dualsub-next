package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type OllamaOptions struct {
	BaseURL      string // default: http://127.0.0.1:11434
	DefaultModel string // default: qwen2.5:7b
	Timeout      time.Duration
}

type ollamaProvider struct {
	opts OllamaOptions
	http *httpClient
}

func NewOllama(opts OllamaOptions) Provider {
	if opts.BaseURL == "" {
		opts.BaseURL = "http://127.0.0.1:11434"
	}
	if opts.DefaultModel == "" {
		opts.DefaultModel = "qwen2.5:7b"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute // local models can be slow on cold start
	}
	return &ollamaProvider{
		opts: opts,
		http: newHTTPClient("ollama", opts.Timeout),
	}
}

func (p *ollamaProvider) Name() string { return "ollama" }

func (p *ollamaProvider) DefaultModel() string { return p.opts.DefaultModel }

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
}

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  ollamaOptions   `json:"options"`
}

type ollamaResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

func (p *ollamaProvider) Translate(ctx context.Context, in Request) (Response, error) {
	model := in.Model
	if model == "" {
		model = p.opts.DefaultModel
	}
	system, user := BuildPrompt(in)

	body := ollamaRequest{
		Model: model,
		Messages: []ollamaMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Stream:  false,
		Options: ollamaOptions{Temperature: 0.3},
	}

	raw, err := p.http.postJSON(ctx, p.opts.BaseURL+"/api/chat", nil, body)
	if err != nil {
		return Response{}, err
	}

	var parsed ollamaResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "ollama", Message: fmt.Sprintf("decode response: %v", err), Cause: err}
	}
	if parsed.Message.Content == "" {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "ollama", Message: "empty content in response"}
	}
	lines, err := ParseResponse(parsed.Message.Content, in.Lines, "ollama")
	if err != nil {
		return Response{}, err
	}
	return Response{Lines: lines, Raw: parsed.Message.Content}, nil
}
