# Pod Lifecycle Timing Observability

## Problem Statement

When a deployment rolls out new pods, operators experience startup latency but have no visibility into *where* that latency comes from. Total time-to-ready is a compound metric that includes several distinct phases:

1. **Node provisioning** — Karpenter scheduling decision, cloud provider instance launch, and node registration with the cluster
2. **Pod startup** — Image pull, init container execution, main container start, and readiness probe satisfaction

Today, operators can observe that a pod took N seconds to become ready, but cannot determine whether the bottleneck was a 3-minute node launch or a 90-second image pull. This matters because the remediation is completely different: node provisioning latency points to instance type selection, launch template configuration, or AMI optimization, while pod startup latency points to image size, init container logic, or readiness probe tuning.

The same blind spot exists for shutdown. Graceful termination, preStop hooks, and volume detachment all contribute to pod removal time during scale-down or node disruption, but operators cannot distinguish between them without manual log correlation.

Without per-phase timing breakdowns, operators resort to ad-hoc log scraping, custom metrics pipelines, or manual timestamp correlation — none of which compose well across deployments or survive pod deletion.

## Recommended Solution

Introduce a controller within the existing Karpenter controller manager that observes pod lifecycle events, computes per-phase timing breakdowns, and exposes them via Prometheus metrics (per-phase histograms) and Deployment annotations (end-to-end Bayesian summaries). The controller leverages the existing `state.Cluster` pod scheduling tracking, `operatorpkg/metrics` wrappers, and the standard controller-runtime reconciler pattern already used by `pkg/controllers/metrics/pod`.

The feature is gated behind a `PodLifecycleTiming=false` feature flag in the `FeatureGates` struct, following the same pattern as `NodeRepair`, `SpotToSpotConsolidation`, etc.

### Architecture

```
Pod Events (watch)                NodeClaim Events (watch)
       │                                  │
       └──────────┬───────────────────────┘
                  ▼
       ┌─────────────────────┐
       │  Timing Collector    │  ← Correlates pod conditions with NodeClaim lifecycle
       │  (per-pod breakdown) │     Extends existing pod metrics controller
       └─────────┬───────────┘
                 │
                 ├──────────────────────┐
                 ▼                      ▼
      ┌─────────────────────┐  ┌──────────────────┐
      │  Bayesian Aggregator │  │ Prometheus Metrics│  ← Per-phase histograms recorded
      │  (Normal-Gamma model)│  │ (per-observation) │     immediately on pod completion
      └─────────┬───────────┘  └──────────────────┘
                │
                ▼
      ┌─────────────────────┐
      │  Annotation Writer   │  ← Periodic (~hourly) end-to-end summary on Deployment
      └─────────────────────┘
```

### Design Decisions

- **Lives in karpenter-core** (`pkg/controllers/metrics/pod/lifecycle/`), not a provider repo. Node provisioning sub-phases use NodeClaim timestamps which are core abstractions.
- **Runs in existing controller manager** — extends the existing Karpenter ServiceAccount. RBAC additions are minimal: read access to Deployments/ReplicaSets (already available), write access to Deployment annotations and a stats ConfigMap.
- **Feature gated** as `PodLifecycleTiming=false` (alpha, opt-in).
- **Annotations carry end-to-end summaries only** (total startup/shutdown time with Bayesian stats). Per-phase detail lives in Prometheus metrics. This keeps annotations small and useful for future consolidation heuristics.
- **Per-phase metrics use separate metric names** (not a `phase` label), keeping cardinality at `namespace × deployment` per metric rather than multiplying by phase count.

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

**Prior initialization:** `mu=0, kappa=20, alpha=10, beta=10` (moderately informative). With `kappa=20`, approximately 20 real observations are needed before data dominates the prior. This prevents early outliers (e.g., a cold image pull or a one-off slow node launch) from permanently skewing estimates. The prior washes out gradually, giving the model stability during the initial observation window while still converging to accurate estimates as data accumulates.

**Point estimates** exposed in annotations:
- Mean: `mu`
- Variance: `beta / (alpha - 1)` (for `alpha > 1`)
- 95% credible interval width derived from the posterior predictive t-distribution

This model is constant-space (5 floats per phase), updates in O(1), and naturally handles non-stationary workloads because recent observations shift the posterior.

### Annotation Schema

Annotations carry end-to-end timing summaries only — per-phase breakdowns live in Prometheus. This keeps annotations compact and useful as inputs for future consolidation heuristics.

Annotations are written to the Deployment object on a periodic cadence (~1 hour, configurable).

```yaml
metadata:
  annotations:
    karpenter.sh/lifecycle-timing: |
      {
        "version": "v1alpha1",
        "updated": "2026-03-26T19:00:00Z",
        "startup": {"mu": 72.6, "sigma": 12.3, "n": 47},
        "shutdown": {"mu": 20.8, "sigma": 5.1, "n": 31}
      }
```

The annotation exposes `mu` (mean seconds), `sigma` (standard deviation), and `n` (observation count) for total startup and shutdown time. The full sufficient statistics (`kappa`, `alpha`, `beta`) are stored in a ConfigMap to avoid annotation bloat.

**Key:** `karpenter.sh/lifecycle-timing`
**Scope:** Deployment
**Update cadence:** Configurable, default 1 hour

#### Sufficient Statistics Storage: ConfigMap vs CRD

| Approach | Pros | Cons |
|----------|------|------|
| **ConfigMap** (`karpenter-lifecycle-stats-<ns>-<deploy>`) | No CRD registration needed, simple RBAC, works immediately | One ConfigMap per deployment — could be many objects; no schema validation; namespace-scoped naming collisions possible |
| **CRD** (`LifecycleTimingStats`) | Schema validation, single list query, natural kubectl experience, versioned API | Requires CRD registration + code generation, heavier initial implementation, migration path if schema changes |

