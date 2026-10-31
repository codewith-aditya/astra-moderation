package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/jobs"
	"github.com/codewith-aditya/astra-moderation/internal/media"
	"github.com/codewith-aditya/astra-moderation/internal/policy"
	"github.com/codewith-aditya/astra-moderation/internal/schema"
	"github.com/codewith-aditya/astra-moderation/internal/upstream"
)

// ---------- public endpoints ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"service":        "astra-moderation",
		"version":        Version,
		"model":          s.cfg.UpstreamModel,
		"categories":     len(schema.Categories),
		"uptime_seconds": int(time.Since(s.started).Seconds()),
		"jobs_in_memory": s.jobs.Len(),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.m.Write(w)
}

func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	type cat struct {
		Name    string  `json:"name"`
		Review  float64 `json:"review"`
		Block   float64 `json:"block"`
		Explain string  `json:"definition"`
	}
	out := make([]cat, 0, len(schema.Categories))
	for _, c := range schema.Categories {
		t := s.pol.ThresholdFor(c)
		out = append(out, cat{Name: c, Review: t.Review, Block: t.Block, Explain: schema.Definitions[c]})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"version":     Version,
		"categories":  out,
		"policy_note": "thresholds shown are global defaults; tenants may override",
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"service": "astra-moderation",
		"version": Version,
		"docs":    "https://github.com/codewith-aditya/astra-moderation",
		"endpoints": map[string]string{
			"health":           "GET /health",
			"metrics":          "GET /metrics",
			"categories":       "GET /v1/categories",
			"moderate":         "POST /v1/moderate            (multipart file=@media | JSON image_url/video_url)",
			"batch":            "POST /v1/moderate/batch      (JSON inputs[])",
			"moderations":      "POST /v1/moderations         (OpenAI-compatible, input=url|urls)",
			"jobs_create":      "POST /v1/jobs                (video async + webhook)",
			"jobs_get":         "GET  /v1/jobs/{id}",
			"usage":            "GET  /v1/usage",
			"admin_create_key": "POST /admin/keys             (X-API-Key: <master key>)",
			"admin_list_keys":  "GET  /admin/keys",
			"admin_usage":      "GET  /admin/usage",
		},
		"auth": "tenant: X-API-Key | admin: X-API-Key with master key",
	})
}

// ---------- moderation core ----------

type outcome struct {
	Verdict   string           `json:"verdict"`
	Flags     []policy.Flag    `json:"flagged"`
	Result    *upstream.Result `json:"-"`
	LatencyMS int64            `json:"-"`
}

func (s *Server) runModeration(ctx context.Context, tenantID string, pol policy.Policy, mediaKind string, parts []upstream.Part, tmpPath string) (outcome, error) {
	res, ms, err := s.up.Moderate(ctx, mediaKind, parts)
	if err != nil && mediaKind == "video" && tmpPath != "" && upstream.IsVideoUnsupported(err) {
		frames, ferr := media.FramesDataURLs(tmpPath, s.cfg.FramesMax)
		if ferr == nil {
			fparts := make([]upstream.Part, 0, len(frames))
			for _, f := range frames {
				fparts = append(fparts, upstream.Part{"type": "image_url", "image_url": map[string]any{"url": f}})
			}
			res, ms, err = s.up.Moderate(ctx, "video sampled as frames (in order)", fparts)
		}
	}
	if err != nil {
		s.m.IncError()
		return outcome{}, err
	}
	verdict, flags := policy.Evaluate(pol, res.Scores)
	s.m.ObserveRequest(verdict, time.Duration(ms)*time.Millisecond)
	_ = s.store.RecordEvent(storeEvent(tenantID, mediaKind, verdict, ms, flags, res.Scores))
	return outcome{Verdict: verdict, Flags: flags, Result: res, LatencyMS: ms}, nil
}

