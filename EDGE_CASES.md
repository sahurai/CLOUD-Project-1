# Edge cases — system behavior under load and failures

This document describes how our system (Go load-balancer + Kubernetes HPA + orchestrator) behaves in individual situations. For the rationale of *why* we keep a custom Go LB instead of pure k8s see [LOAD_BALANCER_COMPARISON.md](LOAD_BALANCER_COMPARISON.md).

## Load and scaling

| Case | Our solution in Kubernetes |
|---|---|
| Normal load | Request goes through `load-balancer` to an available `ai-worker-cpu` or `ai-worker-gpu` pod. Response comes back normally. |
| Growing load | HPA watches CPU utilization of worker pods. As load rises, it starts to increase the replica count. |
| CPU worker scaling | `ai-worker-cpu` scales from `1` to `5` pods by CPU utilization, target around `70%`. |
| GPU worker scaling | `ai-worker-gpu` scales from `1` to `3` pods by CPU utilization, target around `75%`. |
| Sudden traffic spike | Kubernetes starts creating new pods, but scaling is not instant. Until new pods become `Ready`, requests go to existing pods. |
| Replica limit reached | If the CPU pool hits `5/5` or the GPU pool hits `3/3`, HPA stops creating pods. Requests continue to be spread across existing Ready pods. This ceiling is not hard-coded — the orchestrator can raise it via REST at runtime. |
| Overloaded pods | Kubernetes does not know the internal load of the application. If a pod is `Ready`, the Service can send it another request even if it is already processing many. |
| Cluster out of CPU/RAM | If the cluster lacks resources, new pods stay in `Pending`. Effective capacity does not increase. |
| Falling load | HPA does not scale down immediately. It waits for the stabilization window and then gradually reduces the pod count. |

## Pod failures

| Case | Our solution in Kubernetes |
|---|---|
| CPU throttling | If a worker exceeds its CPU limit, Kubernetes does not kill it but throttles its CPU. The result is higher latency. |
| Memory limit exceeded | If a worker exceeds its memory limit, it can be terminated as `OOMKilled`. Kubernetes then restarts it. |
| Pod is `NotReady` | Kubernetes removes the pod from Service endpoints. No new requests are sent to it. |
| Pod crashes | The container is restarted. During the restart the available capacity is lower. |
| Load-balancer overloaded | The load-balancer currently runs `1` replica. If it becomes a bottleneck, Kubernetes will not auto-scale it because we have no HPA for it. |
| Orchestrator overloaded | The orchestrator currently runs `1` replica (`Recreate` strategy due to RWO PVC). If it becomes a bottleneck it can slow API/demo requests. |
| LB / orchestrator restart | With `replicas: 1` and no PodDisruptionBudget both services are briefly unavailable during rolling-update. Workers are unaffected. |

## Routing and validation

| Case | Our solution in Kubernetes |
|---|---|
| CPU/GPU routing | `load-balancer` routes requests by size: smaller requests go to the CPU pool, larger ones to the GPU pool. |
| Routing threshold | Default threshold is `2 500 000 bytes`. Requests above this value go to the GPU pool. |
| Threshold change at runtime | The orchestrator does `kubectl set env SIZE_THRESHOLD=...` on the LB deployment, triggering a RollingUpdate. With `replicas: 1` the LB is briefly unavailable and some requests may fail. |
| Validation before LB | The orchestrator rejects oversized uploads, unsupported MIME types, excessive image dimensions and unsafe `model_name` before the request reaches the LB. The LB itself does not perform these checks. |

## Network behavior

| Case | Our solution in Kubernetes |
|---|---|
| Timeout (direct requests via LB) | The Kubernetes Service has no HTTP timeout. Timeouts arise on the client, orchestrator or proxy side. |
| Timeout (demo via orchestrator) | The orchestrator has its own `UpstreamTimeoutSeconds` — once it expires the orchestrator returns `502 Bad Gateway`. |
| Request during scaling | Kubernetes does not pause a request while a new pod is being created. The request goes to an existing Ready pod. |
| No free worker | Kubernetes does not wait for a "free" worker. If pods are Ready, the Service keeps distributing requests across them. |
| Too many requests | Our solution currently does not automatically return `429 Too Many Requests`. Under overload latency grows and timeouts or errors may occur. |

## Summary

| What Kubernetes handles | What Kubernetes does not handle |
|---|---|
| Worker scaling via HPA | Smart HTTP queueing |
| Restart of crashed containers | Automatic `429 Too Many Requests` |
| Eviction of `NotReady` pods | Waiting for a free pod |
| Traffic distribution across Ready pods | Retry / circuit breaker |
| | Scaling above `maxReplicas` |
