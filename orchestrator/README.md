# Glaucoma Orchestrator

A Go control-plane service that fronts the Glaucoma Detection cluster. It owns
the user-tunable configuration of the system (routing threshold, HPA min/max
replicas per pool, upload safeguards, demo limits) and exposes a single REST
API for running the demo.

The orchestrator does **not** replace the load balancer — it sits in front of
it. Predict requests from clients are validated here, then forwarded to the
existing Go LB which makes the CPU/GPU routing decision.

```
client ──HTTP──▶ orchestrator (:9000)
                     │
                     ├─ validates upload (size, type, dimensions)
                     ├─ forwards /predict to load-balancer
                     └─ patches HPA / LB env via kubectl on config change

                  load-balancer (:8080) ──▶ ai-worker-cpu / ai-worker-gpu
```

## Run

### Locally (no kubectl needed)

```bash
cd orchestrator
go build -o orchestrator .
./orchestrator -listen :9000 -lb-url http://localhost:8000 -no-kube
```

### In the cluster

The provided `k8s/orchestrator.yaml` deploys it with a ServiceAccount that has
`patch` rights on HPAs and Deployments in the `glaucoma` namespace.

```bash
./k8s/deploy.sh           # builds images + applies all manifests
kubectl -n glaucoma get svc orchestrator   # NodePort 30900
minikube service orchestrator -n glaucoma --url
```

## Configuration

Every tunable lives in one JSON config object, persisted to disk
(`/var/lib/orchestrator/config.json` in the pod, configurable via
`ORCH_CONFIG`). Defaults are applied for any missing field, and every value
is validated against hard bounds.

| Field | Default | Bounds | Effect |
|---|---|---|---|
| `size_threshold_bytes` | 2 500 000 | [1024, 1 GiB] | LB routes >= this size to GPU pool |
| `cpu_min_replicas` / `cpu_max_replicas` | 1 / 5 | [0, 50] | HPA `ai-worker-cpu` |
| `cpu_target_utilization` | 70 | [1, 100] % | HPA `ai-worker-cpu` |
| `gpu_min_replicas` / `gpu_max_replicas` | 1 / 3 | [0, 50] | HPA `ai-worker-gpu` |
| `gpu_target_utilization` | 75 | [1, 100] % | HPA `ai-worker-gpu` |
| `max_upload_bytes` | 16 MiB | [4 KiB, 64 MiB] | Hard cap on `/api/demo/predict` upload |
| `max_image_pixels` | 16 000 000 | [4 096, 64 000 000] | Decompression-bomb guard |
| `allowed_content_types` | `image/jpeg,image/png` | must start with `image/` | Allowlist for uploads |
| `max_loadtest_requests` | 200 | [1, 5000] | Cap on `n` for `/api/demo/loadtest` |
| `max_loadtest_concurrency` | 16 | [1, 256] | Cap on `concurrency` for `/api/demo/loadtest` |
| `upstream_timeout_seconds` | 60 | [1, 600] | Per-request timeout to LB |

Validation is run on the **merged** result, so partial PUT bodies are safe.
A field set to `null` is a no-op.

## API

All endpoints are under the listen address (default `:9000`). Responses are
JSON.

### `GET /healthz`

Liveness/readiness check. Returns `{"status":"ok"}`.

### `GET /api/config`

Returns the current effective config.

```bash
curl -s http://localhost:9000/api/config | jq
```

### `PUT /api/config`

Partial update — fields you don't include keep their current value. The
server validates the merged config and rejects the whole patch on any
invariant violation. On success, an in-process `OnChange` hook reconciles
the cluster (kubectl patches the HPAs and `kubectl set env` updates the
LB). Reconcile errors are logged but the API still returns the new config.

```bash
curl -X PUT http://localhost:9000/api/config \
  -H 'Content-Type: application/json' \
  -d '{
    "size_threshold_bytes": 1500000,
    "cpu_max_replicas": 8,
    "gpu_max_replicas": 4,
    "cpu_target_utilization": 60
  }'
```

Unknown fields produce a 422 so typos surface.

### `POST /api/config/apply`

Force-reconcile the cluster against the current config. Useful right after
deploy or when kubectl was unavailable on the previous PUT. Returns 207
(Multi-Status) with a per-step error list if any patch failed.

### `POST /api/config/reset`

Reset the config to defaults and reconcile.

### `GET /api/status`

Aggregated cluster snapshot: pods (name/app/phase/ready), HPAs (min/max,
current replicas, target & current CPU utilization), and the current config.

```bash
curl -s http://localhost:9000/api/status | jq '.cluster'
```

