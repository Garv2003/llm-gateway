# Cost benchmark (M5)

Quantifies how much LLM spend the gateway's **router** saves on a sample workload —
**offline, with no live API keys**.

## What it measures

For every prompt in a workload it runs the exact heuristic classifier the live gateway
uses (`router.New(reg, nil)` → `HeuristicClassifier`) and prices two scenarios against
the registry's per-token costs (`models.json`):

| Scenario   | Model served per prompt                              |
|------------|------------------------------------------------------|
| `baseline` | the **premium** model, every time (i.e. no gateway)  |
| `routed`   | the model the **router picks** for that prompt       |

Cost per prompt = `inputTokens/1000 × inputCostPer1K + outputTokens/1000 × outputCostPer1K`.

- **Input tokens** are estimated with a `chars / 4` heuristic.
- **Output tokens** are a fixed assumption (`-output-tokens`, default 500) — the same for
  both scenarios, so it cancels out of the routing comparison and only scales absolute cost.
- The optional `-cache-hit-rate` models the extra savings a warm semantic cache adds, by
  removing that fraction of routed cost (a cache hit avoids the upstream call entirely).

### This is an ESTIMATE, not a measurement

The savings figure comes from **registry pricing × token heuristics** applied to a sample
workload. **No live LLM API is called**, so it does not — and cannot — verify that the
cheaper models produce equal-quality answers. Treat it as an upper-bound planning estimate.
Real end-to-end validation (cost *and* quality) requires live API keys and a scored eval set;
see "Live mode" below.

## Run

```bash
go run ./bench/cost                       # default workload, offline
go run ./bench/cost -output-tokens 800    # assume longer completions
go run ./bench/cost -cache-hit-rate 0.3   # add a 30% cache-hit estimate
go run ./bench/cost -workload path.json   # your own workload
go run ./bench/cost -baseline gpt-4o      # override the baseline model
```

Flags: `-models` (default `models.json`), `-workload` (default `bench/cost/workload.json`),
`-output-tokens` (default 500), `-baseline` (default: most expensive premium model),
`-cache-hit-rate` (default 0).

## Plugging in a real workload

`-workload` accepts either a JSON array or JSONL (one object per line). Each item:

```json
{ "name": "optional-label", "prompt": "the user prompt text", "messages": 1, "json": false }
```

- `prompt` — required; the text the classifier scores.
- `messages` — optional conversation turn count (defaults to 1); more turns nudges difficulty up.
- `json` — optional; set true for structured-output requests (nudges difficulty up).

Export prompts from your own logs into this shape to get an estimate tuned to your traffic.

## Live mode (future)

Not yet implemented. A live mode would send each prompt to both the baseline and routed
models, record real token usage and latency, and score outputs with an eval/judge to confirm
**equal quality**. That needs provider API keys (`OPENAI_API_KEY`, etc.) and will incur real cost.

## Results (offline estimate)

Measured by running `go run ./bench/cost` on the bundled 19-prompt workload
(`output-tokens=500`, default `models.json`):

```
prompts priced:     19
baseline model:     gpt-4o (tier=premium)

routing distribution
MODEL            TIER        PROMPTS    SHARE    ROUTED COST
llama3.1-8b      cheap            16    84.2%   $    0.00000
gpt-4.1-mini     standard          3    15.8%   $    0.00252

baseline (all premium):      $0.14555
routed:                      $0.00252
saved by routing:            $0.14304

ESTIMATED COST REDUCTION: 98.3% (routing only)
```

> Note: the bundled workload is trivial-prompt-heavy and `models.json` includes a **free local
> model** (`llama3.1-8b`, $0/1K), which the router picks for the 16 cheap prompts — so this
> particular number is near the ceiling. Swap in a cloud-only registry or a harder workload for
> a more conservative estimate. Adding a 30% cache-hit rate raises it to ~98.8%.

_Fill in your own numbers here after running against your workload._
