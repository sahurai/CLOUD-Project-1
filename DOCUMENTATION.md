# Glaucoma Detection Cloud Ecosystem — Project Documentation

**Team (max. 4 members):** Ilia Sukhina, Yevhenii Severin, Adam Partl

**Course:** Cloud Computing — Project 1

**Repository layout referenced throughout:** [`load_balancer/`](load_balancer/), [`orchestrator/`](orchestrator/), [`cloud_ai_worker/`](cloud_ai_worker/), [`cloud_frontend_app/`](cloud_frontend_app/), [`k8s/`](k8s/), supporting docs [`README.md`](README.md), [`EDGE_CASES.md`](EDGE_CASES.md), [`LOAD_BALANCER_COMPARISON.md`](LOAD_BALANCER_COMPARISON.md).

---

## 1. Project Goal and Focus

### 1.1 Motivation

Glaucoma is one of the leading causes of irreversible blindness worldwide. Early screening from retinal *fundus photographs* is effective but labour-intensive: a human grader must look at every image. The bottleneck is not the AI model — a trained Convolutional Neural Network (CNN) classifies a fundus image in well under a second — the bottleneck is **delivering that inference reliably and elastically at clinic scale**: many concurrent uploads, images of very different sizes (a phone snapshot vs. a 4K desktop fundus camera export), and the need to keep a heavy TensorFlow process from crashing a web server with Out-Of-Memory (OOM) errors.

This project is therefore **a cloud-infrastructure project**, not a medical-accuracy project. We build the cloud plumbing that runs a glaucoma-detection CNN as a horizontally scalable, resource-aware, self-tunable service on Kubernetes.

### 1.2 Project focus

Design and implement a **cloud-native microservice ecosystem** that:

1. Serves a glaucoma-detection CNN as an independent, statelessly scalable inference microservice.
2. Routes inference traffic to the *right class of worker node* depending on the cost of the request (small images → cheap CPU pool, large high-resolution images → GPU-profile pool) using a **custom Layer-7 (HTTP) load balancer** — the cloud-functionality component we implement ourselves.
3. Autoscales each worker pool independently with the Kubernetes **HorizontalPodAutoscaler (HPA)**.
4. Exposes a **control-plane orchestrator** (REST API) that lets an operator re-tune routing thresholds, HPA min/max replicas and upload safeguards *at runtime*, with validation and persistence — and that runs a built-in synthetic load test for demonstration.
5. Provides a decoupled **Streamlit dashboard** for medical staff.

### 1.3 Measurable objectives (used by the experiment in §6)

The assignment requires at least one measurable goal. We define the following, all verified by the scripted experiment ([`test_scalability_e2e.sh`](test_scalability_e2e.sh)):

| # | Measurable objective | Pass criterion | How measured |
|---|---|---|---|
| **M1** | Resource-aware routing works | 100 % of "small" requests (< 2.5 MB) reach the CPU pool **and** 100 % of "large" requests (≥ 2.5 MB) reach the GPU pool | `node_type` field in every `/predict/` response + load-balancer routing logs |
| **M2** | CPU worker pool autoscales under load | `ai-worker-cpu` replica count grows from `1` to `≥ 2` while sustained load is applied, then scales back down after the stabilization window | `kubectl -n glaucoma get hpa` / `get pods` sampled during the test |
| **M3** | GPU-profile worker pool autoscales under load | `ai-worker-gpu` replica count grows from `1` to `≥ 2` under sustained large-image load | same as M2 |
| **M4** | Load is actually spread across the new replicas | At least 2 distinct worker pod IDs serve requests for each pool during the run | `worker_id` (pod hostname) returned by the worker, aggregated in `worker_spread` by the orchestrator's `/api/demo/loadtest` |
| **M5** | Runtime reconfiguration takes effect | After `PUT /api/config {"size_threshold_bytes": …}` a borderline-sized request changes pool accordingly | orchestrator `/api/status` + a follow-up `/api/demo/predict` showing `routed_to_pool` |

A successful run prints:

```text
CPU routing:                 PASS
GPU routing:                 PASS
CPU HPA scaling:             PASS
GPU HPA scaling:             PASS
CPU unique worker pods:      2
GPU unique worker pods:      2
PASS: both CPU and GPU worker pools demonstrated HPA scaling.
```

---

## 2. Domain / Problem-Area Characterization

### 2.1 Application domain — medical image screening

* **Input data:** retinal fundus photographs (JPEG/PNG), highly variable in resolution and file size. The CNN expects a fixed `224 × 224 × 3` tensor, so every image is resized server-side; this means the *compute cost per request is roughly constant once decoded*, but the *network + decode cost grows with file size*, and large images are the ones that put memory pressure on a worker.
* **Output:** a single probability in `[0, 1]` (likelihood of glaucoma), surfaced to the clinician as a percentage plus a clinical band ("Low Risk" / "Suspicious" / "High Risk").
* **Non-functional needs:** availability during screening campaigns, elasticity (load is bursty — a clinic processes a batch, then nothing), isolation (a model crash must not take down the UI), observability (which node/pod handled a given medical request — an audit trail), and safety on untrusted uploads (size limits, type checks, decompression-bomb guards).

### 2.2 Cloud-computing domain — the parts we actually build

The project sits at the intersection of several classic cloud building blocks. We map each to a concrete artifact:

| Cloud concept | Where it appears in this project |
|---|---|
| **Containerization (Docker)** | Every service ships as a Docker image; multi-stage Alpine builds for the Go services keep images ~10 MB. |
| **Container orchestration (Kubernetes)** | `glaucoma` namespace, Deployments + Services, PersistentVolume for model files, NodePort for ingress to the UI. |
| **Microservices architecture** | 5 independently deployable services (orchestrator, frontend, load balancer, CPU worker, GPU worker). |
| **Elastic horizontal autoscaling** | Two `HorizontalPodAutoscaler` objects driven by the Metrics Server. |
| **Layer-7 / application-aware load balancing** | **Our own Go reverse proxy** that routes on `Content-Length` — *the self-implemented cloud-functionality component*. |
| **Control plane / Infrastructure-as-an-API** | The Go **orchestrator**: a REST API that mutates cluster state (HPA bounds, LB env) via `kubectl` and persists config to a volume. |
| **Stateless service design** | Workers keep nothing in memory except a model cache that can be rebuilt from the PV; any pod can serve any request. |

