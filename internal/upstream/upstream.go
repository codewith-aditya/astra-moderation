// Package upstream talks to an OpenAI-compatible multimodal server (vLLM,
// SGLang, TGI, llama.cpp server, hosted providers) and requests structured
// moderation JSON using guided decoding.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/schema"
)

// Part is one content part in an OpenAI-style message
// ({"type":"image_url","image_url":{"url":"data:..."}}).
type Part map[string]any

// Result is a normalized model response.
type Result struct {
	Scores    map[string]float64 `json:"scores"`
	Verdict   string             `json:"verdict"`
	Reason    string             `json:"reason"`
	Model     string             `json:"model"`
	TokensIn  int                `json:"tokens_in"`
	TokensOut int                `json:"tokens_out"`
}

// HTTPError describes a non-200 upstream response.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("upstream %d: %s", e.Status, e.Message)
}

// Client is an OpenAI-compatible moderation client.
type Client struct {
	URL             string
	Model           string
	APIKey          string
	DisableThinking bool
	HTTP            *http.Client
	MaxTokens       int
}

// New builds a Client with sensible defaults.
func New(url, model, apiKey string, disableThinking bool, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	return &Client{
		URL:             url,
		Model:           model,
		APIKey:          apiKey,
		DisableThinking: disableThinking,
		HTTP:            &http.Client{Timeout: timeout},
		MaxTokens:       700,
	}
}

func (c *Client) requestBody(mediaKind string, parts []Part, maxTokens int) map[string]any {
	content := make([]Part, 0, len(parts)+1)
	content = append(content, Part{"type": "text", "text": schema.Prompt(mediaKind)})
	content = append(content, parts...)

	body := map[string]any{
		"model":       c.Model,
		"max_tokens":  maxTokens,
		"temperature": 0,
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "moderation", "schema": schema.JSONSchema()},
		},
		"messages": []map[string]any{{"role": "user", "content": content}},
	}
	if c.DisableThinking {
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	}
	return body
}

// Moderate sends the media parts and returns a parsed result plus latency in
// milliseconds. Invalid or truncated JSON is retried once with a larger token
// budget.
func (c *Client) Moderate(ctx context.Context, mediaKind string, parts []Part) (*Result, int64, error) {
	start := time.Now()
	res, err := c.call(ctx, mediaKind, parts, c.MaxTokens)
	ms := time.Since(start).Milliseconds()
	if err == nil {
		return res, ms, nil
	}
	if _, ok := err.(*HTTPError); ok {
		return nil, ms, err
	}
	// Retry once with more room for the JSON body.
	start = time.Now()
	res2, err2 := c.call(ctx, mediaKind, parts, c.MaxTokens*3/2)
	ms += time.Since(start).Milliseconds()
	if err2 != nil {
		return nil, ms, err2
	}
	return res2, ms, nil
}

func (c *Client) call(ctx context.Context, mediaKind string, parts []Part, maxTokens int) (*Result, error) {
	payload, err := json.Marshal(c.requestBody(mediaKind, parts, maxTokens))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return nil, &HTTPError{Status: resp.StatusCode, Message: msg}
	}
	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode upstream envelope: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("upstream returned no choices")
	}
	ch := parsed.Choices[0]
	var out struct {
		Categories map[string]float64 `json:"categories"`
		Verdict    string             `json:"verdict"`
		Reason     string             `json:"reason"`
	}
	if err := json.Unmarshal([]byte(ch.Message.Content), &out); err != nil {
		return nil, fmt.Errorf("model output invalid JSON (finish=%s): %.200s", ch.FinishReason, ch.Message.Content)
	}
	scores := make(map[string]float64, len(schema.Categories))
	for _, c := range schema.Categories {
		scores[c] = out.Categories[c]
	}
	return &Result{
		Scores:    scores,
		Verdict:   out.Verdict,
		Reason:    out.Reason,
		Model:     parsed.Model,
		TokensIn:  parsed.Usage.PromptTokens,
		TokensOut: parsed.Usage.CompletionTokens,
	}, nil
}

// IsVideoUnsupported reports whether the upstream rejected native video input
// (vLLM `--limit-mm-per-prompt video=0`), so callers can fall back to frames.
func IsVideoUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "video(s) may be provided") ||
		strings.Contains(msg, "video is not supported") ||
		strings.Contains(msg, "limit-mm-per-prompt")
}
