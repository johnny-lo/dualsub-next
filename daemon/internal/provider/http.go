package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// apiKeyParamRE matches credentials carried in URL query strings (Gemini puts
// the API key in `?key=...`). We scrub them out of any error message that
// might surface to the extension or logs.
var apiKeyParamRE = regexp.MustCompile(`(?i)([?&](?:api_?key|key|access_token)=)[^&\s'"]+`)

func sanitizeMessage(s string) string {
	return apiKeyParamRE.ReplaceAllString(s, "${1}REDACTED")
}

type httpClient struct {
	client *http.Client
	name   string
}

func newHTTPClient(name string, timeout time.Duration) *httpClient {
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	return &httpClient{
		name:   name,
		client: &http.Client{Timeout: timeout},
	}
}

func (h *httpClient) postJSON(ctx context.Context, urlStr string, headers map[string]string, body any) ([]byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{Code: CodeBadRequest, Provider: h.name, Message: "marshal request: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, &Error{Code: CodeBadRequest, Provider: h.name, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, mapNetErr(h.name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return raw, mapStatus(h.name, resp.StatusCode, raw)
	}
	return raw, nil
}

func mapNetErr(name string, err error) error {
	msg := sanitizeMessage(err.Error())
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Provider: name, Message: msg, Retryable: true, Cause: err}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return &Error{Code: CodeTimeout, Provider: name, Message: msg, Retryable: true, Cause: err}
	}
	return &Error{Code: CodeNetwork, Provider: name, Message: msg, Retryable: true, Cause: err}
}

func mapStatus(name string, status int, body []byte) error {
	msg := sanitizeMessage(truncate(string(body), 500))
	switch {
	case status == 401 || status == 403:
		return &Error{Code: CodeInvalidKey, Provider: name, Message: msg, Retryable: false}
	case status == 429:
		return &Error{Code: CodeRateLimit, Provider: name, Message: msg, Retryable: true}
	case status == 408 || status == 504:
		return &Error{Code: CodeTimeout, Provider: name, Message: msg, Retryable: true}
	case status >= 500:
		return &Error{Code: CodeServerError, Provider: name, Message: msg, Retryable: true}
	case status == 400:
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "context") || strings.Contains(lower, "token") || strings.Contains(lower, "too long") {
			return &Error{Code: CodeContextTooLong, Provider: name, Message: msg, Retryable: false}
		}
		return &Error{Code: CodeBadRequest, Provider: name, Message: msg, Retryable: false}
	default:
		return &Error{Code: CodeBadRequest, Provider: name, Message: msg, Retryable: false}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
