// Package llm is a small client for OpenAI-compatible chat APIs — OpenAI
// itself, Anthropic's and Google's compatibility endpoints, OpenRouter,
// Groq, Mistral, Ollama, vLLM, LiteLLM, and any proxy that speaks
// /v1/models and /v1/chat/completions.
//
// Providers disagree on details; the client adapts instead of failing:
// it retries a rejected max_tokens as max_completion_tokens (and then
// without a limit), reads content that arrives as a string or as parts,
// and strips reasoning blocks some open models emit.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Client talks to one provider.
type Client struct {
	BaseURL string
	APIKey  string
	// HTTP is the client used for requests; nil means a default with a
	// two-minute timeout.
	HTTP *http.Client
}

// Preset is a well-known provider base URL for the settings form.
type Preset struct {
	Name    string
	BaseURL string
	Hint    string
}

// Presets lists providers with OpenAI-compatible endpoints.
var Presets = []Preset{
	{"OpenAI", "https://api.openai.com/v1", "API key from platform.openai.com."},
	{"Anthropic", "https://api.anthropic.com/v1", "Claude via Anthropic's OpenAI-compatible endpoint."},
	{"Google Gemini", "https://generativelanguage.googleapis.com/v1beta/openai", "Gemini via Google's OpenAI-compatible endpoint."},
	{"OpenRouter", "https://openrouter.ai/api/v1", "Many models behind one key."},
	{"Groq", "https://api.groq.com/openai/v1", ""},
	{"Mistral", "https://api.mistral.ai/v1", ""},
	{"Ollama (local)", "http://localhost:11434/v1", "No key needed; models you have pulled."},
}

// NormalizeBaseURL cleans a user-entered base URL: trims whitespace and
// trailing slashes, and drops a pasted /chat/completions or /models
// suffix. It returns an error for anything that is not an http(s) URL.
func NormalizeBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/")
	for _, suffix := range []string{"/chat/completions", "/models"} {
		s = strings.TrimSuffix(s, suffix)
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("enter the provider's base URL, e.g. https://api.openai.com/v1")
	}
	return s, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "keel-cloud")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// apiError extracts the provider's error message from a non-2xx body.
func apiError(status int, body []byte) error {
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	msg := ""
	if json.Unmarshal(body, &env) == nil {
		var obj struct {
			Message string `json:"message"`
		}
		switch {
		case len(env.Error) > 0 && json.Unmarshal(env.Error, &obj) == nil && obj.Message != "":
			msg = obj.Message
		case len(env.Error) > 0:
			var s string
			if json.Unmarshal(env.Error, &s) == nil {
				msg = s
			}
		case env.Message != "":
			msg = env.Message
		case env.Detail != "":
			msg = env.Detail
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("the provider rejected the API key (HTTP %d): %s", status, msg)
	case http.StatusNotFound:
		return fmt.Errorf("not found (HTTP 404) — check the base URL and the model id: %s", msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limited by the provider (HTTP 429): %s", msg)
	}
	return fmt.Errorf("HTTP %d from the provider: %s", status, msg)
}

// ListModels returns the model ids the provider offers, sorted.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	data, status, err := c.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", c.BaseURL, err)
	}
	if status/100 != 2 {
		return nil, apiError(status, data)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		// Ollama's native shape, in case someone points at it directly.
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unexpected response from %s/models", c.BaseURL)
	}
	seen := map[string]bool{}
	var ids []string
	for _, m := range out.Data {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	for _, m := range out.Models {
		if m.Name != "" && !seen[m.Name] {
			seen[m.Name] = true
			ids = append(ids, m.Name)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// nonChat matches model ids that cannot answer a chat request, to keep the
// picker short on providers that list everything.
var nonChat = regexp.MustCompile(`(?i)embed|whisper|^tts|-tts|dall-e|moderation|davinci|babbage|realtime|transcribe|audio|image|sora|imagen|veo|aqa|-search-|rerank|ocr|codestral-embed`)

// ChatModels filters ids down to those likely to support chat.
func ChatModels(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !nonChat.MatchString(id) {
			out = append(out, id)
		}
	}
	return out
}

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string    `json:"model"`
	Messages            []Message `json:"messages"`
	Stream              bool      `json:"stream"`
	MaxTokens           int       `json:"max_tokens,omitempty"`
	MaxCompletionTokens int       `json:"max_completion_tokens,omitempty"`
}

// Chat sends one conversation and returns the assistant's text. maxTokens
// bounds the reply (0 = provider default).
func (c *Client) Chat(ctx context.Context, model string, msgs []Message, maxTokens int) (string, error) {
	if model == "" {
		return "", errors.New("no model selected")
	}
	attempts := []chatRequest{{Model: model, Messages: msgs, MaxTokens: maxTokens}}
	if maxTokens > 0 {
		attempts = append(attempts,
			chatRequest{Model: model, Messages: msgs, MaxCompletionTokens: maxTokens},
			chatRequest{Model: model, Messages: msgs},
		)
	}
	var lastErr error
	for i, req := range attempts {
		data, status, err := c.do(ctx, http.MethodPost, "/chat/completions", req)
		if err != nil {
			return "", fmt.Errorf("could not reach %s: %w", c.BaseURL, err)
		}
		if status/100 != 2 {
			lastErr = apiError(status, data)
			// A 400 complaining about the token-limit parameter means the
			// provider wants the other spelling (or none); try the next form.
			if status == http.StatusBadRequest && i+1 < len(attempts) && mentionsTokenLimit(data) {
				continue
			}
			return "", lastErr
		}
		return parseChat(data)
	}
	return "", lastErr
}

func mentionsTokenLimit(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "max_tokens") || strings.Contains(s, "max_completion_tokens")
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// parseChat reads the assistant text out of a completion response,
// tolerating the content-as-parts shape and stripping reasoning blocks.
func parseChat(data []byte) (string, error) {
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("the provider returned no completion")
	}
	choice := out.Choices[0]
	var text string
	if err := json.Unmarshal(choice.Message.Content, &text); err != nil {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(choice.Message.Content, &parts) == nil {
			var b strings.Builder
			for _, p := range parts {
				if p.Type == "" || p.Type == "text" || p.Type == "output_text" {
					b.WriteString(p.Text)
				}
			}
			text = b.String()
		}
	}
	text = strings.TrimSpace(thinkRe.ReplaceAllString(text, ""))
	if text == "" {
		if choice.FinishReason == "length" {
			return "", errors.New("the model ran out of output tokens before answering")
		}
		return "", errors.New("the model returned an empty reply")
	}
	return text, nil
}

// Ping asks the model for a one-word reply to prove the base URL, key,
// and model id work together. It returns the model's reply.
func (c *Client) Ping(ctx context.Context, model string) (string, error) {
	reply, err := c.Chat(ctx, model, []Message{
		{Role: "system", Content: "You are a connectivity check for Keel. Follow the instruction exactly."},
		{Role: "user", Content: "Reply with the single word OK."},
	}, 64)
	if err != nil {
		return "", err
	}
	if !strings.Contains(strings.ToLower(reply), "ok") {
		return reply, fmt.Errorf("the model answered, but not with OK: %q", truncate(reply, 120))
	}
	return reply, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
