package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		" https://api.openai.com/v1/ ":               "https://api.openai.com/v1",
		"https://api.openai.com/v1/chat/completions": "https://api.openai.com/v1",
		"http://localhost:11434/v1/models":           "http://localhost:11434/v1",
	} {
		got, err := NormalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "api.openai.com/v1", "ftp://x"} {
		if _, err := NormalizeBaseURL(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestChatModels(t *testing.T) {
	got := ChatModels([]string{"gpt-4o-mini", "text-embedding-3-small", "whisper-1", "dall-e-3", "models/gemini-2.0-flash", "tts-1"})
	if strings.Join(got, ",") != "gpt-4o-mini,models/gemini-2.0-flash" {
		t.Errorf("ChatModels = %v", got)
	}
}

// provider is a fake OpenAI-compatible server with configurable quirks.
type provider struct {
	rejectMaxTokens bool // 400 on max_tokens, accept max_completion_tokens
	partsContent    bool // content as an array of parts
	think           bool // wrap the answer in <think> blocks
	requests        []map[string]any
	auth            string
}

func (p *provider) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		p.auth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "b-model"}, {"id": "a-model"}, {"id": "text-embedding-3"}}})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		p.requests = append(p.requests, req)
		if p.rejectMaxTokens {
			if _, ok := req["max_tokens"]; ok {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."}})
				return
			}
		}
		var content any = "OK"
		if p.think {
			content = "<think>hmm\nlet me see</think>\n\nOK"
		}
		if p.partsContent {
			content = []map[string]string{{"type": "text", "text": "O"}, {"type": "text", "text": "K"}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}})
	})
	return mux
}

func TestListModelsAndPing(t *testing.T) {
	p := &provider{}
	srv := httptest.NewServer(p.handler())
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "sk-test"}
	ids, err := c.ListModels(context.Background())
	if err != nil || strings.Join(ids, ",") != "a-model,b-model,text-embedding-3" {
		t.Fatalf("ListModels = %v, %v", ids, err)
	}
	if p.auth != "Bearer sk-test" {
		t.Errorf("auth header = %q", p.auth)
	}
	reply, err := c.Ping(context.Background(), "a-model")
	if err != nil || reply != "OK" {
		t.Fatalf("Ping = %q, %v", reply, err)
	}
	if _, ok := p.requests[0]["max_tokens"]; !ok {
		t.Error("first attempt should send max_tokens")
	}
	if p.requests[0]["stream"] != false {
		t.Error("stream must be false")
	}
}

func TestAdaptiveTokenLimit(t *testing.T) {
	p := &provider{rejectMaxTokens: true}
	srv := httptest.NewServer(p.handler())
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1"}
	reply, err := c.Chat(context.Background(), "o-model", []Message{{Role: "user", Content: "hi"}}, 100)
	if err != nil || reply != "OK" {
		t.Fatalf("Chat = %q, %v", reply, err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("expected a retry, got %d requests", len(p.requests))
	}
	if _, ok := p.requests[1]["max_completion_tokens"]; !ok {
		t.Error("retry should use max_completion_tokens")
	}
}

func TestContentShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    *provider
	}{{"parts", &provider{partsContent: true}}, {"think", &provider{think: true}}} {
		srv := httptest.NewServer(tc.p.handler())
		c := &Client{BaseURL: srv.URL + "/v1"}
		reply, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, 0)
		srv.Close()
		if err != nil || reply != "OK" {
			t.Errorf("%s: Chat = %q, %v", tc.name, reply, err)
		}
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	if _, err := c.ListModels(context.Background()); err == nil || !strings.Contains(err.Error(), "API key") || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := parseChat([]byte(`{"choices":[{"finish_reason":"length","message":{"content":""}}]}`)); err == nil || !strings.Contains(err.Error(), "output tokens") {
		t.Errorf("length error not surfaced: %v", err)
	}
	notOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"Hello there!"}}]}`))
	}))
	defer notOK.Close()
	if reply, err := (&Client{BaseURL: notOK.URL}).Ping(context.Background(), "m"); err == nil || reply != "Hello there!" {
		t.Errorf("Ping should fail on a non-OK reply: %q, %v", reply, err)
	}
}
