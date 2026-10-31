# Benchmark: 15-category vision-LLM moderation

> Sanity benchmark, not a compliance certification. Validate on your own data and tune thresholds.

## Setup

- **Upstream**: Qwen3.8-27B served by vLLM (TP2, mxfp8), `enable_thinking=false`, JSON-schema guided decoding
- **Service**: astra-moderation with the default policy (`review 0.40`, `block 0.80`; `nudity`/`sexual_content` review 0.30 / block 0.70; `child_safety` 0.10 / 0.30)
- **Dataset**: 40 explicit images sampled from public NSFW detection datasets on Hugging Face, 40 benign real-world photos (random landscape/street/portrait images), resized to max 768 px JPEG
- **Metric definition**: binary NSFW detection where score = `max(nudity, sexual_content, suggestive)`
- **Hardware note**: the model ran on a shared 2×GPU box with other traffic; latency numbers reflect that.

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

Threshold sweep:

| Threshold | Precision | Recall | F1 |
|---|---|---|---|
| 0.30 | 1.00 | 0.975 | 0.99 |
| 0.40 | 1.00 | 0.975 | 0.99 |
| 0.50 | 1.00 | 0.975 | 0.99 |
| 0.80 | 1.00 | 0.975 | 0.99 |

The scores are strongly separated, so the operating point is not sensitive within
0.3–0.8 on this dataset. That is expected for clean-vs-explicit evaluation and
should **not** be read as "any threshold works" — borderline content compresses
the score distribution.

## Per-category mean score

| Category | Explicit images | Benign images |
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
  stylised/cartoon violence. These are where false positives live. Build a
  labelled set from your own traffic and tune per-category thresholds.
- **Video**: only images were used. Video quality depends on the upstream
  sampler (frames or native video); latency scales with duration.
- **Adversarial inputs**: text overlays, watermarks, occlusions, memes.
- **Other models**: a 2–4B VLM will score worse; a 70B+ model will score better
  but cost more. The service is model-agnostic on purpose.

## Reproduce

```bash
# positives/ and negatives/ contain jpg/png files
python3 tools/eval.py \
  --base http://localhost:8096 \
  --key amk_... \
  --pos positives/ --neg negatives/ \
  --out results.csv
```

The script prints AUC, precision/recall/F1 at four thresholds, per-category
means, and writes per-image scores to CSV. It throttles to `--concurrency 4` by
default so a shared GPU is not overwhelmed.