### 2.3 Why "resource-aware" routing is the interesting cloud problem here

A plain Kubernetes `Service` load-balances **between identical replicas of one pool** (L4, round-robin). It cannot look at an HTTP request and decide *which pool* should handle it. But our workload genuinely has two cost classes (small vs. large images) and we want them on different node profiles for cost/stability reasons. Bridging that gap — an L7 decision *in front of* the L4 mechanics — is the cloud-functionality we implement ourselves. §3 and the dedicated [`LOAD_BALANCER_COMPARISON.md`](LOAD_BALANCER_COMPARISON.md) justify building it rather than installing a heavyweight Ingress controller.

---

## 3. Analysis of Similar Solutions and How Ours Differs

### 3.1 Comparable approaches

| Approach | What it offers | Why it does not directly fit our requirement |
|---|---|---|
| **Plain Kubernetes `Service` + kube-proxy** | L4 load balancing across identical pods; trivial to use | Cannot route by request body size / `Content-Length` — it is L4 and content-agnostic. It picks a *pod*, never a *pool*. |
| **Kubernetes HPA alone** | Elastic replica count per Deployment | Orthogonal — decides *how many* pods, never *which* pod or pool a given request goes to. We use it, but it does not solve routing. |
| **Ingress Controller (NGINX / Traefik / Envoy / HAProxy)** | Mature L7: TLS, host/path routing, retries, rate limiting, circuit breakers | None of them match on request body size out of the box. Replicating our rule needs a custom NGINX/Lua snippet or an `EnvoyFilter` reading `$content_length`, plus a ~100 MB controller image, a Helm chart, and another platform component to upgrade and monitor. Heavy for a single `if`. |
| **Service mesh (Istio / Linkerd)** | Fine-grained L7 traffic policy, observability | Even more operational weight; still no native body-size matcher; massive overkill for a 5-service demo. |
| **Cloud-provider managed model endpoints (AWS SageMaker, GCP Vertex AI, Azure ML)** | Managed autoscaling inference, A/B traffic splitting | Vendor lock-in; opaque; the assignment asks us to *implement* a cloud-functionality component, not consume a managed one; and they still don't expose "route by upload size to a different instance type" as a first-class knob. |
| **Generic API gateways (Kong, KrakenD)** | Plugin-based L7 routing | Same story — request-size routing is, at best, a custom plugin; another component to run. |

### 3.2 How our solution differs

* **The routing primitive is the differentiator.** The whole CPU/GPU decision is four lines of Go (`if r.ContentLength >= threshold { gpuProxy } else { cpuProxy }`) in an ~80-line, ~10 MB Alpine container — versus a 100 MB Ingress controller plus custom Lua/Envoy config spread across annotations, a ConfigMap and Helm values.
* **Two complementary layers, cleanly separated.** Our Go LB owns the *domain decision* (which pool); kube-proxy owns the *infrastructure mechanic* (which pod within the pool); HPA owns *how many pods*. Neither replaces the other — see the layered flow in §4.1.
* **A real control plane for the demo.** Unlike "just deploy YAML", we ship the **orchestrator**: an Infrastructure-as-an-API service that re-tunes routing threshold, HPA min/max, target utilization and upload safeguards *at runtime* (validated, persisted, reconciled via `kubectl`), plus a built-in synthetic load tester that reports latency percentiles and per-pod spread. That makes the system *measurably* tunable, which is exactly what the experiment in §6 exercises.
* **Portability.** Because the LB is a tiny standalone binary, the exact same routing logic runs unchanged under `docker compose` for local development and under Kubernetes in the "cloud" deployment. An Ingress-based design would be k8s-only.
* **Honest scope.** We do *not* claim mesh-grade features (TLS termination, retries, circuit breakers, rate limiting) — see the explicit limitations in §5.2. The point is to demonstrate the cloud mechanics (containerization, orchestration, L7 routing, elastic autoscaling, a control-plane API), not to re-implement Envoy.

---

## 4. Concept — Architecture, Views, and Technical Analysis

### 4.1 Base architecture diagram

```text
                          ┌─────────────────────────────────────────────┐
                          │            Kubernetes namespace: glaucoma    │
                          │                                              │
  medical staff           │   ┌────────────┐                             │
  (browser) ──NodePort──▶ │   │  frontend  │  Streamlit dashboard        │
                          │   │ (Streamlit)│  GET /models/ , POST /predict│
                          │   └─────┬──────┘                             │
                          │         │ HTTP                               │
   operator / demo        │         ▼                                    │
   curl, scripts ─NodePort▶  ┌──────────────┐  validates upload          │
                          │  │ orchestrator │  (size, MIME, dimensions,  │
                          │  │   (Go API)   │   model_name); patches      │
                          │  └──────┬───────┘   HPA + LB env via kubectl  │
                          │         │ HTTP /predict/                     │
                          │         ▼                                    │
                          │  ┌──────────────┐   L7 routing decision      │
                          │  │ load-balancer│   if Content-Length ≥ T    │
                          │  │   (Go proxy) │      → GPU pool             │
                          │  └───┬──────┬───┘   else → CPU pool;          │
                          │      │      │       GET /models/ → CPU pool  │
                          │  L4  │      │  L4                            │
                          │      ▼      ▼                                │
                          │ ┌─────────┐ ┌─────────┐                      │
                          │ │ Service │ │ Service │   (kube-proxy: pick   │
                          │ │ cpu     │ │ gpu     │    a Ready pod, RR)   │
                          │ └───┬─────┘ └───┬─────┘                      │
                          │     │ 1..5      │ 1..3   ◀── HorizontalPod    │
                          │  ┌──┴───┐    ┌──┴───┐        Autoscaler       │
                          │  │ai-wk │ ...│ai-wk │   (CPU% target)         │
                          │  │ cpu  │    │ gpu  │   FastAPI + TF/Keras    │
                          │  └──┬───┘    └──┬───┘                        │
                          │     └────┬──────┘                            │
                          │          ▼                                   │
                          │   ┌──────────────┐                           │
                          │   │ models-pv    │  PersistentVolume: .h5/.keras
                          │   │  (PVC)       │  weights mounted read-only │
                          │   └──────────────┘                           │
                          │                                              │
                          │   Metrics Server ──feeds──▶ HPA controllers   │
                          └─────────────────────────────────────────────┘

Request flow (predict):  frontend/operator → orchestrator (validate) → load-balancer
                         → (size check) → cpu|gpu Service → worker pod → model.predict → JSON

Responsibility split:    Go LB picks the POOL (L7)
                         kube-proxy picks the POD inside the pool (L4)
                         HPA decides HOW MANY pods exist per pool
                         orchestrator RE-TUNES all of the above at runtime
```