### `POST /api/demo/predict`

Single-prediction demo. Multipart form: `file` (image) + `model_name`. The
orchestrator runs the full safeguard chain before forwarding to the LB:

1. **Size cap** (`max_upload_bytes`) — enforced via a `limit+1` read so we
   never buffer an oversized request.
2. **Declared content-type allowlist** (`allowed_content_types`).
3. **Magic-byte sniff** — `http.DetectContentType` must agree that the
   payload is an image.
4. **Header-only decode** with `image.DecodeConfig` to enforce
   `max_image_pixels`. This is what stops decompression bombs: a 1 KiB PNG
   that decodes to 100 000 × 100 000 is rejected without ever materializing
   the pixel buffer.
5. **`model_name` sanitisation** — only `[A-Za-z0-9._-]` allowed, no
   path-traversal segments.

Errors map to the correct HTTP status code (`413`, `415`, `400`).

Response wraps the upstream JSON with orchestrator metadata so you can see
exactly what was validated and which pool the LB should have routed to:

```json
{
  "orchestrator": {
    "validated": {"content_type": "image/jpeg", "filename": "eye.jpg",
                  "width": 224, "height": 224, "bytes": 18432},
    "routed_to_pool": "cpu"
  },
  "upstream": { ... worker JSON ... }
}
```

```bash
curl -X POST http://localhost:9000/api/demo/predict \
  -F file=@models/sample.jpg \
  -F model_name=glaucoma_v1.h5
```

### `POST /api/demo/loadtest`

Synthetic batch demo — fires `n` predict requests at the LB with the
configured `concurrency` and reports throughput, latency percentiles, the
distribution across worker pods, and HTTP status breakdown.

If `image_base64` is omitted, the orchestrator generates a tiny synthetic
JPEG so the demo has zero prerequisites.

```bash
curl -X POST http://localhost:9000/api/demo/loadtest \
  -H 'Content-Type: application/json' \
  -d '{"n": 100, "concurrency": 8, "model_name": "glaucoma_v1.h5"}'
```

Response:

```json
{
  "started_at": "...", "finished_at": "...",
  "duration_seconds": 12.4,
  "requests": 100, "concurrency": 8,
  "successes": 100, "failures": 0,
  "throughput_rps": 8.06,
  "latency_ms": {"min": 95, "avg": 980, "p50": 920, "p95": 1450, "p99": 1700, "max": 1810},
  "node_distribution": {"Standard CPU": 100},
  "worker_spread": {"ai-worker-cpu-7c9-abc": 45, "ai-worker-cpu-7c9-def": 55},
  "status_codes": {"200": 100}
}
```

`worker_spread` is the most useful demo signal — it shows the HPA actually
spreading load across pods after a scale-up.

## Edge cases handled

- **Empty upload** → 415 (`upload is empty`).
- **Upload over `max_upload_bytes`** → 413 (`upload exceeds max_upload_bytes`),
  detected without buffering the entire body.
- **Wrong file type** → 415 (`content-type not allowed`).
- **Disguised payload** (extension says `.jpg`, bytes are something else) →
  caught by the magic-byte sniff.
- **Decompression bomb** (tiny file, huge declared dimensions) → 413
  (`image dimensions exceed max_image_pixels`).
- **Path-traversal in `model_name`** (`../../etc/passwd`) → 400.
- **Invalid config patch** (e.g. `cpu_min > cpu_max`) → 422, server state
  unchanged.
- **Unknown config field** (typo) → 422 with the offending key in the error.
- **kubectl unavailable** → API still serves config + demo; status reports
  `kubectl_available: false`; reconcile errors logged with `WARN`.
- **Loadtest above caps** → 422 before any traffic is generated.
- **Cancelled loadtest** (client disconnect) → in-flight requests finish,
  no new ones launched, partial report returned.

## Flags / environment

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `-listen` | `ORCH_LISTEN` | `:9000` | HTTP listen address |
| `-config` | `ORCH_CONFIG` | `/var/lib/orchestrator/config.json` | Persisted config path |
| `-lb-url` | `ORCH_LB_URL` | `http://load-balancer:8080` | Upstream LB |
| `-namespace` | `ORCH_NAMESPACE` | `glaucoma` | Namespace for kubectl |
| `-kubectl` | `ORCH_KUBECTL` | `kubectl` | kubectl binary path |
| `-no-kube` | `ORCH_NO_KUBE` | unset | Disable cluster ops; config-only mode |
| `-read-timeout` | — | `30s` | HTTP read timeout |
| `-write-timeout` | — | `120s` | HTTP write timeout (long for loadtest) |