func (s *Server) handleModerate(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	started := time.Now()
	ctype := r.Header.Get("Content-Type")

	var parts []upstream.Part
	var kind, tmpPath string

	switch {
	case strings.HasPrefix(ctype, "multipart/form-data"):
		maxBody := (s.cfg.MaxVideoMB + 2) << 20
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "upload too large or malformed: "+err.Error())
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "multipart field 'file' is required")
			return
		}
		defer f.Close()
		kind = media.Kind(hdr.Filename, hdr.Header.Get("Content-Type"))
		switch kind {
		case "image":
			buf, err := io.ReadAll(io.LimitReader(f, (s.cfg.MaxImageMB<<20)+1))
			if err != nil || int64(len(buf)) > s.cfg.MaxImageMB<<20 {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("image exceeds %d MB", s.cfg.MaxImageMB))
				return
			}
			parts = []upstream.Part{{"type": "image_url", "image_url": map[string]any{"url": media.ImageDataURL(buf)}}}
		case "video":
			path, size, err := media.SaveTemp(f, hdr.Filename)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not buffer upload")
				return
			}
			tmpPath = path
			defer os.Remove(tmpPath)
			if size > s.cfg.MaxVideoMB<<20 {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("video exceeds %d MB", s.cfg.MaxVideoMB))
				return
			}
			dur, err := media.ProbeDuration(path)
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, "invalid video (ffprobe failed)")
				return
			}
			if dur > s.cfg.MaxVideoSec {
				writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("video duration %.1fs exceeds %.0fs", dur, s.cfg.MaxVideoSec))
				return
			}
			dataURL, err := media.VideoDataURL(path)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not encode video")
				return
			}
			parts = []upstream.Part{{"type": "video_url", "video_url": map[string]any{"url": dataURL}}}
		default:
			writeError(w, http.StatusUnsupportedMediaType, "unsupported media type")
			return
		}

	case strings.HasPrefix(ctype, "application/json"):
		var body struct {
			ImageURL string `json:"image_url"`
			VideoURL string `json:"video_url"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		switch {
		case body.ImageURL != "":
			kind = "image"
			parts = []upstream.Part{{"type": "image_url", "image_url": map[string]any{"url": body.ImageURL}}}
		case body.VideoURL != "":
			kind = "video"
			parts = []upstream.Part{{"type": "video_url", "video_url": map[string]any{"url": body.VideoURL}}}
		default:
			writeError(w, http.StatusBadRequest, "provide image_url or video_url")
			return
		}

	default:
		writeError(w, http.StatusUnsupportedMediaType, "use multipart/form-data or application/json")
		return
	}

	pol := policy.Merge(s.pol, tenant.Policy)
	out, err := s.runModeration(r.Context(), tenant.ID, pol, kind, parts, tmpPath)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream failure: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"id":         newID("mod"),
		"verdict":    out.Verdict,
		"flagged":    out.Flags,
		"categories": out.Result.Scores,
		"reason":     out.Result.Reason,
		"model":      out.Result.Model,
		"media_type": kind,
		"processing_ms": map[string]int64{
			"upstream": out.LatencyMS,
			"total":    time.Since(started).Milliseconds(),
		},
		"usage": map[string]int{
			"prompt_tokens":     out.Result.TokensIn,
			"completion_tokens": out.Result.TokensOut,
		},
	})
}

// ---------- batch ----------

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	var req struct {
		Inputs []struct {
			ImageURL string `json:"image_url"`
			VideoURL string `json:"video_url"`
		} `json:"inputs"`
		Concurrency int `json:"concurrency"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Inputs) == 0 {
		writeError(w, http.StatusBadRequest, "inputs is required")
		return
	}
	if len(req.Inputs) > 32 {
		writeError(w, http.StatusBadRequest, "at most 32 inputs per batch")
		return
	}
	conc := req.Concurrency
	if conc <= 0 {
		conc = 4
	}
	if conc > 8 {
		conc = 8
	}
	pol := policy.Merge(s.pol, tenant.Policy)
	results := make([]map[string]any, len(req.Inputs))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, in := range req.Inputs {
		i, in := i, in
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			kind := "image"
			parts := []upstream.Part{{"type": "image_url", "image_url": map[string]any{"url": in.ImageURL}}}
			if in.VideoURL != "" {
				kind = "video"
				parts = []upstream.Part{{"type": "video_url", "video_url": map[string]any{"url": in.VideoURL}}}
			}
			if in.ImageURL == "" && in.VideoURL == "" {
				results[i] = map[string]any{"index": i, "error": "image_url or video_url required"}
				return
			}
			out, err := s.runModeration(r.Context(), tenant.ID, pol, kind, parts, "")
			if err != nil {
				results[i] = map[string]any{"index": i, "error": err.Error()}
				return
			}
			results[i] = map[string]any{
				"index":      i,
				"verdict":    out.Verdict,
				"flagged":    out.Flags,
				"categories": out.Result.Scores,
				"reason":     out.Result.Reason,
			}
		}()
	}
	wg.Wait()
	s.writeJSON(w, http.StatusOK, map[string]any{
		"count":   len(results),
		"results": results,
	})
}

// ---------- OpenAI-compatible moderations ----------