### 4.2 Architectural views and the core functionality of each

#### 4.2.1 Logical / decomposition view — the five microservices

| Service | Tech | Core functionality |
|---|---|---|
| **Frontend** | Python / Streamlit ([`cloud_frontend_app/app.py`](cloud_frontend_app/app.py)) | Diagnostic dashboard for clinicians: uploads a fundus image, picks a model (`GET /models/`), calls `POST /predict/`, renders the probability, the clinical band, and the infrastructure telemetry (`node_type`, `worker_id`, `execution_time`). Fully decoupled — it knows only one backend URL and does no routing itself. |
| **Orchestrator** | Go ([`orchestrator/`](orchestrator/)) | Control plane / Infrastructure-as-an-API. Owns one validated, persisted JSON config object (routing threshold, HPA min/max & target per pool, upload safeguards, demo caps, upstream timeout). On `PUT /api/config` it reconciles the cluster (`kubectl patch` on HPAs, `kubectl set env` on the LB). Runs the demo: `POST /api/demo/predict` (single validated prediction) and `POST /api/demo/loadtest` (synthetic batch with latency percentiles + per-pod spread). Reports `GET /api/status` (pods, HPAs, config). Validates uploads *before* they reach the LB. |
| **Load balancer** | Go ([`load_balancer/main.go`](load_balancer/main.go)) | The self-implemented cloud-functionality component. An L7 reverse proxy: `POST /predict/` with `Content-Length ≥ SIZE_THRESHOLD` → GPU backend, else → CPU backend; `GET /models/` → CPU backend; `GET /health` → `{"status":"ok"}`. Logs every routing decision. Stateless, configured by 4 env vars. |
| **AI worker — CPU** | Python / FastAPI + TensorFlow/Keras ([`cloud_ai_worker/main.py`](cloud_ai_worker/main.py)) | Inference microservice on the cost-effective node profile. Lazy-loads & caches Keras models, preprocesses the image to `224×224×3`, runs `model.predict`, returns probability + `node_type` + `worker_id` (pod hostname) + `execution_time`. Cheap `/health` endpoint for probes (no TF load). |
| **AI worker — GPU** | same image as CPU worker | Same code, deployed with `NODE_TYPE="High-Performance GPU"` and a separate Deployment/Service/HPA so high-resolution images land on accelerated nodes without competing with the high-throughput CPU pool. |

#### 4.2.2 Deployment / physical view

* One Kubernetes namespace `glaucoma`; on minikube it is a single node, but the design is multi-node-ready (the CPU and GPU Deployments would carry different `nodeSelector`/taints in a real cluster).
* **Deployments + Services:** `ai-worker-cpu`, `ai-worker-gpu`, `load-balancer`, `frontend` (NodePort `30501`), `orchestrator` (NodePort `30900`).
* **PersistentVolume `models-pv` + PVC:** holds the `.h5`/`.keras` weights, mounted into worker pods. Model artifacts are deliberately *not* in Git (too large) — supplied via `models.zip` + [`prepare_models.sh`](prepare_models.sh) locally, or via PV / object storage / Git LFS in production.
* **RBAC:** the orchestrator runs under a ServiceAccount with `patch` rights on HPAs and Deployments in the namespace.
* **Metrics Server:** required by HPA; on minikube enabled with `minikube addons enable metrics-server`.

#### 4.2.3 Process / runtime view (request lifecycle)

1. Clinician uploads an image in the Streamlit UI → `POST /predict/` (multipart: `file`, `model_name`) to the configured backend URL (orchestrator, or LB directly in the bare stack).
2. **Orchestrator validation chain** (when used): size cap via a `limit+1` read (never buffers an oversized body) → declared content-type allowlist → magic-byte sniff (`http.DetectContentType`) → header-only `image.DecodeConfig` to enforce `max_image_pixels` (this is the decompression-bomb guard — a 1 KiB PNG that *claims* 100 000 × 100 000 is rejected without allocating the pixel buffer) → `model_name` sanitization (`[A-Za-z0-9._-]` only, no `..` segments). Failures map to `413` / `415` / `400`.
3. Orchestrator forwards to the **load balancer**, which reads `Content-Length`: ≥ threshold → GPU `ReverseProxy`, else → CPU `ReverseProxy`; logs `[route] POST /predict/ (<n> bytes) -> CPU|GPU`.
4. The pool's **Kubernetes Service** (kube-proxy) forwards to one Ready worker pod (round-robin/random among replicas).
5. **Worker:** `await file.read()` → resize to `224×224`, normalize to `[0,1]`, add batch dim → `get_model(model_name)` (cache hit, or load from PV on first use, `compile=False`) → `model.predict(x)[0][0]` → optional synthetic CPU burn for the HPA demo → JSON `{status, probability, node_type, worker_id, execution_time}`.
6. Response bubbles back; the orchestrator wraps it with `{orchestrator:{validated:{…}, routed_to_pool:"cpu|gpu"}, upstream:{…}}`; the UI renders probability + clinical band + telemetry.