**Recommendation: Start with ConfigMap.** The data is internal controller state, not user-facing API. ConfigMap count scales linearly with deployments but each is tiny (~500 bytes). If the feature graduates to beta and the number of tracked deployments becomes a concern, migrating to a CRD is straightforward since the data format is the same — only the storage backend changes.

### Node Provisioning Breakout

The `node_provisioning` phase is further decomposed when Karpenter is the provisioner:

| Sub-phase | Start Signal | End Signal |
|-----------|-------------|------------|
| `scheduling_decision` | Pod marked unschedulable | NodeClaim created |
| `instance_launch` | NodeClaim created | Instance running (cloud provider) |
| `node_registration` | Instance running | Node `Ready=True` |

These sub-phases are tracked with the same Bayesian model and included in the annotation under `startup.node_provisioning_breakout` when available.

### Prometheus Metrics

The controller exposes per-phase timing as individual Prometheus histogram metrics, following the existing pattern in `pkg/controllers/metrics/pod` which uses `operatorpkg/metrics` wrappers and `controller-runtime/pkg/metrics` registry.

Each phase gets its own metric name rather than using a `phase` label. This keeps cardinality at `namespace × deployment` per metric and avoids the combinatorial explosion of a phase label dimension.

**Startup phase histograms** (labeled by `namespace`, `deployment`):

```
karpenter_pods_lifecycle_node_provisioning_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_image_pull_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_init_containers_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_container_start_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_readiness_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_startup_total_duration_seconds{namespace, deployment}
```

**Shutdown phase histograms:**

```
karpenter_pods_lifecycle_graceful_termination_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_container_stop_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_volume_detach_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_shutdown_total_duration_seconds{namespace, deployment}
```

**Node provisioning sub-phase histograms** (when Karpenter provisions the node):

```
karpenter_pods_lifecycle_scheduling_decision_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_instance_launch_duration_seconds{namespace, deployment}
karpenter_pods_lifecycle_node_registration_duration_seconds{namespace, deployment}
```

All histograms use `metrics.DurationBuckets()` for consistency with existing Karpenter metrics. Metrics are registered via `opmetrics.NewPrometheusHistogram()` following the established pattern.

### Controller Design

The controller lives at `pkg/controllers/metrics/pod/lifecycle/` and runs as part of the existing Karpenter controller manager, gated behind `PodLifecycleTiming` feature flag.

1. **Pod watcher** — Reconciles on pod condition transitions. Uses `state.Cluster` to access `PodSchedulingSuccessTime` and `PodSchedulingDecisionTime` (already tracked). Records timestamps in an in-memory ring buffer (bounded, per-deployment, evicted after annotation write).
2. **NodeClaim correlator** — Joins pod scheduling events with NodeClaim lifecycle timestamps (`Created`, `Launched`, `Registered` conditions on NodeClaim status) to compute provisioning sub-phases.
3. **Metrics recorder** — Records each per-phase duration as a Prometheus histogram observation immediately upon pod lifecycle completion. Uses `opmetrics.NewPrometheusHistogram()` registered against `controller-runtime/pkg/metrics.Registry`.
4. **Annotation writer** (PR 2) — Periodic reconciliation loop that:
   - Computes Bayesian updates from buffered observations
   - Serializes end-to-end summary stats to the Deployment annotation
   - Persists full sufficient statistics to a ConfigMap `karpenter-lifecycle-stats-<namespace>-<deployment>`
   - Clears the observation buffer

The controller is stateless across restarts — sufficient statistics are recovered from the ConfigMap. Observations buffered but not yet written are lost on crash (acceptable: they represent at most one annotation interval of data). Prometheus histogram data is similarly lost on restart, but this is standard behavior for in-process metrics.

## Design Questions

### Why Bayesian updating instead of exponential moving average?

An EMA provides a point estimate of the mean but discards uncertainty information. The Normal-Gamma model provides both mean and variance estimates, plus a natural confidence measure (`n` / `kappa`). This lets operators distinguish "we think it's 45s but we've only seen 3 pods" from "we're confident it's 45s based on 200 pods." The computational cost is identical — both are O(1) per update with O(1) storage.

### Why both annotations and Prometheus metrics?

Annotations are zero-dependency: no metrics pipeline required, visible via `kubectl`, and travel with the object. They serve as the portable, always-available interface. Prometheus metrics complement this by enabling dashboarding, alerting, and percentile queries (`p99 startup latency`) that annotations cannot support. Since Karpenter already vendors the Prometheus client library, the incremental cost of emitting histograms and gauges is minimal. Operators without a monitoring stack still get full value from annotations alone.

### Why not store per-pod timing as Events?

Kubernetes Events are garbage-collected aggressively (default 1 hour) and are not designed for analytical queries. Storing timing data as Events would require an external system to aggregate them before they disappear. The Bayesian approach eliminates this dependency entirely.

### Why hourly annotation updates?

Frequent annotation writes create unnecessary API server load and etcd churn. Hourly updates balance freshness with cost. During rapid rollouts, the controller can optionally flush early when the observation buffer exceeds a threshold (e.g., 50 new observations).

## Out of Scope (Initial Implementation)

- **Per-node-type breakdowns** — Tracking timing by instance type adds cardinality; deferred until annotation schema is validated
- **Historical trend analysis** — The Bayesian model captures current state, not time-series history; external tooling can snapshot annotations over time
- **Custom phase definitions** — Users cannot define arbitrary phases initially; the fixed phase set covers the common cases
- **Non-Deployment workloads** — StatefulSets, DaemonSets, and Jobs have different lifecycle semantics; deferred to a follow-up RFC
