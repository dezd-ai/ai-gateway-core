# AI-Gateway-Obs Dashboard

**English** | [中文](README.md)

The **read-only observability tool** for AI-Gateway-Core (the `obs/` subdirectory of this repository): a local web page for inspecting the gateway's audit log, call chains and cost.

- **Not an admin panel**: it never edits config or controls the gateway — a pure read-only observer (the gateway itself has no web admin UI; configuration lives only in `config.yaml`).
- **Zero changes to ai-gateway-core**: it only reads `logs/uba.json` and `config.yaml`.
- **Zero external dependencies**: pure Go standard library, single-file HTML frontend (no CDN).
- **Pure observer**: does not write to the gateway, does not write logs, does not control anything.

---

## Features

| Module | Description |
|---|---|
| Summary cards | total requests / total tokens / total cost / cache hit rate / average latency |
| Cost chart | cost bar chart grouped by user (or model/upstream) |
| Audit table | per-request time/user/model/upstream/tokens/cost/billing precision/security/**status**/latency, filterable by user/model/upstream |
| **Blocked badge** | requests blocked by the gateway (401/403/429/413) show a red "blocked" badge with the reason code |
| **Failover badge** | requests that failed over show a purple `↪ upstreamA→upstreamB` badge (details in the call-chain view) |
| **Agent badge** | requests from `agent_mode: true` keys show a blue `agent` badge in the user column (cost attribution) |
| **Call chain** | click a row or the "chain" button to open a vertical timeline of plugin execution: what each step did and what data it produced |
| Auto refresh | re-reads the log every 3 seconds |

> Audit fields consumed are documented in [../docs/audit-log.md](../docs/audit-log.md) (including `agent_mode` / `fallback_chain` / `http_status` / `abort_reason`).

---

## Quick start

```bash
cd obs
go build -o bin/ai-gateway-obs .   # or: go run .
./bin/ai-gateway-obs
# open http://localhost:9090
```

**See it working without running a gateway**: the repository ships sample audit data at `examples/uba.json` — point the dashboard at it to see a fully populated view (normal / blocked / failover / agent records):

```bash
./bin/ai-gateway-obs -log ../examples/uba.json
```

Default inputs (relative paths; run from the `obs/` directory):

- audit log: `../logs/uba.json`
- gateway config: `../config.yaml` (reads each upstream's `cost_per_1k` to compute cost); **if that file is missing it falls back to `../config.example.yaml`**, so a fresh clone still shows the cost column.

Override via flags:

```bash
./bin/ai-gateway-obs -listen :9090 -log /path/to/uba.json -config /path/to/config.yaml
```

---

## Layout

```
ai-gateway-obs/
├── go.mod            # module github.com/dezd-ai/ai-gateway-core/obs (separate module)
├── main.go           # HTTP server + /api/* endpoints + go:embed frontend
├── store.go          # uba.json read/filter/aggregate (RWMutex + 2s refresh)
├── cost.go           # extract per-upstream cost_per_1k from config.yaml
└── web/index.html    # single-file dashboard (vanilla HTML/CSS/JS)
```

---

## API

| Endpoint | Description |
|---|---|
| `GET /` | dashboard page |
| `GET /api/records?limit=&username=&model=&upstream=` | audit records (newest first, with `cost` and `rate_known`) |
| `GET /api/stats` | summary + grouping by user/model/upstream |
| `GET /api/trace?id=<trace_id>` | single call-chain detail |

---

## Cost model

`cost = total_tokens / 1000 × cost_per_1k`, with rates read from `upstreams[].cost_per_1k` in `config.yaml`. Upstreams without a configured rate report `rate_known=false` and render as `-`.

---

## Known limitations

1. **Upstream failure vs. success is still hard to distinguish in "normal" records**: blocked/rejected records carry `http_status`/`success`/`abort_reason`, but successful proxied requests (which may carry an upstream 5xx) do not record the upstream status yet — a full fix needs an upstream-status field from the gateway's auditor.
2. ~~**403-blocked requests were absent from this log**~~ ✅ Fixed: requests blocked at PreRequest (401/403/502) are now persisted to `uba.json` (with `http_status`/`abort_reason`) and rendered with a red "blocked" badge.
3. **Mock/failed historical records may be mixed in**: development-time mock runs and failures stay in the log; clear `ai-gateway-core/logs/uba.json` if you only want real billing data.