#### 4.2.4 Scaling / control view

* **Metrics Server** scrapes pod CPU.
* **HPA `ai-worker-cpu`:** 1 → 5 replicas, target ≈ 70 % CPU. **HPA `ai-worker-gpu`:** 1 → 3 replicas, target ≈ 75 % CPU. (Defaults — all four numbers are runtime-tunable through the orchestrator within hard bounds `[0,50]` replicas, `[1,100]` % utilization.)
* Scale-up is not instant; until new pods are `Ready`, traffic stays on existing pods. Scale-down waits for the stabilization window. (For local minikube demos the workers set `LOAD_TEST_CPU_BURN_SECONDS=0.8` so inference creates measurable CPU pressure for the CPU-metric HPA; set to `0` for inference-only runs.)
* **Orchestrator** is the human-facing control loop on top: `PUT /api/config` validates the *merged* config, persists it to `/var/lib/orchestrator/config.json`, and an `OnChange` hook runs `kubectl patch hpa …` and `kubectl set env deployment/load-balancer SIZE_THRESHOLD=…`. Reconcile errors are logged (`WARN`) but the API still returns the new config; `POST /api/config/apply` force-reconciles; `POST /api/config/reset` restores defaults. Behaviour under failures is catalogued in [`EDGE_CASES.md`](EDGE_CASES.md).

### 4.3 Detailed technical analysis of the chosen technology and its algorithms

The self-implemented cloud-functionality component is the **Layer-7 resource-aware load balancer** (Go). Its decision is small, so the "algorithm" is best described together with the surrounding mechanics of the three cooperating layers.

#### 4.3.1 The L7 routing algorithm (Go reverse proxy)

```go
const defaultThreshold = 2_500_000 // bytes (~2.5 MB), overridable via SIZE_THRESHOLD

mux.HandleFunc("/predict/", func(w http.ResponseWriter, r *http.Request) {
    if r.ContentLength >= int64(threshold) {
        log.Printf("[route] %s %s (%d bytes) -> GPU", r.Method, r.URL.Path, r.ContentLength)
        gpuProxy.ServeHTTP(w, r)        // httputil.ReverseProxy to GPU_BACKEND
    } else {
        log.Printf("[route] %s %s (%d bytes) -> CPU", r.Method, r.URL.Path, r.ContentLength)
        cpuProxy.ServeHTTP(w, r)        // httputil.ReverseProxy to CPU_BACKEND
    }
})
mux.HandleFunc("/models/", /* always */ cpuProxy.ServeHTTP)
mux.HandleFunc("/health",  /* returns {"status":"ok"} */)
```

* **Why `Content-Length`:** it is available in the request headers *before* the body is read, so the routing decision is O(1) and requires no buffering. The file size is a good proxy for "this is a high-resolution fundus image that would put memory pressure on a CPU worker if many arrive at once" — the kind of request we want on the GPU-profile pool.
* **Why a custom proxy over an Ingress controller:** vanilla Kubernetes primitives have no body-size matcher; an Ingress-based equivalent needs a 100 MB controller plus custom Lua/`EnvoyFilter`. The trade-off table is in [`LOAD_BALANCER_COMPARISON.md`](LOAD_BALANCER_COMPARISON.md). Cost of our choice: no TLS termination, no retry/circuit-breaker, single replica (no HPA on the LB itself) — acceptable for the scope, listed in §5.2.
* **`httputil.ReverseProxy`:** Go's standard library proxy. We override the `Director` so the upstream `Host` header is the backend's host (correct virtual-host behaviour); the path and body are forwarded untouched. On a backend failure the default behaviour is a `502` — we do not retry (deliberate, documented limitation).
* **Statelessness & config:** four env vars only — `CPU_BACKEND`, `GPU_BACKEND`, `SIZE_THRESHOLD`, `PORT`. No persistent state, so the LB pod is freely restartable; the orchestrator changes `SIZE_THRESHOLD` via `kubectl set env`, which triggers a rolling restart of the (single-replica) LB Deployment — a brief blip, noted in [`EDGE_CASES.md`](EDGE_CASES.md).

#### 4.3.2 The L4 layer — Kubernetes `Service` / kube-proxy

Each pool has a `ClusterIP` Service. kube-proxy programs iptables/IPVS rules that distribute connections **across the Ready pods of that pool** (effectively round-robin / random). It is content-agnostic — it never sees the HTTP body. This is exactly why the L7 LB is needed *in front of it*: the LB picks the **pool**, kube-proxy picks the **pod**.

#### 4.3.3 The autoscaling control algorithm — HorizontalPodAutoscaler

The HPA controller periodically reads each pool's average CPU utilization from the Metrics Server and applies the standard ratio formula:

```text
desiredReplicas = ceil( currentReplicas × ( currentMetricValue / targetMetricValue ) )
```

clamped to `[minReplicas, maxReplicas]` (CPU pool `[1,5]` @ 70 %, GPU pool `[1,3]` @ 75 % by default). Scale-up is prompt but bounded by pod start time; scale-down is delayed by a stabilization window to avoid flapping. HPA is **orthogonal** to routing — it changes *how many* pods exist per pool, never *which* pod or pool a request hits. Together: **LB = which pool, kube-proxy = which pod, HPA = how many pods.**

#### 4.3.4 The control-plane algorithm — orchestrator config reconciliation

1. `PUT /api/config` accepts a *partial* JSON body; unknown fields → `422` (so typos surface).
2. The patch is merged onto the current config; **validation runs on the merged result** against hard bounds and cross-field invariants (e.g. `cpu_min_replicas ≤ cpu_max_replicas`, `allowed_content_types` must each start with `image/`). Any violation → `422`, server state unchanged. A field explicitly set to `null` is a no-op.
3. On success the new config is written atomically to disk, then an `OnChange` hook reconciles the cluster:
   * `kubectl patch hpa ai-worker-cpu / ai-worker-gpu` with new min/max/target,
   * `kubectl set env deployment/load-balancer SIZE_THRESHOLD=<n>`.
   Reconcile errors are logged at `WARN`; the API still returns the new (now-persisted) config. `POST /api/config/apply` re-runs the reconciliation (useful right after deploy, or if `kubectl` was unavailable earlier); it returns `207 Multi-Status` with a per-step error list if any patch failed.
