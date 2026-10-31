# Benchmark: 15-category vision-LLM moderation

> Sanity benchmark, not a compliance certification. Validate on your own data and tune thresholds.

## Setup

| Item | Value |
|---|---|
| Model | `Qwen/Qwen3.8-27B` (vision-language, mxfp8) |
| Serving | vLLM 0.29.x, tensor-parallel 2 (two GPUs), shared machine with other traffic |
| Decoding | temperature 0, `max_tokens` 700 (retry 1050 on truncated JSON), JSON-schema guided decoding |
| Thinking | disabled (`chat_template_kwargs.enable_thinking=false`) |
| Service | astra-moderation, default policy: `review 0.40 / block 0.80`; `nudity`/`sexual_content` 0.30 / 0.70; `child_safety` 0.10 / 0.30 |
| Concurrency | 4 parallel requests |
| Requests | 80 total, 0 errors |

### Dataset

- **Positives (40 images)**: sampled from two public NSFW-detection datasets on
  Hugging Face — [`x1101/nsfw`](https://huggingface.co/datasets/x1101/nsfw)
  (44 images) and [`x1101/nsfw-full`](https://huggingface.co/datasets/x1101/nsfw-full)
  (126 images). The union (170 files) was shuffled with `random.seed(42)` and the
  first 40 were used.
- **Negatives (40 images)**: real-world photos from `picsum.photos` (a
  Unsplash-derived public service), seeds `mod0`–`mod39`, 640×480.
- **Preprocessing**: every image resized so the longest edge is ≤768 px and
  re-encoded as JPEG (`ffmpeg -vf scale='min(768,iw)':-2 -q:v 4`), matching what
  the service sends to the model.
- **No overlap check, no dedupe beyond hashing filenames.** This is a small,
  convenience sample, not a curated benchmark set.

### Labels — read this before citing the numbers

Labels are **dataset-level, not manually verified**:

- every image from the NSFW datasets is treated as positive
- every `picsum` image is treated as negative

Consequences:

- Dataset positives include some borderline (lingerie/suggestive, not explicit)
  items. The single false negative (`score 0.0`) is plausibly label noise rather
  than a model error — we did not adjudicate it with human review.
- There is no "suggestive but benign" hard-negative class, so the reported
  precision is optimistic for production traffic.
- Reported metrics are **binary NSFW detection**, computed as
  `score = max(nudity, sexual_content, suggestive)`. Precision/recall for the
  other twelve categories is not measured (no labels), only mean scores are
  reported.

## Results

| Metric | Value |
|---|---|
| AUC | 0.988 |
| Precision @ 0.40 | 1.00 |
| Recall @ 0.40 | 0.975 |
| F1 @ 0.40 | 0.99 |
| Confusion (40 pos / 40 neg) | tp 39, fp 0, fn 1, tn 40 |
| Latency p50 | 18.5 s |
| Latency p90 | 19.6 s |
| Requests OK | 80 / 80 |

Threshold sweep (same scores, different cutoffs):

| Threshold | Precision | Recall | F1 |
|---|---|---|---|
| 0.30 | 1.00 | 0.975 | 0.99 |
| 0.40 | 1.00 | 0.975 | 0.99 |
| 0.50 | 1.00 | 0.975 | 0.99 |
| 0.80 | 1.00 | 0.975 | 0.99 |

The scores are strongly separated, so the operating point is not sensitive within
0.3–0.8 **on this dataset**. That is expected for clean-vs-explicit evaluation and
should not be read as "any threshold works" — borderline content compresses the
score distribution.

## Per-category mean score

Means over the same 80 images (not per-category precision/recall — no labels for
those categories):

| Category | Explicit-set mean | Benign-set mean |
|---|---|---|
| nudity | 0.87 | 0.00 |
| sexual_content | 0.91 | 0.00 |
| suggestive | 0.92 | 0.00 |
| child_safety | 0.14 | 0.00 |
| violence | 0.04 | 0.00 |
| graphic_violence | 0.02 | 0.00 |
| gore | 0.01 | 0.00 |
| weapons | 0.02 | 0.00 |
| drugs / alcohol / tobacco / hate_symbols / harassment / self_harm / gambling | 0.00 | 0.00 |

The taxonomy held up: categories that do not apply stayed at zero, no
cross-category bleed was observed.

## What this benchmark does **not** cover

- **Borderline content**: swimwear, lingerie, art nudity, medical imagery,
  stylised/cartoon violence. These are where false positives live.
- **Video quality**: only images were used. Video accuracy depends on the
  upstream sampler (frames or native video) and duration; latency scales with it.
- **Adversarial inputs**: text overlays, watermarks, occlusions, memes.
- **Other models**: a 2–4B VLM will score worse; a 70B+ model will score better
  but cost more. The service is model-agnostic on purpose.
- **Per-category precision/recall** for the twelve categories without labels.

## Reproduce

```bash
# 1. fetch the sample sets (respect each dataset's license/terms)
#    positives: x1101/nsfw + x1101/nsfw-full (Hugging Face)
#    negatives: https://picsum.photos/seed/mod0/640/480 ... mod39

# 2. run against your own service instance
python3 tools/eval.py \
  --base http://localhost:8096 \
  --key amk_... \
  --pos positives/ --neg negatives/ \
  --out results.csv --concurrency 4
```

The script prints AUC, precision/recall/F1 at 0.3/0.4/0.5/0.8 and writes
per-image scores to CSV. It defaults to concurrency 4 so a shared GPU is not
overwhelmed. Latency will differ on your hardware — the absolute numbers above
are from a shared 2-GPU box with concurrent traffic, not a dedicated benchmark
rig.
