#!/usr/bin/env bash
# End-to-end demo. Requires jq.
set -euo pipefail

BASE="${BASE:-http://localhost:8096}"
MASTER="${MASTER:-$(cat data/master.key 2>/dev/null || true)}"
if [ -z "$MASTER" ]; then
  echo "start the server once to generate data/master.key, or export MASTER="
  exit 1
fi

echo "== health =="
curl -s "$BASE/health" | jq .

echo "== create tenant key =="
RESP=$(curl -s "$BASE/admin/keys" -H "X-API-Key: $MASTER" -H 'Content-Type: application/json' \
  -d '{"name":"demo","rpm":120}')
echo "$RESP" | jq .
KEY=$(echo "$RESP" | jq -r .key)

echo "== moderate an image URL =="
curl -s "$BASE/v1/moderate" -H "X-API-Key: $KEY" -H 'Content-Type: application/json' \
  -d '{"image_url":"https://picsum.photos/seed/mod-demo/640/480"}' | jq '{verdict, flagged, processing_ms}'

echo "== openai-compatible moderations =="
curl -s "$BASE/v1/moderations" -H "X-API-Key: $KEY" -H 'Content-Type: application/json' \
  -d '{"input":["https://picsum.photos/seed/mod-demo/640/480"]}' | jq '.results[0] | {flagged, nudity: .category_scores.nudity}'

echo "== upload a local image (optional) =="
if [ -n "${IMAGE:-}" ]; then
  curl -s "$BASE/v1/moderate" -H "X-API-Key: $KEY" -F "file=@$IMAGE" | jq '{verdict, flagged}'
fi

echo "== async video job (optional) =="
if [ -n "${VIDEO:-}" ]; then
  JOB=$(curl -s "$BASE/v1/jobs" -H "X-API-Key: $KEY" -F "file=@$VIDEO")
  echo "$JOB" | jq .
  ID=$(echo "$JOB" | jq -r .id)
  sleep 3
  curl -s "$BASE/v1/jobs/$ID" -H "X-API-Key: $KEY" | jq '{status, verdict, flagged}'
fi

echo "== usage =="
curl -s "$BASE/v1/usage?days=1" -H "X-API-Key: $KEY" | jq .usage