func (s *Server) handleOpenAIModerations(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	urls, err := parseModerationInputs(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(urls) == 0 {
		writeError(w, http.StatusBadRequest, "input is required")
		return
	}
	if len(urls) > 16 {
		writeError(w, http.StatusBadRequest, "at most 16 inputs")
		return
	}
	pol := policy.Merge(s.pol, tenant.Policy)
	type modResult struct {
		Flagged        bool               `json:"flagged"`
		Categories     map[string]bool    `json:"categories"`
		CategoryScores map[string]float64 `json:"category_scores"`
	}
	results := make([]modResult, 0, len(urls))
	for _, u := range urls {
		parts := []upstream.Part{{"type": "image_url", "image_url": map[string]any{"url": u}}}
		out, err := s.runModeration(r.Context(), tenant.ID, pol, "image", parts, "")
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream failure: "+err.Error())
			return
		}
		cats := make(map[string]bool, len(schema.Categories))
		for _, c := range schema.Categories {
			cats[c] = out.Result.Scores[c] >= pol.ThresholdFor(c).Block
		}
		results = append(results, modResult{
			Flagged:        out.Verdict == "block",
			Categories:     cats,
			CategoryScores: out.Result.Scores,
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"id":      newID("modr"),
		"model":   s.cfg.UpstreamModel,
		"results": results,
	})
}

func parseModerationInputs(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("input is required")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var objs []struct {
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &objs); err == nil {
		out := make([]string, 0, len(objs))
		for _, o := range objs {
			if o.ImageURL.URL != "" {
				out = append(out, o.ImageURL.URL)
			}
		}
		return out, nil
	}
	return nil, errors.New("input must be a string, array of strings, or [{image_url:{url}}]")
}

// ---------- usage ----------

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	days := queryInt(r, "days", 30, 1, 365)
	sum, err := s.store.Summary(tenant.ID, time.Now().AddDate(0, 0, -days))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "usage lookup failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant.Name, "window_days": days, "usage": sum})
}

// ---------- async jobs ----------

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	ctype := r.Header.Get("Content-Type")
	var videoURL, webhookURL, tmpPath string

	switch {
	case strings.HasPrefix(ctype, "multipart/form-data"):
		maxBody := (s.cfg.MaxVideoMB + 2) << 20
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "upload too large or malformed")
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "multipart field 'file' is required")
			return
		}
		defer f.Close()
		if media.Kind(hdr.Filename, hdr.Header.Get("Content-Type")) != "video" {
			writeError(w, http.StatusBadRequest, "only video uploads are accepted for jobs")
			return
		}
		path, size, err := media.SaveTemp(f, hdr.Filename)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not buffer upload")
			return
		}
		if size > s.cfg.MaxVideoMB<<20 {
			os.Remove(path)
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("video exceeds %d MB", s.cfg.MaxVideoMB))
			return
		}
		if _, err := media.ProbeDuration(path); err != nil {
			os.Remove(path)
			writeError(w, http.StatusUnprocessableEntity, "invalid video (ffprobe failed)")
			return
		}
		tmpPath = path
		webhookURL = r.FormValue("webhook_url")
	case strings.HasPrefix(ctype, "application/json"):
		var body struct {
			VideoURL   string `json:"video_url"`
			WebhookURL string `json:"webhook_url"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		videoURL, webhookURL = body.VideoURL, body.WebhookURL
		if videoURL == "" {
			writeError(w, http.StatusBadRequest, "video_url is required")
			return
		}
	default:
		writeError(w, http.StatusUnsupportedMediaType, "use multipart/form-data or application/json")
		return
	}
	if webhookURL != "" && !(strings.HasPrefix(webhookURL, "http://") || strings.HasPrefix(webhookURL, "https://")) {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
		writeError(w, http.StatusBadRequest, "webhook_url must be http(s)")
		return
	}

	job, err := s.jobs.Create(tenant.ID, "video", webhookURL)
	if err != nil {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
		writeError(w, http.StatusInternalServerError, "could not create job")
		return
	}
	pol := policy.Merge(s.pol, tenant.Policy)
	jobID, jobSecret, jobStatus := job.ID, job.Secret, job.Status
	go s.runJob(job, tenant.ID, pol, videoURL, tmpPath)

	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"id":      jobID,
		"status":  jobStatus,
		"webhook": webhookURL != "",
		"secret":  jobSecret,
		"poll":    "/v1/jobs/" + jobID,
		"note":    "secret is shown once and used to sign webhook deliveries",
	})
}

func (s *Server) runJob(job *jobs.Job, tenantID string, pol policy.Policy, videoURL, tmpPath string) {
	defer func() {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
		if rec := recover(); rec != nil {
			s.jobs.Fail(job.ID, fmt.Sprintf("panic: %v", rec))
			s.m.ObserveJob("failed")
		}
	}()
	s.jobs.SetRunning(job.ID)

	var parts []upstream.Part
	if tmpPath != "" {
		u, err := media.VideoDataURL(tmpPath)
		if err != nil {
			s.jobs.Fail(job.ID, "encode failed: "+err.Error())
			s.m.ObserveJob("failed")
			return
		}
		parts = []upstream.Part{{"type": "video_url", "video_url": map[string]any{"url": u}}}
	} else {
		parts = []upstream.Part{{"type": "video_url", "video_url": map[string]any{"url": videoURL}}}
	}

	out, err := s.runModeration(context.Background(), tenantID, pol, "video", parts, tmpPath)
	if err != nil {
		s.jobs.Fail(job.ID, err.Error())
		s.m.ObserveJob("failed")
		s.deliverWebhook(job)
		return
	}
	s.jobs.Complete(job.ID, out.Verdict, out.Flags, out.Result)
	s.m.ObserveJob("done")
	s.deliverWebhook(job)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	id := r.PathValue("id")
	job, ok := s.jobs.Get(tenant.ID, id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}

type webhookPayload struct {
	ID         string           `json:"id"`
	Status     string           `json:"status"`
	Kind       string           `json:"kind"`
	Verdict    string           `json:"verdict,omitempty"`
	Flags      []policy.Flag    `json:"flagged,omitempty"`
	Result     *upstream.Result `json:"result,omitempty"`
	Error      string           `json:"error,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
}

