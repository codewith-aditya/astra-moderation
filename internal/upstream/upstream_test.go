package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/schema"
)

func allScores(v float64) map[string]float64 {
	out := map[string]float64{}
	for _, c := range schema.Categories {
		out[c] = v
	}
	return out
}

func completion(content string) []byte {
	body := map[string]any{
		"model": "fake-model",
		"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 22},
	}
	raw, _ := json.Marshal(body)
	return raw
}

func TestModerateParsesStructuredOutput(t *testing.T) {
	content, _ := json.Marshal(map[string]any{
		"categories": allScores(0.1),
		"verdict":    "safe",
		"reason":     "nothing visible",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(completion(string(content)))
	}))
	defer srv.Close()

	c := New(srv.URL, "fake", "", true, 5*time.Second)
	res, ms, err := c.Moderate(context.Background(), "image", []Part{{"type": "image_url"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "safe" || res.TokensIn != 11 || res.TokensOut != 22 {
		t.Fatalf("result = %+v", res)
	}
	if res.Scores["nudity"] != 0.1 {
		t.Fatalf("nudity score = %v", res.Scores["nudity"])
	}
	if ms < 0 {
		t.Fatalf("negative latency")
	}
}

func TestModerateRetriesOnTruncatedJSON(t *testing.T) {
	var calls int32
	valid, _ := json.Marshal(map[string]any{"categories": allScores(0.4), "verdict": "review", "reason": "hmm"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Write(completion(`{"categories": {"nudity": 0.9`)) // truncated
			return
		}
		w.Write(completion(string(valid)))
	}))
	defer srv.Close()

	c := New(srv.URL, "fake", "", true, 5*time.Second)
	res, _, err := c.Moderate(context.Background(), "image", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "review" {
		t.Fatalf("verdict = %q", res.Verdict)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestModerateReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"At most 0 video(s) may be provided in one prompt."}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "fake", "", true, 5*time.Second)
	_, _, err := c.Moderate(context.Background(), "video", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 400 {
		t.Fatalf("err = %v", err)
	}
	if !IsVideoUnsupported(err) {
		t.Fatal("IsVideoUnsupported should be true")
	}
}

func TestDisableThinkingFlag(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		content, _ := json.Marshal(map[string]any{"categories": allScores(0), "verdict": "safe", "reason": ""})
		w.Write(completion(string(content)))
	}))
	defer srv.Close()

	c := New(srv.URL, "fake", "", true, 5*time.Second)
	if _, _, err := c.Moderate(context.Background(), "image", nil); err != nil {
		t.Fatal(err)
	}
	kwargs, ok := received["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("chat_template_kwargs = %v", received["chat_template_kwargs"])
	}
	if _, ok := received["response_format"]; !ok {
		t.Fatal("response_format missing")
	}
}
