# Load Balancer: Custom Go vs Kubernetes-Only

**Question:** can we drop the Go LB and rely only on what Kubernetes gives us out of the box?
**Short answer:** no — k8s cannot route by `Content-Length` natively.

## How our balancers work today

**Custom Go LB** ([load_balancer/main.go](load_balancer/main.go), ~80 lines, L7 reverse proxy):
- `POST /predict/` with `Content-Length ≥ 2.5 MB` → GPU worker
- `POST /predict/` with `Content-Length < 2.5 MB` → CPU worker
- `GET /models/` → CPU worker; `GET /health` → returns `{"status":"ok"}`

The entire routing decision is four lines of Go:
```go
if r.ContentLength >= threshold {
    gpuProxy.ServeHTTP(w, r)
} else {
    cpuProxy.ServeHTTP(w, r)
}
```

**k8s `Service` + kube-proxy** ([k8s/*.yaml](k8s/), L4, runs natively in cluster):
- ClusterIP services for `load-balancer`, `ai-worker-cpu`, `ai-worker-gpu`, plus NodePort for `frontend`
- Round-robin / random distribution between **same-type** pod replicas

**`HorizontalPodAutoscaler`** ([k8s/ai-worker-hpa.yaml](k8s/ai-worker-hpa.yaml)):
- CPU pool: 1 → 5 replicas at 70% CPU; GPU pool: 1 → 3 replicas at 75% CPU

Flow: `frontend → Service → Go LB pod → (size check) → CPU/GPU Service → worker pod`. The Go LB picks the **pool** (L7), kube-proxy picks the **pod** (L4), HPA decides **how many pods** exist.

## Head-to-head: Go LB vs a k8s-only replacement

Vanilla k8s primitives cannot match on request body size. To replicate the same routing using only k8s, you would need an **Ingress Controller** (Nginx, Envoy, Traefik) plus a **custom Lua/EnvoyFilter** that reads `$content_length` — Ingress alone has no body-size matcher.

| Aspect | Custom Go LB | k8s-only (Ingress + custom Lua/Envoy) |
|---|---|---|
| OSI layer | L7 (HTTP) | L7 (HTTP) |
| Routes by `Content-Length` | yes, native, one `if` | only via custom Lua / EnvoyFilter |
| Components to install | none (one Go binary in a pod) | Ingress Controller (~100 MB image, helm chart) |
| Configuration surface | 4 env vars | Ingress manifest + ConfigMap with Lua + controller helm values |
| Runtime reconfiguration | yes, via orchestrator REST (`kubectl set env` triggers rollout) | yes, via ConfigMap reload / controller hot-reload |
| Lines to maintain | ~80 lines of Go | ~30 lines Lua + 50+ lines YAML + controller upgrades |
| Logic location | one file, one language | spread across annotations, Lua, and controller config |
| Debugging | `go test`, `kubectl logs deployment/load-balancer` | decipher Lua + Nginx error logs in controller pod |
| Health-aware retry / circuit-breaker | no (returns 502 on backend failure) | yes, built into the controller |
| TLS termination | no | yes |
| Load balancing across pool replicas | done by kube-proxy downstream | done by the controller itself |
| Autoscaling | no (HPA scales workers, orthogonal) | no (same — HPA is orthogonal) |
| Portability | also runs in `docker-compose` unchanged | k8s-only |
| Operational risk | one tiny service | another platform component to upgrade and monitor |
| Image size | ~10 MB Alpine | controller ~100 MB + sidecars |
| Cold start | milliseconds | seconds (controller reload on rule change) |

## Verdict

For **this single requirement** — "route large images to GPU, small ones to CPU" — the Go LB is **simpler, more explicit, and more portable**. A k8s-only solution forces you to install a heavyweight Ingress Controller and write custom Lua just to inspect a header that vanilla k8s primitives ignore by design.

Migrating to an Ingress Controller becomes worthwhile only when **additional L7 features** appear: TLS termination, multi-host, rate-limiting, auth, retries, circuit-breakers — capabilities that come for free in mature controllers but are non-trivial to add to a custom Go service.

In the current architecture both layers coexist cleanly: **Go LB owns the domain decision (pool selection), k8s owns the infrastructure mechanics (replica balancing, autoscaling, service discovery)** — neither replaces the other.
