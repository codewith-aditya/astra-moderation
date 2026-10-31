# Astra Moderation

**Self-hosted image & video content moderation API powered by any vision LLM.**  
One Go binary, zero runtime dependencies, OpenAI-compatible endpoint, 15-category taxonomy, per-tenant keys and policies, async video jobs with signed webhooks, Prometheus metrics.

```
Client ──► Go API ──► vLLM / any OpenAI-compatible VLM ──► Policy engine ──► JSON verdict
             │
             ├─ API keys (hashed, tenant-scoped)
             ├─ Rate limiting (per key, RPM)
             ├─ Media validation (type, size, duration via ffprobe)
             ├─ Video: native video_url or ffmpeg frame fallback
             ├─ JSON-schema guided decoding + retry on truncation
             ├─ Policy thresholds → safe / review / block
             └─ Events, usage metering, Prometheus metrics
```

[![ci](https://github.com/codewith-aditya/astra-moderation/actions/workflows/ci.yml/badge.svg)](https://github.com/codewith-aditya/astra-moderation/actions/workflows/ci.yml)
[![Go 1.22+](https://img.shields.io/badge/go-1.22%2B-00ADD8)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

---

## Why another moderation API?

Most moderation stacks are either closed SaaS products (per-image pricing, no data-control story) or hand-rolled wrappers around a single NSFW classifier (no video, no policy engine, no tenancy).

Astra Moderation sits in the middle: **you bring a vision-capable model** (a local vLLM deployment, an Ollama/llama.cpp server, or a hosted OpenAI-compatible API) and this service adds everything a product actually needs — multi-tenancy, thresholds, audit events, async video, webhooks and metrics — as a single static binary.

It was extracted from a production AI-gateway deployment where it moderates user-generated images and videos before they reach downstream consumers.

## Features

| | |
|---|---|
| **15-category taxonomy** | nudity, sexual content, suggestive, violence, graphic violence, weapons, drugs, alcohol, tobacco, hate symbols, harassment, self-harm, gore, child safety, gambling |
| **Images and video** | native `video_url` support; automatic ffmpeg frame-sampling fallback when the upstream disables video |
| **Structured output** | JSON-schema guided decoding; automatic retry with a larger token budget on truncated JSON |
| **OpenAI-compatible** | `POST /v1/moderations` accepts the OpenAI request/response shape — drop-in for existing clients |
| **Tenant management** | hashed API keys, per-key RPM limits, enable/disable, per-tenant policy overrides |
| **Policy engine** | two thresholds per category (review / block), aggressive defaults for `child_safety` |
| **Async jobs + webhooks** | video jobs with HMAC-SHA256-signed webhook delivery |
| **Observability** | Prometheus text metrics, structured request logs, append-only event log |
| **Zero dependencies** | Go standard library only — no cgo, no external packages |
| **Tiny footprint** | one static binary + ffmpeg, runs on a 1 GB VM |

## Quick start

### Docker (recommended)

```bash
git clone https://github.com/codewith-aditya/astra-moderation
cd astra-moderation
cp .env.example .env        # point MOD_UPSTREAM_URL at your VLM
docker compose up -d --build
curl -s localhost:8096/health
```

### From source

```bash
go build -o bin/astra-moderation ./cmd/server
MOD_UPSTREAM_URL=http://127.0.0.1:8000/v1/chat/completions \
MOD_MODEL=Qwen/Qwen3.8-27B \
./bin/astra-moderation
```

You need `ffprobe` and `ffmpeg` on `PATH` for video support (`apt install ffmpeg`).

On first start the server generates an admin key at `data/master.key`. Use it to create a tenant key:

```bash
MASTER=$(cat data/master.key)

curl -s localhost:8096/admin/keys \
  -H "X-API-Key: $MASTER" \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-app","rpm":120}'
# => {"id":"t_...","key":"amk_...","note":"store this key now; it is not retrievable later"}
```

Moderate an image:

```bash
curl -s localhost:8096/v1/moderate \
  -H "X-API-Key: amk_..." \
  -F file=@photo.jpg
```

```json
{
  "verdict": "block",
  "flagged": [
    { "category": "nudity", "score": 0.95, "level": "block" }
  ],
  "categories": { "nudity": 0.95, "sexual_content": 0.88, "...": 0 },
  "reason": "explicit nudity visible",
  "model": "Qwen/Qwen3.8-27B",
  "media_type": "image",
  "processing_ms": { "upstream": 812, "total": 845 },
  "usage": { "prompt_tokens": 406, "completion_tokens": 237 }
}
```

## API

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/health` | – | liveness + model info |
| `GET` | `/metrics` | – | Prometheus exposition |
| `GET` | `/v1/categories` | – | taxonomy with thresholds |
| `POST` | `/v1/moderate` | tenant | multipart `file=` or JSON `{image_url}` / `{video_url}` |
| `POST` | `/v1/moderate/batch` | tenant | up to 32 URLs, `concurrency` 1–8 |
| `POST` | `/v1/moderations` | tenant | OpenAI-compatible `input` (string / array / objects) |
| `POST` | `/v1/jobs` | tenant | async video job, optional `webhook_url` |
| `GET` | `/v1/jobs/{id}` | tenant | poll job status/result |
| `GET` | `/v1/usage` | tenant | usage summary (`?days=30`) |
| `POST` | `/admin/keys` | master | create tenant key `{name, rpm}` |
| `GET` | `/admin/keys` | master | list tenants |
| `POST` | `/admin/keys/{id}/disable` | master | revoke a key |
| `POST` | `/admin/keys/{id}/enable` | master | re-enable |
| `POST` | `/admin/keys/{id}/policy` | master | set per-tenant threshold override |
| `GET` | `/admin/usage` | master | usage for all tenants or `?tenant=` |
| `GET` | `/admin/events` | master | recent moderation events |

### OpenAI-compatible usage

```bash
curl -s localhost:8096/v1/moderations \
  -H "X-API-Key: amk_..." \
  -H 'Content-Type: application/json' \
  -d '{"input": ["https://example.com/img.jpg"]}'
```

```json
{
  "id": "modr_9f2c...",
  "model": "Qwen/Qwen3.8-27B",
  "results": [{
    "flagged": true,
    "categories": { "nudity": true, "violence": false, "...": false },
    "category_scores": { "nudity": 0.95, "violence": 0.02, "...": 0.0 }
  }]
}
```

### Async video jobs

```bash
# submit
curl -s localhost:8096/v1/jobs \
  -H "X-API-Key: amk_..." \
  -F file=@clip.mp4 \
  -F webhook_url=https://example.com/hooks/moderation

# => {"id":"job_...","status":"queued","secret":"<hex>","poll":"/v1/jobs/job_..."}
```

Webhook deliveries are signed:

```http
POST /hooks/moderation
Content-Type: application/json
X-Astra-Signature: sha256=<hex hmac>

{"id":"job_...","status":"done","verdict":"review","flagged":[...],"result":{...}}
```

Verify with `HMAC-SHA256(body, secret)` using the secret returned once at job creation.

## Configuration

All configuration is environment-driven (see [`.env.example`](.env.example)):

| Variable | Default | Description |
|---|---|---|
| `MOD_ADDR` | `:8096` | listen address |
| `MOD_DATA_DIR` | `./data` | tenants, events, `master.key` |
| `MOD_POLICY` | `./policy.json` | threshold policy file |
| `MOD_UPSTREAM_URL` | `http://127.0.0.1:8000/v1/chat/completions` | OpenAI-compatible endpoint |
| `MOD_MODEL` | `Qwen/Qwen3.8-27B` | upstream model name |
| `MOD_UPSTREAM_KEY` | – | bearer token for the upstream |
| `MOD_DISABLE_THINKING` | `true` | sends `chat_template_kwargs.enable_thinking=false` (Qwen-style) |
| `MOD_UPSTREAM_TIMEOUT_SEC` | `180` | per-request timeout |
| `MOD_MAX_IMAGE_MB` | `12` | image upload limit |
| `MOD_MAX_VIDEO_MB` | `30` | video upload limit |
| `MOD_MAX_VIDEO_SEC` | `120` | max video duration |
| `MOD_FRAMES_MAX` | `8` | frames sampled when falling back |
| `MOD_MASTER_KEY` | generated | operator key (else `data/master.key`) |
| `MOD_JOB_TTL_SEC` | `3600` | finished job retention |
| `MOD_WEBHOOK_TIMEOUT_SEC` | `15` | webhook delivery timeout |

### Policy

Thresholds live in [`policy.json`](policy.json). Missing categories fall back to `default`; unknown categories are rejected at startup so typos fail loudly.

```json
{
  "default": { "review": 0.40, "block": 0.80 },
  "categories": {
    "child_safety": { "review": 0.10, "block": 0.30 },
    "nudity":       { "review": 0.30, "block": 0.70 }
  }
}
```

Tenants can override thresholds without server restarts:

```bash
curl -s localhost:8096/admin/keys/t_123/policy \
  -H "X-API-Key: $MASTER" -H 'Content-Type: application/json' \
  -d '{"policy":{"default":{"review":0.3,"block":0.9}}}'
```

## Design notes

**Why a vision LLM instead of a classifier?** A dedicated NSFW classifier is faster and cheaper per image, but it only answers one question. A vision LLM covers all fifteen categories (violence, weapons, hate symbols, self-harm, gambling, …) with a single prompt and can reason about context ("a knife on a kitchen counter" vs "a knife held at a person"). For high-volume image pipelines, put a cheap classifier in front and call this service only for unresolved cases — the API is designed for exactly that tiered setup.

**Why guided JSON?** Free-form model output breaks parsers. Requests set `response_format: json_schema`, so the sampler is constrained to the taxonomy object. If the upstream still truncates (small `max_tokens`), the client retries once with a larger budget before returning an error.

**Why frame fallback?** Some vLLM deployments are started with `--limit-mm-per-prompt '{"video":0}'`. Native `video_url` then fails with an explicit error; the service detects it, samples up to `MOD_FRAMES_MAX` evenly spaced frames with ffmpeg, and re-runs the request as an image list — no config change required.

**Latency expectations.** On a shared 27B vision model the p50 is ~10–20 s per request (guided decoding adds overhead). Hardware and concurrency dominate; see [`docs/BENCHMARK.md`](docs/BENCHMARK.md).

## Benchmark

Sanity benchmark with a 27B vision model (40 explicit images vs 40 benign photos, review threshold 0.40):

| Metric | Value |
|---|---|
| AUC | **0.988** |
| Precision | **1.00** |
| Recall | **0.97** |
| F1 | **0.99** |
| False positives | 0 / 40 |
| Latency p50 | 18.5 s (shared GPU, guided decoding) |

Methodology, caveats and how to reproduce: [`docs/BENCHMARK.md`](docs/BENCHMARK.md). These numbers are a sanity check on clean-vs-explicit data, **not** a production guarantee — validate on your own distribution and tune thresholds.

## Current limitations

- Single-node persistence: JSON files + JSONL event log. Fine for tens of req/s and a few tenants; a Postgres/SQLite driver is on the roadmap for horizontal scaling.
- Jobs are in-memory: a restart drops queued jobs (results already delivered via webhook are unaffected).
- No built-in auth UI; keys are managed via the admin API.
- The model is only as good as your upstream — small VLMs over-flag suggestive content and under-flag stylised violence. Tune thresholds per category.
- Not legal advice or a compliance product. Human review remains necessary for enforcement decisions.

## Roadmap

- [ ] Pluggable persistence (Postgres / SQLite)
- [ ] Redis-backed rate limiting and job queue
- [ ] Second-opinion cascade (cheap classifier → VLM escalation)
- [ ] Audio track moderation (ASR → text moderation)
- [ ] Embeddable JS / Python SDKs
- [ ] OpenAPI-generated clients

## Contributing

Issues and PRs are welcome. Please run `make vet test race` before opening a PR; CI runs `gofmt`, `go vet` and `go test -race`.

## License

[MIT](LICENSE) © 2026 Aditya Wakharkar
