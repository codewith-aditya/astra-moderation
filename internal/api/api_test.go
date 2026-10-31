package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/api"
	"github.com/codewith-aditya/astra-moderation/internal/config"
	"github.com/codewith-aditya/astra-moderation/internal/policy"
	"github.com/codewith-aditya/astra-moderation/internal/schema"
	"github.com/codewith-aditya/astra-moderation/internal/store"
	"github.com/codewith-aditya/astra-moderation/internal/upstream"
)

const masterKey = "amk_master_test"

func fakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	scores := map[string]float64{}
	for _, c := range schema.Categories {
		scores[c] = 0
	}
	scores["nudity"] = 0.95
	content, _ := json.Marshal(map[string]any{
		"categories": scores,
		"verdict":    "block",
		"reason":     "explicit nudity visible",
	})
	body := map[string]any{
		"model": "fake-vl",
		"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]any{"content": string(content)},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 20},
	}
	raw, _ := json.Marshal(body)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}))
}

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	fake := fakeUpstream(t)
	t.Cleanup(fake.Close)

	dir := t.TempDir()
	cfg := config.Config{
		Addr:            ":0",
		DataDir:         dir,
		UpstreamURL:     fake.URL,
		UpstreamModel:   "fake-vl",
		DisableThinking: true,
		UpstreamTimeout: 5 * time.Second,
		MaxImageMB:      12,
		MaxVideoMB:      30,
		MaxVideoSec:     120,
		FramesMax:       8,
		MasterKey:       masterKey,
		JobTTL:          time.Hour,
		WebhookTimeout:  2 * time.Second,
	}
	st, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	up := upstream.New(cfg.UpstreamURL, cfg.UpstreamModel, "", true, 5*time.Second)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := api.New(cfg, policy.Default(), st, up, logger)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func doJSON(t *testing.T, ts *httptest.Server, method, path, key string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func createKey(t *testing.T, ts *httptest.Server, name string, rpm int) string {
	t.Helper()
	code, raw := doJSON(t, ts, http.MethodPost, "/admin/keys", masterKey, map[string]any{"name": name, "rpm": rpm})
	if code != http.StatusCreated {
		t.Fatalf("create key: %d %s", code, raw)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Key == "" {
		t.Fatalf("create key response: %s", raw)
	}
	return out.Key
}

func TestHealthAndCategories(t *testing.T) {
	ts, _ := newTestServer(t)
	code, raw := doJSON(t, ts, http.MethodGet, "/health", "", nil)
	if code != 200 || !bytes.Contains(raw, []byte(`"ok": true`)) {
		t.Fatalf("health: %d %s", code, raw)
	}
	code, raw = doJSON(t, ts, http.MethodGet, "/v1/categories", "", nil)
	if code != 200 || !bytes.Contains(raw, []byte("child_safety")) {
		t.Fatalf("categories: %d %s", code, raw)
	}
}

func TestUnauthorized(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := doJSON(t, ts, http.MethodPost, "/v1/moderate", "", map[string]any{"image_url": "https://x/y.jpg"})
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d", code)
	}
	code, _ = doJSON(t, ts, http.MethodPost, "/v1/moderate", "amk_bogus", map[string]any{"image_url": "https://x/y.jpg"})
	if code != http.StatusUnauthorized {
		t.Fatalf("bogus key code = %d", code)
	}
}

func TestModerateFlow(t *testing.T) {
	ts, _ := newTestServer(t)
	key := createKey(t, ts, "flow", 60)

	code, raw := doJSON(t, ts, http.MethodPost, "/v1/moderate", key, map[string]any{
		"image_url": "data:image/png;base64,iVBORw0KGgo=",
	})
	if code != http.StatusOK {
		t.Fatalf("moderate: %d %s", code, raw)
	}
	var out struct {
		Verdict string `json:"verdict"`
		Flagged []struct {
			Category string  `json:"category"`
			Score    float64 `json:"score"`
			Level    string  `json:"level"`
		} `json:"flagged"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Verdict != "block" {
		t.Fatalf("verdict = %q", out.Verdict)
	}
	if len(out.Flagged) == 0 || out.Flagged[0].Category != "nudity" || out.Flagged[0].Level != "block" {
		t.Fatalf("flagged = %+v", out.Flagged)
	}
}

func TestOpenAIModerationsShape(t *testing.T) {
	ts, _ := newTestServer(t)
	key := createKey(t, ts, "openai", 60)

	code, raw := doJSON(t, ts, http.MethodPost, "/v1/moderations", key, map[string]any{
		"input": []string{"https://example.com/a.jpg", "https://example.com/b.jpg"},
	})
	if code != http.StatusOK {
		t.Fatalf("moderations: %d %s", code, raw)
	}
	var out struct {
		Results []struct {
			Flagged        bool               `json:"flagged"`
			Categories     map[string]bool    `json:"categories"`
			CategoryScores map[string]float64 `json:"category_scores"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results = %d", len(out.Results))
	}
	if !out.Results[0].Flagged || !out.Results[0].Categories["nudity"] {
		t.Fatalf("first result = %+v", out.Results[0])
	}
	if out.Results[0].CategoryScores["nudity"] != 0.95 {
		t.Fatalf("score = %v", out.Results[0].CategoryScores["nudity"])
	}
}

func TestRateLimit(t *testing.T) {
	ts, _ := newTestServer(t)
	key := createKey(t, ts, "ratelimited", 1)

	code, _ := doJSON(t, ts, http.MethodPost, "/v1/moderate", key, map[string]any{"image_url": "x"})
	if code != http.StatusOK {
		t.Fatalf("first code = %d", code)
	}
	code, raw := doJSON(t, ts, http.MethodPost, "/v1/moderate", key, map[string]any{"image_url": "x"})
	if code != http.StatusTooManyRequests {
		t.Fatalf("second code = %d %s", code, raw)
	}
}

func TestAsyncJob(t *testing.T) {
	ts, _ := newTestServer(t)
	key := createKey(t, ts, "jobs", 60)

	code, raw := doJSON(t, ts, http.MethodPost, "/v1/jobs", key, map[string]any{
		"video_url": "data:video/mp4;base64,AAAA",
	})
	if code != http.StatusAccepted {
		t.Fatalf("create job: %d %s", code, raw)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" {
		t.Fatalf("job response: %s", raw)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		code, raw = doJSON(t, ts, http.MethodGet, "/v1/jobs/"+created.ID, key, nil)
		if code != http.StatusOK {
			t.Fatalf("get job: %d %s", code, raw)
		}
		var job struct {
			Status  string `json:"status"`
			Verdict string `json:"verdict"`
		}
		json.Unmarshal(raw, &job)
		if job.Status == "done" {
			if job.Verdict != "block" {
				t.Fatalf("job verdict = %q", job.Verdict)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %s", raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWebhookDelivery(t *testing.T) {
	ts, _ := newTestServer(t)
	key := createKey(t, ts, "webhook", 60)

	sigCh := make(chan string, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sigCh <- r.Header.Get("X-Astra-Signature"):
		default:
		}
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer hook.Close()

	code, raw := doJSON(t, ts, http.MethodPost, "/v1/jobs", key, map[string]any{
		"video_url":   "data:video/mp4;base64,AAAA",
		"webhook_url": hook.URL,
	})
	if code != http.StatusAccepted {
		t.Fatalf("create job: %d %s", code, raw)
	}
	select {
	case sig := <-sigCh:
		if !strings.HasPrefix(sig, "sha256=") {
			t.Fatalf("signature = %q", sig)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("webhook not delivered")
	}
}

func TestUsageTracksRequests(t *testing.T) {
	ts, _ := newTestServer(t)
	key := createKey(t, ts, "usage", 60)
	for i := 0; i < 3; i++ {
		doJSON(t, ts, http.MethodPost, "/v1/moderate", key, map[string]any{"image_url": "x"})
	}
	code, raw := doJSON(t, ts, http.MethodGet, "/v1/usage?days=7", key, nil)
	if code != 200 {
		t.Fatalf("usage: %d %s", code, raw)
	}
	if !bytes.Contains(raw, []byte(`"total": 3`)) {
		t.Fatalf("usage body: %s", raw)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	key := createKey(t, ts, "metrics", 60)
	doJSON(t, ts, http.MethodPost, "/v1/moderate", key, map[string]any{"image_url": "x"})
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"astra_moderation_requests_total",
		`astra_moderation_requests_total{verdict="block"} 1`,
		"astra_moderation_request_duration_seconds_bucket",
	} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("metrics missing %q\n%s", want, raw)
		}
	}
}

func TestAdminPolicyOverride(t *testing.T) {
	ts, _ := newTestServer(t)
	key := createKey(t, ts, "policy", 60)

	// Tighten nudity block threshold to 0.99: one image scoring 0.95 now only reviews.
	code, raw := doJSON(t, ts, http.MethodGet, "/admin/keys", masterKey, nil)
	if code != 200 {
		t.Fatalf("list keys: %d %s", code, raw)
	}
	var list struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	json.Unmarshal(raw, &list)
	if len(list.Keys) != 1 {
		t.Fatalf("keys = %d", len(list.Keys))
	}
	id := list.Keys[0].ID

	code, raw = doJSON(t, ts, http.MethodPost, fmt.Sprintf("/admin/keys/%s/policy", id), masterKey, map[string]any{
		"policy": map[string]any{
			"default":    map[string]any{"review": 0.4, "block": 0.8},
			"categories": map[string]any{"nudity": map[string]any{"review": 0.5, "block": 0.99}},
		},
	})
	if code != 200 {
		t.Fatalf("set policy: %d %s", code, raw)
	}

	code, raw = doJSON(t, ts, http.MethodPost, "/v1/moderate", key, map[string]any{"image_url": "x"})
	if code != 200 {
		t.Fatalf("moderate: %d %s", code, raw)
	}
	var out struct {
		Verdict string `json:"verdict"`
	}
	json.Unmarshal(raw, &out)
	if out.Verdict != "review" {
		t.Fatalf("verdict with override = %q, want review", out.Verdict)
	}
}
