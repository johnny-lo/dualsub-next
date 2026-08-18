package sharedcache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

var errCircuitOpen = errors.New("shared cache temporarily unavailable")

type ClientOptions struct {
	BaseURL        string
	Token          string
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	RetryDelay     time.Duration
}

type Client struct {
	baseURL    string
	token      string
	http       *http.Client
	retryDelay time.Duration

	mu               sync.Mutex
	unavailableUntil time.Time
}

func NewClient(opts ClientOptions) (*Client, error) {
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid shared cache URL %q", opts.BaseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("shared cache URL must use http or https")
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 800 * time.Millisecond
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 6 * time.Minute
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = 10 * time.Second
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The configured destination is a tailnet peer. Sending it through a host
	// HTTP proxy both leaks metadata and commonly breaks MagicDNS resolution.
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: opts.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = opts.RequestTimeout

	return &Client{
		baseURL: baseURL,
		token:   opts.Token,
		http: &http.Client{
			Transport: transport,
			Timeout:   opts.RequestTimeout,
		},
		retryDelay: opts.RetryDelay,
	}, nil
}

func (c *Client) Resolve(ctx context.Context, providerName, defaultModel string, in provider.Request) (provider.Response, error) {
	if err := c.available(); err != nil {
		return provider.Response{}, err
	}
	model := in.Model
	if model == "" {
		model = defaultModel
	}
	req := resolveRequest{
		Provider: providerName, Model: model, SourceLang: in.SourceLang,
		TargetLang: in.TargetLang, Lines: in.Lines,
	}
	var payload resolveResponse
	if err := c.post(ctx, "/v1/resolve", req, &payload); err != nil {
		c.markUnavailable()
		return provider.Response{}, err
	}

	lines := make([]provider.TranslatedLine, 0, len(in.Lines))
	for _, line := range in.Lines {
		key := cache.Key(providerName, model, in.SourceLang, in.TargetLang, line.Text)
		translated, ok := payload.Translations[key]
		if !ok {
			c.markUnavailable()
			return provider.Response{}, fmt.Errorf("shared cache omitted translation for line %d", line.Index)
		}
		lines = append(lines, provider.TranslatedLine{Index: line.Index, Text: translated})
	}
	c.markAvailable()
	return provider.Response{Lines: lines}, nil
}

func (c *Client) Import(ctx context.Context, entries []cache.TranslationEntry) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if err := c.available(); err != nil {
		return nil, err
	}
	var payload importResponse
	if err := c.post(ctx, "/v1/import", importRequest{Entries: entries}, &payload); err != nil {
		c.markUnavailable()
		return nil, err
	}
	c.markAvailable()
	return payload.Acknowledged, nil
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("shared cache HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode shared cache response: %w", err)
	}
	return nil
}

func (c *Client) available() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.unavailableUntil) {
		return errCircuitOpen
	}
	return nil
}

func (c *Client) markUnavailable() {
	c.mu.Lock()
	c.unavailableUntil = time.Now().Add(c.retryDelay)
	c.mu.Unlock()
}

func (c *Client) markAvailable() {
	c.mu.Lock()
	c.unavailableUntil = time.Time{}
	c.mu.Unlock()
}

type FallbackProvider struct {
	local  provider.Provider
	remote *Client
}

func NewFallbackProvider(local provider.Provider, remote *Client) *FallbackProvider {
	return &FallbackProvider{local: local, remote: remote}
}

func (p *FallbackProvider) Name() string         { return p.local.Name() }
func (p *FallbackProvider) DefaultModel() string { return p.local.DefaultModel() }

func (p *FallbackProvider) Translate(ctx context.Context, in provider.Request) (provider.Response, error) {
	if res, err := p.remote.Resolve(ctx, p.Name(), p.DefaultModel(), in); err == nil {
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return provider.Response{}, err
	}
	res, err := p.local.Translate(ctx, in)
	if err == nil {
		res.QueueForSync = true
	}
	return res, err
}
