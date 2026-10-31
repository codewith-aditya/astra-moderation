package api_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/codewith-aditya/astra-moderation/internal/schema"
)

// zeroScores returns all-zero category scores for a given overrides map.
func scoresWith(overrides map[string]float64) []byte {
	cats := map[string]float64{}
	for _, c := range schema.Categories {
		cats[c] = 0
	}
	for k, v := range overrides {
		cats[k] = v
	}
	verdict := "safe"
	if overrides["nudity"] >= 0.8 {
		verdict = "block"
	}
	content, _ := json.Marshal(map[string]any{
		"categories": cats,
		"verdict":    verdict,
		"reason":     "test fixture",
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
	return raw
}

// bodyHasVideoPart reports whether the OpenAI request body contains a native
// video_url content part.
func bodyHasVideoPart(body map[string]any) bool {
	return bodyContainsType(body, "video_url")
}

func bodyHasImagePart(body map[string]any) bool {
	return bodyContainsType(body, "image_url")
}

func bodyContainsType(body map[string]any, want string) bool {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		content, _ := msg["content"].([]any)
		for _, p := range content {
			part, _ := p.(map[string]any)
			if part["type"] == want {
				return true
			}
		}
	}
	return false
}

// TestNativeVideoJSONPath proves the service forwards a native video_url part
// (not frames) when the client sends a video URL.
func TestNativeVideoJSONPath(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(scoresWith(nil))
	})

	ts, _ := newTestServerWithHandler(t, upstream)
	key := createKey(t, ts, "native-video", 60)

	code, raw := doJSON(t, ts, http.MethodPost, "/v1/moderate", key, map[string]any{
		"video_url": "data:video/mp4;base64,AAAA",
	})
	if code != http.StatusOK {
		t.Fatalf("moderate: %d %s", code, raw)
	}
	var out struct {
		MediaType string `json:"media_type"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.MediaType != "video" {
		t.Fatalf("media_type = %q", out.MediaType)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(bodies))
	}
	if !bodyHasVideoPart(bodies[0]) {
		t.Fatalf("upstream request has no native video_url part: %v", bodies[0])
	}
}

// TestVideoFrameFallback proves that when the upstream rejects native video
// (vLLM with --limit-mm-per-prompt video=0), the service extracts frames with
// ffmpeg and retries with image parts. Requires ffmpeg/ffprobe on PATH.
func TestVideoFrameFallback(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}

	clip := filepath.Join(t.TempDir(), "clip.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=160x120:rate=8",
		"-pix_fmt", "yuv420p", clip)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v (%.200s)", err, out)
	}

	var mu sync.Mutex
	var bodies []map[string]any

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if bodyContainsType(body, "video_url") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"At most 0 video(s) may be provided in one prompt. (parameter=video)"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(scoresWith(map[string]float64{"nudity": 0.95}))
	})

	ts, _ := newTestServerWithHandler(t, upstream)
	key := createKey(t, ts, "frames", 60)

	code, raw := postFile(t, ts, key, clip)
	if code != http.StatusOK {
		t.Fatalf("moderate video: %d %s", code, raw)
	}
	var out struct {
		Verdict   string `json:"verdict"`
		MediaType string `json:"media_type"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Verdict != "block" || out.MediaType != "video" {
		t.Fatalf("out = %+v", out)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 {
		t.Fatalf("upstream calls = %d, want >= 2 (native attempt + frame retry)", len(bodies))
	}
	last := bodies[len(bodies)-1]
	if bodyContainsType(last, "video_url") {
		t.Fatal("retry still used native video part")
	}
	if !bodyHasImagePart(last) {
		t.Fatal("retry did not contain extracted frame images")
	}
}

// postFile uploads a file as multipart/form-data to /v1/moderate.
func postFile(t *testing.T, ts *httptest.Server, key, path string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/moderate", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := new(bytes.Buffer)
	if _, err := out.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out.Bytes()
}
