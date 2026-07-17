# llm-gateway

An OpenAI-compatible **LLM gateway** in Go. It sits in front of one or more model providers and, for
every request, **routes to the cheapest model that can handle it**, serves a **semantic cache** on
repeated/similar prompts, **rate-limits per API key**, and reports **cost, latency, and cache-hit
metrics**. The goal: cut LLM spend at the same output quality — and prove it with numbers.

## Why

LLM API cost is dominated by sending every prompt to the strongest (most expensive) model. Most
prompts don't need it. A routing gateway + semantic cache can cut cost substantially while staying
drop-in (OpenAI-compatible), so existing clients need no changes.

## Architecture

```
client ──/v1/chat/completions──▶  gateway
                                    │  1. auth + per-key rate limit
                                    │  2. semantic cache lookup (embed prompt → vector similarity)
                                    │  3. router: difficulty classifier → cheap vs strong model (+ fallback)
                                    │  4. proxy to chosen provider; stream back
                                    │  5. cache response; record cost/latency/model metrics
                                    ▼
                        providers (OpenAI / Anthropic / local / …)
```

## Milestones

- **M0 — passthrough proxy.** OpenAI-compatible `POST /v1/chat/completions` proxied to one upstream; streaming works.
- **M1 — multi-provider + config.** Register models with cost tiers/capabilities in config; select by name.
- **M2 — router.** Difficulty classifier (start heuristic: length/keywords/JSON-mode; then embedding-based)
  → route easy prompts to a cheap model, hard ones to a strong model; **fallback** on error/timeout.
- **M3 — semantic cache.** Embed the prompt, vector-similarity lookup (Redis / pgvector); return cached
  completion on a hit above a threshold; TTL + invalidation.
- **M4 — per-key rate limiting + auth.** API keys with quotas; reuse the sliding-window limiter from
  [`distributed-rate-limiter`](https://github.com/Garv2003/distributed-rate-limiter).
- **M5 — metrics + proof.** Prometheus (`cost_saved`, latency histogram, cache-hit rate, model mix) +
  Grafana; a benchmark that reports **% cost cut at equal quality** on a sample workload.

## Tech stack

Go · Redis (cache + rate limit) · an embeddings API (for cache + classifier) · Prometheus/Grafana ·
Docker Compose.

## Getting started

```bash
go run ./cmd/gateway     # M0: proxy on :8080 → set UPSTREAM_URL + provider key
```

## Learning goals

- Cost/quality routing and how to measure it honestly (no quality regression).
- Semantic caching (embeddings + vector similarity) and its hit/quality trade-offs.
- Building a streaming, provider-agnostic proxy; composing rate-limiting + observability into infra.