func (s *Server) deliverWebhook(job *jobs.Job) {
	if job.WebhookURL == "" {
		return
	}
	payload, err := json.Marshal(webhookPayload{
		ID: job.ID, Status: job.Status, Kind: job.Kind, Verdict: job.Verdict,
		Flags: job.Flags, Result: job.Result, Error: job.Error,
		CreatedAt: job.CreatedAt, FinishedAt: job.FinishedAt,
	})
	if err != nil {
		s.log.Error("webhook marshal", "err", err, "job", job.ID)
		return
	}
	mac := hmac.New(sha256.New, []byte(job.Secret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	client := &http.Client{Timeout: s.cfg.WebhookTimeout}
	req, err := http.NewRequest(http.MethodPost, job.WebhookURL, strings.NewReader(string(payload)))
	if err != nil {
		s.log.Error("webhook build", "err", err, "job", job.ID)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Astra-Signature", sig)
	resp, err := client.Do(req)
	if err != nil {
		s.log.Error("webhook delivery", "err", err, "job", job.ID)
		return
	}
	defer resp.Body.Close()
	s.log.Info("webhook delivered", "job", job.ID, "status", resp.StatusCode)
}

// ---------- admin ----------

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		RPM  int    `json:"rpm"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.RPM <= 0 {
		req.RPM = 120
	}
	tenant, plain, err := s.store.CreateTenant(req.Name, req.RPM)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"id":         tenant.ID,
		"name":       tenant.Name,
		"rpm":        tenant.RPM,
		"key":        plain,
		"created_at": tenant.CreatedAt,
		"note":       "store this key now; it is not retrievable later",
	})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		RPM       int       `json:"rpm"`
		Disabled  bool      `json:"disabled"`
		CreatedAt time.Time `json:"created_at"`
		Overrides bool      `json:"has_policy_override"`
	}
	list := s.store.List()
	out := make([]item, 0, len(list))
	for _, t := range list {
		out = append(out, item{ID: t.ID, Name: t.Name, RPM: t.RPM, Disabled: t.Disabled, CreatedAt: t.CreatedAt, Overrides: t.Policy != nil})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "keys": out})
}

func (s *Server) handleToggleKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	disable := strings.HasSuffix(r.URL.Path, "/disable")
	if err := s.store.SetDisabled(id, disable); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.rl.Reset(id)
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "disabled": disable})
}

func (s *Server) handleSetPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Policy *policy.Policy `json:"policy"`
		Clear  bool           `json:"clear"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Clear {
		if err := s.store.SetPolicy(id, nil); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "policy_override": nil})
		return
	}
	if body.Policy == nil {
		writeError(w, http.StatusBadRequest, "provide {\"policy\": {...}} or {\"clear\": true}")
		return
	}
	normalized, err := policy.Normalize(*body.Policy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetPolicy(id, &normalized); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "policy_override": normalized})
}

func (s *Server) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant")
	days := queryInt(r, "days", 30, 1, 365)
	sum, err := s.store.Summary(tenantID, time.Now().AddDate(0, 0, -days))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "usage lookup failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"tenant": tenantID, "window_days": days, "usage": sum})
}

func (s *Server) handleAdminEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant")
	limit := queryInt(r, "limit", 50, 1, 500)
	events, err := s.store.RecentEvents(tenantID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "event lookup failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"count": len(events), "events": events})
}

// ---------- small helpers ----------

func queryInt(r *http.Request, key string, def, min, max int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