4. **Upload safeguard chain** on `POST /api/demo/predict` (run before forwarding to the LB): size cap (`limit+1` read) → declared content-type allowlist → magic-byte sniff → header-only `image.DecodeConfig` for `max_image_pixels` (decompression-bomb guard) → `model_name` sanitization. Errors → `413` / `415` / `400`.
5. **Synthetic load test** `POST /api/demo/loadtest`: fires `n` predict requests at the LB with the configured `concurrency` (both capped: `n ≤ max_loadtest_requests`, `concurrency ≤ max_loadtest_concurrency`, otherwise `422` *before* any traffic). If no `image_base64` is supplied it generates a tiny synthetic JPEG (zero prerequisites). It reports duration, throughput (RPS), success/failure counts, latency percentiles (`min/avg/p50/p95/p99/max`), `node_distribution`, `worker_spread` (requests per worker pod — the key signal that HPA actually spread load), and an HTTP `status_codes` breakdown. Client disconnect → in-flight requests finish, no new ones launched, partial report returned.

#### 4.3.5 The inference algorithm — CNN on the worker (FastAPI + TensorFlow/Keras)

| Step | Detail |
|---|---|
| **Model management** | Lazy loading + in-process cache (`MODEL_CACHE` dict). On the first request for a given filename the worker loads it from `MODEL_DIR` (the PV) with `load_model(path, compile=False)` — inference-only, so no optimizer state, less RAM, faster init. Subsequent requests are cache hits. Supported formats: `.h5`, `.keras`, TF SavedModel directories (`saved_model.pb`). `GET /models/` lists available files for the UI dropdown. |
| **Preprocessing** | `PIL.Image.open(BytesIO(bytes)).convert("RGB")` (normalizes grayscale/RGBA inputs) → `resize((224,224))` (the CNN's expected input — typical for ResNet/VGG-style backbones) → `img_to_array(img) / 255.0` (normalize pixels to `[0,1]`) → `np.expand_dims(..., axis=0)` (batch dimension, final shape `(1, 224, 224, 3)`). Done entirely in memory; the upload is never written to disk. |
| **Inference** | `model.predict(x)` returns a `(1,1)` array; the scalar is the glaucoma probability. We multiply by 100 for the UI. The CNN is a standard image classifier: stacked convolution + pooling feature extractor followed by dense layers ending in a sigmoid — the project consumes a *pre-trained* model file rather than training one (training is out of scope; this is the cloud-infrastructure project). |
| **Telemetry** | Every response carries `node_type` (from the `NODE_TYPE` env var — "Standard CPU" vs "High-Performance GPU"), `worker_id` (`socket.gethostname()` = the Kubernetes pod name, the audit trail / scaling proof), and `execution_time` (server-measured inference latency). |
| **Health** | `GET /health` returns `{status, node_type, worker_id, model_dir}` *without* touching TensorFlow, so readiness probes stay cheap while pods are scaling. |
| **HPA demo aid** | `LOAD_TEST_CPU_BURN_SECONDS` (default `0`) optionally spins one core with deterministic FP work after `predict`, so on a CPU-metric HPA in minikube the load test produces measurable pressure. Production: leave at `0`. |
| **Error handling** | Corrupted image, missing model, etc. are caught and returned as `{status:"error", message:…}` rather than crashing the worker. |

---

## 5. Detailed Technical Implementation Description

### 5.1 Component inventory and key files

| Component | Language / framework | Key source files | Container |
|---|---|---|---|
| Frontend | Python 3.10+, Streamlit, Requests | [`cloud_frontend_app/app.py`](cloud_frontend_app/app.py), [`requirements.txt`](cloud_frontend_app/requirements.txt) | [`cloud_frontend_app/Dockerfile`](cloud_frontend_app/Dockerfile) |
| Orchestrator | Go (stdlib `net/http`, `image`, `os/exec`→`kubectl`) | [`orchestrator/main.go`](orchestrator/main.go) (entry, flags, graceful shutdown), [`config.go`](orchestrator/config.go) (defaults, validation, persistence), [`validate.go`](orchestrator/validate.go) (upload safeguards), [`k8s.go`](orchestrator/k8s.go) (`kubectl` wrappers), [`handlers.go`](orchestrator/handlers.go) (`/api/config`, `/api/status`, `/api/demo/*`), [`demo.go`](orchestrator/demo.go) (load-test runner), [`server.go`](orchestrator/server.go) (routes/middleware) | [`orchestrator/Dockerfile`](orchestrator/Dockerfile) |
| Load balancer | Go (stdlib `net/http/httputil`) | [`load_balancer/main.go`](load_balancer/main.go) (~80 LOC), [`main_test.go`](load_balancer/main_test.go) | [`load_balancer/Dockerfile`](load_balancer/Dockerfile) (multi-stage Alpine, ~10 MB) |
| AI worker (CPU & GPU) | Python, FastAPI/uvicorn, TensorFlow/Keras, Pillow, NumPy | [`cloud_ai_worker/main.py`](cloud_ai_worker/main.py), [`requirements.txt`](cloud_ai_worker/requirements.txt), [`tests/test_main.py`](cloud_ai_worker/tests/test_main.py) | [`cloud_ai_worker/Dockerfile`](cloud_ai_worker/Dockerfile) |
| K8s manifests | YAML | [`k8s/namespace.yaml`](k8s/namespace.yaml), [`models-pv.yaml`](k8s/models-pv.yaml), [`ai-worker-cpu.yaml`](k8s/ai-worker-cpu.yaml), [`ai-worker-gpu.yaml`](k8s/ai-worker-gpu.yaml), [`load-balancer.yaml`](k8s/load-balancer.yaml), [`frontend.yaml`](k8s/frontend.yaml), [`ai-worker-hpa.yaml`](k8s/ai-worker-hpa.yaml), [`orchestrator.yaml`](k8s/orchestrator.yaml) (+ RBAC), [`deploy.sh`](k8s/deploy.sh) | — |
| Local stack | Docker Compose | [`docker-compose.yml`](docker-compose.yml), [`prepare_models.sh`](prepare_models.sh) | — |
| Tests / experiment | Bash + `go test` + `pytest` | [`test_all.sh`](test_all.sh), [`test_load_balancer.sh`](test_load_balancer.sh), [`test_routing_with_logs.sh`](test_routing_with_logs.sh), [`test_scalability.sh`](test_scalability.sh), [`test_scalability_e2e.sh`](test_scalability_e2e.sh) | — |

### 5.2 Limitations / boundaries of the solution

Stated explicitly (full failure catalogue in [`EDGE_CASES.md`](EDGE_CASES.md)):

* **The load balancer is single-replica and has no HPA of its own.** If it becomes the bottleneck it does not scale; a restart (rolling update, or an `SIZE_THRESHOLD` change) makes it briefly unavailable — there is no PodDisruptionBudget. It also does **no** retry / circuit-breaker (a backend failure → `502`) and **no** TLS termination.
* **The orchestrator is single-replica** (`Recreate` strategy, because it mounts an RWO PVC for its config). It is a control-plane convenience, not a hardened gateway; if overloaded it slows API/demo calls. Its `kubectl`-based reconciliation is best-effort — if `kubectl` is unavailable the API still serves config + demo, `kubectl_available:false` is reported, and `POST /api/config/apply` must be re-run later.
* **No application-level admission control / queueing.** The system does **not** emit `429 Too Many Requests`; under overload, latency grows and timeouts/errors appear. Kubernetes Services have no HTTP timeout — timeouts arise client-side, or via the orchestrator's `UpstreamTimeoutSeconds` (→ `502`).
* **Kubernetes is not load-aware at the application level.** A `Ready` pod can receive another request even while busy with many; HPA reacts to CPU%, not in-flight request count. If `maxReplicas` is hit, no more pods are added (the orchestrator can raise it at runtime, but it is still a hard ceiling at any moment). If the cluster runs out of CPU/RAM, new pods stay `Pending` and effective capacity does not grow.
* **"GPU" is a profile, not real hardware in this submission.** The GPU worker runs the same image with a different `NODE_TYPE` and its own Deployment/Service/HPA; on minikube there is no actual accelerator and HPA is driven by CPU metrics (hence the optional `LOAD_TEST_CPU_BURN_SECONDS`). The architecture is ready for real GPU nodes (`nodeSelector`/taints) but that is outside this environment.
* **No medical-accuracy claims.** The project validates *cloud behaviour* (routing, scaling, tunability), not diagnostic quality. The CNN model file is consumed pre-trained; model weights are not in Git (delivered via `models.zip`/PV/object storage/LFS).
* **Single-namespace, single-node demo target (minikube).** Multi-tenancy, network policies, secrets management, persistent metrics/logging stacks, and TLS are out of scope.

### 5.3 The self-implemented cloud-functionality component (in depth): the Go L7 Load Balancer (Docker container)

This is the component the assignment asks us to describe in detail as *our own implementation of a chosen cloud functionality* — here, **application-aware (Layer-7) load balancing**, packaged as a Docker container and run as a Kubernetes Deployment.

* **What cloud functionality it implements:** content-aware request routing across heterogeneous backend pools — something Kubernetes' built-in L4 `Service` cannot do and managed Ingress controllers only do via custom plugins. It turns "route by request body size to a different instance class" into a first-class, four-line decision.
* **Internal design:**
  * `main()` reads four env vars (`CPU_BACKEND` default `http://ai-worker-cpu:8001`, `GPU_BACKEND` default `http://ai-worker-gpu:8001`, `SIZE_THRESHOLD` default `2_500_000`, `PORT` default `8080`), parses the backend URLs, and builds two `*httputil.ReverseProxy` instances via `newProxy(target)`.
  * `newProxy` wraps `httputil.NewSingleHostReverseProxy` and overrides the `Director` so the outgoing `Host` header equals the target host (correct virtual hosting); request path and body are forwarded verbatim.
  * Three handlers on a `http.ServeMux`: `/predict/` (the `Content-Length` branch → GPU or CPU proxy, with a `[route]` log line), `/models/` (always CPU proxy, logged), `/health` (returns `{"status":"ok"}` with `Content-Type: application/json`).
  * `http.ListenAndServe(":"+port, mux)` — no extra middleware, no state.
* **Containerization:** [`load_balancer/Dockerfile`](load_balancer/Dockerfile) is a multi-stage build — a Go builder stage compiles a static binary, the final stage is `alpine` with just that binary → ~10 MB image, milliseconds cold start. Built with `docker build -t glaucoma/load-balancer:latest ./load_balancer`.
* **Kubernetes integration:** [`k8s/load-balancer.yaml`](k8s/load-balancer.yaml) defines a Deployment (`replicas: 1`, container port `8080`, env vars pointing at the worker Services) and a `ClusterIP` Service `load-balancer:8080`. The orchestrator mutates `SIZE_THRESHOLD` at runtime with `kubectl set env deployment/load-balancer SIZE_THRESHOLD=<n>`, which triggers a rolling restart.
* **Local parity:** the same container runs under [`docker-compose.yml`](docker-compose.yml) (exposed on host `:8000` → container `:8080`) so developers exercise the *identical* routing code without a cluster.
* **Observability:** stdout logs every decision (`[route] POST /predict/ (124987 bytes) -> CPU`, `[route] POST /predict/ (3500000 bytes) -> GPU`, `[route] GET /models/ -> CPU`), so routing is auditable via `docker compose logs -f load-balancer` or `kubectl -n glaucoma logs -f deployment/load-balancer`. Unit-tested in [`load_balancer/main_test.go`](load_balancer/main_test.go) (threshold logic, `/health`, env handling, a routing benchmark).

### 5.4 Worker, orchestrator, frontend implementation notes

* **Worker** ([`cloud_ai_worker/main.py`](cloud_ai_worker/main.py)) — see §4.3.5. FastAPI app with `GET /models/`, `GET /health`, `POST /predict/`. Env: `PORT`, `MODEL_DIR`, `NODE_TYPE`, `TF_CPP_MIN_LOG_LEVEL`, `LOAD_TEST_CPU_BURN_SECONDS`. The CPU and GPU Deployments use this same image with different `NODE_TYPE` and separate HPAs. Models come from the `models-pv` PersistentVolume.
* **Orchestrator** ([`orchestrator/`](orchestrator/)) — see §4.3.4. Flags/env: `-listen`/`ORCH_LISTEN` (`:9000`), `-config`/`ORCH_CONFIG` (`/var/lib/orchestrator/config.json`), `-lb-url`/`ORCH_LB_URL` (`http://load-balancer:8080`), `-namespace`/`ORCH_NAMESPACE` (`glaucoma`), `-kubectl`/`ORCH_KUBECTL`, `-no-kube`/`ORCH_NO_KUBE` (config-only mode for local runs), `-read-timeout` (`30s`), `-write-timeout` (`120s`, long for load tests). Config fields and bounds: `size_threshold_bytes` `[1024, 1 GiB]` default 2 500 000; `cpu_min/max_replicas` & `gpu_min/max_replicas` `[0,50]` default 1/5 and 1/3; `cpu/gpu_target_utilization` `[1,100]%` default 70/75; `max_upload_bytes` `[4 KiB,64 MiB]` default 16 MiB; `max_image_pixels` `[4096, 64 000 000]` default 16 000 000; `allowed_content_types` default `image/jpeg,image/png`; `max_loadtest_requests` `[1,5000]` default 200; `max_loadtest_concurrency` `[1,256]` default 16; `upstream_timeout_seconds` `[1,600]` default 60. Endpoints: `GET /healthz`, `GET /api/config`, `PUT /api/config`, `POST /api/config/apply`, `POST /api/config/reset`, `GET /api/status`, `POST /api/demo/predict`, `POST /api/demo/loadtest`.
* **Frontend** ([`cloud_frontend_app/app.py`](cloud_frontend_app/app.py)) — wide-layout Streamlit dashboard; env `BACKEND_URL`. Calls `GET /models/` to populate the model dropdown and `POST /predict/` on upload; renders the probability, the clinical band (Low Risk / Suspicious / High Risk), and the cloud telemetry (`node_type`, `worker_id`, `execution_time`). Stateless, fully decoupled — it never routes.

### 5.5 Step-by-step: configuring and running the whole system

#### Option A — Local development (Docker Compose)

```bash
# 0. Prerequisites: Docker + Docker Compose installed.
# 1. Provide the model weights (not in Git): put models.zip in the repo root, then
./prepare_models.sh                 # extracts models.zip → ./models
# 2. Build and start the whole stack (frontend, load balancer, CPU worker, GPU worker)
docker compose up --build
# 3. Open the UI and the LB API
#    Frontend:        http://localhost:8501
#    Load Balancer:   http://localhost:8000   (GET /health, POST /predict/, GET /models/)
# 4. Watch routing decisions live (separate terminal)
docker compose logs -f load-balancer
# 5. Verify routing with the helper script (generates a ~500 KB and a ~3.5 MB image)
docker compose up --build -d
./test_load_balancer.sh
# or send several sizes and print the matching LB logs:
./test_routing_with_logs.sh http://localhost:8000 compose
```

#### Option B — Cloud deployment (Kubernetes / minikube)

```bash
# 0. Prerequisites: Docker, minikube, kubectl (or: alias kubectl="minikube kubectl --").
# 1. Start the cluster and the Metrics Server (required for HPA)
minikube start --driver=docker
minikube addons enable metrics-server
# 2. Provide model weights as in Option A (models.zip in repo root, or .h5/.keras in ./models)
# 3. Build all images into minikube's Docker and apply every manifest in k8s/
./k8s/deploy.sh                     # build + apply; later: ./k8s/deploy.sh apply (re-apply only)
#    Creates, in namespace `glaucoma`:
#      namespace, models-pv (PV+PVC), ai-worker-cpu (Deploy+Svc), ai-worker-gpu (Deploy+Svc),
#      load-balancer (Deploy+Svc), frontend (Deploy+Svc NodePort 30501),
#      ai-worker-hpa (HPA cpu 1→5@70%, gpu 1→3@75%), orchestrator (Deploy+Svc NodePort 30900 + RBAC)
# 4. Get the URLs
minikube service frontend     -n glaucoma --url        # the clinician dashboard
ORCH_URL=$(minikube service orchestrator -n glaucoma --url)
# 5. Sanity checks
kubectl -n glaucoma get pods
kubectl -n glaucoma get hpa
kubectl top nodes                                      # confirms Metrics Server is up
# 6. Inspect / tune the live config through the orchestrator
curl -s "$ORCH_URL/api/config" | jq
curl -X PUT "$ORCH_URL/api/config" -H 'Content-Type: application/json' \
     -d '{"size_threshold_bytes": 1500000, "cpu_max_replicas": 8, "gpu_max_replicas": 4}'
curl -s "$ORCH_URL/api/status" | jq '.cluster'
# (if kubectl was unavailable on a PUT:)  curl -X POST "$ORCH_URL/api/config/apply"
# 7. Run a single validated prediction (rejects oversize / wrong type / decompression bomb / bad model_name)
curl -X POST "$ORCH_URL/api/demo/predict" -F file=@models/sample.jpg -F model_name=glaucoma_v1.h5
# 8. Run a synthetic batch load test (latency percentiles + per-pod spread)
curl -X POST "$ORCH_URL/api/demo/loadtest" -H 'Content-Type: application/json' \
     -d '{"n": 100, "concurrency": 8, "model_name": "glaucoma_v1.h5"}'
```

#### Option C — Orchestrator alone, no cluster (config-only)

```bash
cd orchestrator
go build -o orchestrator .
./orchestrator -listen :9000 -lb-url http://localhost:8000 -no-kube   # serves config + demo, no kubectl
```

#### Running the unit tests

```bash
cd cloud_ai_worker && pip install -r requirements.txt && python -m pytest tests/ -v   # worker
cd load_balancer   && go test -v ./...                                                # LB
./test_all.sh                                                                          # all + integration if k8s present
```

#### Common troubleshooting (full list in [`README.md`](README.md))

* minikube `/var` full → `docker system prune -a --volumes`, `minikube delete`, `minikube start --driver=docker --cpus=2 --memory=6000`.
* Metrics Server `0/1` → wait 1–2 min; or `minikube addons disable metrics-server && minikube addons enable metrics-server`.
* `pip install` DNS failure during image build → set Docker DNS (`/etc/docker/daemon.json` → `"dns":["8.8.8.8","1.1.1.1"]`, restart Docker) or build with `--network=host`, then `./k8s/deploy.sh apply`.

---

## 6. Experiment — Verifying the Stated Goal

### 6.1 Purpose

Demonstrate, with a single scripted run, that the system fulfils the measurable objectives M1–M5 from §1.3: **(a)** the L7 load balancer routes light vs. heavy inference traffic to different worker pools, and **(b)** Kubernetes HPA elastically adds replicas to each pool under sustained load (and **(c)**, via the orchestrator, that the routing threshold is tunable at runtime). This is a cloud-infrastructure experiment, not a medical-accuracy test.

### 6.2 Setup

1. Deploy to minikube as in §5.5 Option B (`./k8s/deploy.sh`; `minikube addons enable metrics-server`). The worker manifests ship with `LOAD_TEST_CPU_BURN_SECONDS=0.8` so inference produces measurable CPU pressure for the CPU-based HPA in minikube.
2. Keep a port-forward to the load balancer open:
   ```bash
   kubectl -n glaucoma port-forward svc/load-balancer 8000:8080
   ```
3. In separate terminals, watch the cluster react:
   ```bash
   kubectl -n glaucoma get hpa  -w
   kubectl -n glaucoma get pods -w
   ```

### 6.3 Procedure

Run the end-to-end proof — it sends both CPU-routed (small) and GPU-routed (large) traffic, monitors both HPAs, samples pod counts, and prints an automatic verdict:

```bash
# args: <LB-url> <duration-seconds> <cpu-concurrency> <gpu-concurrency>
./test_scalability_e2e.sh http://localhost:8000 360 20 10
```

(Optionally, single-pool runs: `./test_scalability.sh http://localhost:8000 300 12 cpu` and `… 300 8 gpu`. For the runtime-tuning objective M5: `curl -X PUT $ORCH_URL/api/config -d '{"size_threshold_bytes":1500000}'`, then `curl -X POST $ORCH_URL/api/demo/predict -F file=@... -F model_name=...` and check `orchestrator.routed_to_pool`, plus `curl -s $ORCH_URL/api/status | jq '.cluster.hpas'`.)

### 6.4 Expected result (success criterion)

```text
CPU routing:                 PASS      ← M1: every small request reached the CPU pool
GPU routing:                 PASS      ← M1: every large request reached the GPU pool
CPU HPA scaling:             PASS      ← M2: ai-worker-cpu went 1 → ≥2 replicas under load
GPU HPA scaling:             PASS      ← M3: ai-worker-gpu went 1 → ≥2 replicas under load
CPU unique worker pods:      2         ← M4: load was spread over ≥2 CPU pods
GPU unique worker pods:      2         ← M4: load was spread over ≥2 GPU pods
PASS: both CPU and GPU worker pools demonstrated HPA scaling.
```

Corroborating evidence:

* Load-balancer log (`kubectl -n glaucoma logs -f deployment/load-balancer`):
  ```text
  [route] POST /predict/ (124987 bytes) -> CPU
  [route] POST /predict/ (3500000 bytes) -> GPU
  [route] GET /models/ -> CPU
  ```
* `kubectl -n glaucoma get hpa` shows `TARGETS` above the threshold and `REPLICAS` rising during the run, then settling back after the HPA stabilization window once load stops.
* The orchestrator's `POST /api/demo/loadtest` response gives a quantitative view: `latency_ms` percentiles, `throughput_rps`, and a `worker_spread` map with one entry per worker pod — the clearest single signal that HPA-added replicas actually took traffic.

### 6.5 Interpretation

A `PASS` on all five verdict lines means the project's goal is met: heterogeneous inference traffic is **routed by content (size)** to the appropriate worker class by our self-implemented L7 load balancer, each worker class **scales elastically** under load via Kubernetes HPA with traffic spread across the new pods, and the routing/scaling parameters are **re-tunable at runtime** through the orchestrator control plane — all within a containerized, Kubernetes-orchestrated microservice ecosystem.

---

## Appendix — Related documents in this repository

* [`README.md`](README.md) — quick start, full command reference, troubleshooting.
* [`LOAD_BALANCER_COMPARISON.md`](LOAD_BALANCER_COMPARISON.md) — detailed "custom Go LB vs. Kubernetes-only" trade-off analysis.
* [`EDGE_CASES.md`](EDGE_CASES.md) — exhaustive catalogue of system behaviour under load, pod failures, routing/validation edge cases, and network anomalies.
* [`orchestrator/README.md`](orchestrator/README.md) — full orchestrator API reference, config bounds, edge cases, flags.
* [`load_balancer/README.md`](load_balancer/README.md), [`cloud_ai_worker/README.md`](cloud_ai_worker/README.md), [`cloud_frontend_app/README.md`](cloud_frontend_app/README.md), [`k8s/README.md`](k8s/README.md) — per-component documentation.
