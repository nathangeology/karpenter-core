# Pod Lifecycle Timing Observability

## Problem Statement

When a deployment rolls out new pods, operators experience startup latency but have no visibility into *where* that latency comes from. Total time-to-ready is a compound metric that includes several distinct phases:

1. **Node provisioning** — Karpenter scheduling decision, cloud provider instance launch, and node registration with the cluster
2. **Pod startup** — Image pull, init container execution, main container start, and readiness probe satisfaction

Today, operators can observe that a pod took N seconds to become ready, but cannot determine whether the bottleneck was a 3-minute node launch or a 90-second image pull. This matters because the remediation is completely different: node provisioning latency points to instance type selection, launch template configuration, or AMI optimization, while pod startup latency points to image size, init container logic, or readiness probe tuning.

The same blind spot exists for shutdown. Graceful termination, preStop hooks, and volume detachment all contribute to pod removal time during scale-down or node disruption, but operators cannot distinguish between them without manual log correlation.

Without per-phase timing breakdowns, operators resort to ad-hoc log scraping, custom metrics pipelines, or manual timestamp correlation — none of which compose well across deployments or survive pod deletion.

## Recommended Solution

Introduce a controller that observes pod lifecycle events, computes per-phase timing breakdowns, and annotates Deployment (and ReplicaSet) objects with rolling Bayesian statistics. The design avoids storing raw event history by maintaining sufficient statistics that update incrementally.

### Architecture

```
Pod Events (watch)
       │
       ▼
┌─────────────────────┐
│  Timing Collector    │  ← Observes pod condition transitions, node events
│  (per-pod breakdown) │
└─────────┬───────────┘
          │
          ▼
┌─────────────────────┐
│  Bayesian Aggregator │  ← Updates sufficient statistics per deployment
│  (Normal-Gamma model)│
└─────────┬───────────┘
          │
          ▼
┌─────────────────────┐
│  Annotation Writer   │  ← Periodic (~hourly) annotation on Deployment/ReplicaSet
└─────────────────────┘
```

### Phase Breakdown

For each pod lifecycle, the controller computes these phases from existing Kubernetes timestamps and conditions:

**Startup phases:**

| Phase | Start Signal | End Signal |
|-------|-------------|------------|
| `node_provisioning` | NodeClaim created | Node registered (`Ready=True` first observed) |
| `image_pull` | Pod scheduled to node | All containers report image pulled (container state transition) |
| `init_containers` | First init container starts | Last init container completes |
| `container_start` | Main containers start | All containers running |
| `readiness` | Containers running | Pod condition `Ready=True` |

**Shutdown phases:**

| Phase | Start Signal | End Signal |
|-------|-------------|------------|
| `graceful_termination` | Pod `deletionTimestamp` set | preStop hooks complete |
| `container_stop` | SIGTERM sent | All containers exited |
| `volume_detach` | Containers exited | All volumes detached |

When a pod lands on an existing node (no Karpenter provisioning), `node_provisioning` is zero and omitted from the annotation.

### Bayesian Statistical Model

Rather than storing per-pod timing samples, the controller maintains a Normal-Gamma conjugate prior for each phase. This is the standard Bayesian approach for estimating the mean and variance of normally-distributed data with unknown parameters.

**Sufficient statistics per phase:**

```go
type PhaseStats struct {
    // Normal-Gamma parameters
    Mu    float64 `json:"mu"`    // Current mean estimate (seconds)
    Kappa float64 `json:"kappa"` // Pseudo-observation count for mean
    Alpha float64 `json:"alpha"` // Shape parameter for variance
    Beta  float64 `json:"beta"`  // Rate parameter for variance

    N     int     `json:"n"`     // Total observations
}
```

**Update rule** — on observing a new duration `x` for a phase:

```
kappa' = kappa + 1
mu'    = (kappa * mu + x) / kappa'
alpha' = alpha + 0.5
beta'  = beta + (kappa * (x - mu)^2) / (2 * kappa')
n'     = n + 1
```

**Prior initialization:** `mu=0, kappa=1, alpha=1, beta=1` (weakly informative). After ~5 observations the prior is washed out by data.

**Point estimates** exposed in annotations:
- Mean: `mu`
- Variance: `beta / (alpha - 1)` (for `alpha > 1`)
- 95% credible interval width derived from the posterior predictive t-distribution

This model is constant-space (5 floats per phase), updates in O(1), and naturally handles non-stationary workloads because recent observations shift the posterior.

### Annotation Schema

Annotations are written to the Deployment object (and optionally the current ReplicaSet) on a periodic cadence (~1 hour, configurable).

