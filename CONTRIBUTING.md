# Contributing

Thanks for taking a look! This project is intentionally dependency-free and
small — please keep it that way where possible.

## Ground rules

- Go standard library only. New third-party dependencies need a strong reason
  (persistence drivers and Redis are the known exceptions on the roadmap).
- Every behaviour change comes with a test. `make test race` must pass.
- `gofmt` clean, `go vet` clean.
- Keep the public API stable; add new endpoints rather than changing shapes.

## Local workflow

```bash
make vet test race
make build
./bin/astra-moderation   # needs ffmpeg for video
```

Run against any OpenAI-compatible vision endpoint:

```bash
MOD_UPSTREAM_URL=http://127.0.0.1:8000/v1/chat/completions \
MOD_MODEL=Qwen/Qwen3.8-27B \
./bin/astra-moderation
```

## Commit style

`area: short imperative summary` — e.g. `api: add per-tenant policy overrides`.

## Reporting security issues

Please open a private security advisory rather than a public issue.