```yaml
metadata:
  annotations:
    karpenter.sh/lifecycle-timing: |
      {
        "version": "v1alpha1",
        "updated": "2026-03-26T19:00:00Z",
        "startup": {
          "node_provisioning": {"mu": 45.2, "sigma": 8.1, "n": 23},
          "image_pull":        {"mu": 12.4, "sigma": 3.2, "n": 47},
          "init_containers":   {"mu": 5.1,  "sigma": 1.0, "n": 47},
          "container_start":   {"mu": 1.2,  "sigma": 0.3, "n": 47},
          "readiness":         {"mu": 8.7,  "sigma": 2.5, "n": 47}
        },
        "shutdown": {
          "graceful_termination": {"mu": 15.3, "sigma": 4.2, "n": 31},
          "container_stop":      {"mu": 2.1,  "sigma": 0.8, "n": 31},
          "volume_detach":       {"mu": 3.4,  "sigma": 1.1, "n": 31}
        }
      }
```

The annotation exposes `mu` (mean seconds), `sigma` (standard deviation), and `n` (observation count) — the user-facing summary. The full sufficient statistics (`kappa`, `alpha`, `beta`) are stored in a ConfigMap or internal CRD status to avoid annotation bloat.

**Key:** `karpenter.sh/lifecycle-timing`
**Scope:** Deployment and active ReplicaSet
**Update cadence:** Configurable, default 1 hour

### Node Provisioning Breakout

The `node_provisioning` phase is further decomposed when Karpenter is the provisioner:

| Sub-phase | Start Signal | End Signal |
|-----------|-------------|------------|
| `scheduling_decision` | Pod marked unschedulable | NodeClaim created |
| `instance_launch` | NodeClaim created | Instance running (cloud provider) |
| `node_registration` | Instance running | Node `Ready=True` |

These sub-phases are tracked with the same Bayesian model and included in the annotation under `startup.node_provisioning_breakout` when available.

### Controller Design

The controller runs as part of the Karpenter controller manager:

1. **Pod watcher** — Watches pod condition transitions and records timestamps in an in-memory ring buffer (bounded, per-deployment, evicted after annotation write).
2. **NodeClaim correlator** — Joins pod scheduling events with NodeClaim lifecycle to attribute node provisioning time to specific pods.
3. **Annotation writer** — Periodic reconciliation loop that:
   - Computes Bayesian updates from buffered observations
   - Serializes summary stats to the Deployment annotation
   - Persists full sufficient statistics to a ConfigMap `karpenter-lifecycle-stats-<namespace>-<deployment>`
   - Clears the observation buffer

The controller is stateless across restarts — sufficient statistics are recovered from the ConfigMap. Observations buffered but not yet written are lost on crash (acceptable: they represent at most one annotation interval of data).

## Design Questions

### Why Bayesian updating instead of exponential moving average?

An EMA provides a point estimate of the mean but discards uncertainty information. The Normal-Gamma model provides both mean and variance estimates, plus a natural confidence measure (`n` / `kappa`). This lets operators distinguish "we think it's 45s but we've only seen 3 pods" from "we're confident it's 45s based on 200 pods." The computational cost is identical — both are O(1) per update with O(1) storage.

### Why annotate Deployments instead of emitting Prometheus metrics?

Annotations are zero-dependency: no metrics pipeline required, visible via `kubectl`, and travel with the object. Prometheus metrics are a natural future extension but require the operator to have a monitoring stack. The annotation approach works out of the box. A future iteration can expose the same data as Prometheus metrics for dashboarding.

### Why not store per-pod timing as Events?

Kubernetes Events are garbage-collected aggressively (default 1 hour) and are not designed for analytical queries. Storing timing data as Events would require an external system to aggregate them before they disappear. The Bayesian approach eliminates this dependency entirely.

### Why hourly annotation updates?

Frequent annotation writes create unnecessary API server load and etcd churn. Hourly updates balance freshness with cost. During rapid rollouts, the controller can optionally flush early when the observation buffer exceeds a threshold (e.g., 50 new observations).

## Out of Scope (Initial Implementation)

- **Prometheus metrics exporter** — Future iteration; annotations are the initial interface
- **Per-node-type breakdowns** — Tracking timing by instance type adds cardinality; deferred until annotation schema is validated
- **Historical trend analysis** — The Bayesian model captures current state, not time-series history; external tooling can snapshot annotations over time
- **Custom phase definitions** — Users cannot define arbitrary phases initially; the fixed phase set covers the common cases
- **Non-Deployment workloads** — StatefulSets, DaemonSets, and Jobs have different lifecycle semantics; deferred to a follow-up RFC
