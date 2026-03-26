/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	appsv1 "k8s.io/api/apps/v1"

	"sigs.k8s.io/karpenter/pkg/operator/injection"
)

const (
	lifecycleTimingAnnotation = "karpenter.sh/lifecycle-timing"
	statsConfigMapPrefix      = "karpenter-lifecycle-stats-"
	defaultWriteInterval      = time.Hour
	maxBufferSize             = 100
)

// deploymentKey uniquely identifies a deployment.
type deploymentKey struct {
	Namespace string
	Name      string
}

// observation holds a single startup or shutdown duration sample.
type observation struct {
	Startup  float64
	Shutdown float64
	HasStart bool
	HasShut  bool
}

// deploymentStats holds the Bayesian stats for one deployment.
type deploymentStats struct {
	Startup  PhaseStats `json:"startup"`
	Shutdown PhaseStats `json:"shutdown"`
}

// Aggregator collects observations and maintains Bayesian stats per deployment.
type Aggregator struct {
	mu      sync.Mutex
	stats   map[deploymentKey]*deploymentStats
	buffers map[deploymentKey][]observation
}

func NewAggregator() *Aggregator {
	return &Aggregator{
		stats:   make(map[deploymentKey]*deploymentStats),
		buffers: make(map[deploymentKey][]observation),
	}
}

// RecordStartup buffers a startup observation.
func (a *Aggregator) RecordStartup(namespace, deployment string, duration float64) {
	if deployment == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := deploymentKey{Namespace: namespace, Name: deployment}
	buf := a.buffers[key]
	if len(buf) >= maxBufferSize {
		buf = buf[1:]
	}
	a.buffers[key] = append(buf, observation{Startup: duration, HasStart: true})
}

// RecordShutdown buffers a shutdown observation.
func (a *Aggregator) RecordShutdown(namespace, deployment string, duration float64) {
	if deployment == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := deploymentKey{Namespace: namespace, Name: deployment}
	buf := a.buffers[key]
	if len(buf) >= maxBufferSize {
		buf = buf[1:]
	}
	a.buffers[key] = append(buf, observation{Shutdown: duration, HasShut: true})
}

// FlushAndUpdate applies buffered observations to Bayesian stats, clears the buffer,
// and returns the updated stats.
func (a *Aggregator) FlushAndUpdate(key deploymentKey) *deploymentStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	buf := a.buffers[key]
	if len(buf) == 0 {
		if s, ok := a.stats[key]; ok {
			cp := *s
			return &cp
		}
		return nil
	}
	s, ok := a.stats[key]
	if !ok {
		s = &deploymentStats{Startup: NewPhaseStats(), Shutdown: NewPhaseStats()}
		a.stats[key] = s
	}
	for _, obs := range buf {
		if obs.HasStart {
			s.Startup.Update(obs.Startup)
		}
		if obs.HasShut {
			s.Shutdown.Update(obs.Shutdown)
		}
	}
	a.buffers[key] = nil
	cp := *s
	return &cp
}

// DirtyKeys returns deployment keys with buffered observations.
func (a *Aggregator) DirtyKeys() []deploymentKey {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := make([]deploymentKey, 0, len(a.buffers))
	for k, buf := range a.buffers {
		if len(buf) > 0 {
			keys = append(keys, k)
		}
	}
	return keys
}

// RestoreStats loads previously persisted stats for a deployment.
func (a *Aggregator) RestoreStats(key deploymentKey, s *deploymentStats) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stats[key] = s
}

// annotationPayload is the JSON written to the Deployment annotation.
type annotationPayload struct {
	Version  string       `json:"version"`
	Updated  string       `json:"updated"`
	Startup  phaseSummary `json:"startup"`
	Shutdown phaseSummary `json:"shutdown"`
}

type phaseSummary struct {
	Mu    float64 `json:"mu"`
	Sigma float64 `json:"sigma"`
	N     int     `json:"n"`
}

// AnnotationWriter periodically flushes Bayesian stats to Deployment annotations
// and persists sufficient statistics to ConfigMaps.
type AnnotationWriter struct {
	kubeClient    client.Client
	clk           clock.Clock
	aggregator    *Aggregator
	writeInterval time.Duration
	recovered     bool
}

func NewAnnotationWriter(kubeClient client.Client, clk clock.Clock, aggregator *Aggregator) *AnnotationWriter {
	return &AnnotationWriter{
		kubeClient:    kubeClient,
		clk:           clk,
		aggregator:    aggregator,
		writeInterval: defaultWriteInterval,
	}
}

func (w *AnnotationWriter) Reconcile(ctx context.Context) (reconciler.Result, error) {
	ctx = injection.WithControllerName(ctx, w.Name())

	// Recover persisted stats on first reconcile
	if !w.recovered {
		w.recovered = true
		if err := w.recoverStats(ctx); err != nil {
			return reconciler.Result{}, err
		}
	}

	for _, key := range w.aggregator.DirtyKeys() {
		stats := w.aggregator.FlushAndUpdate(key)
		if stats == nil {
			continue
		}
		if err := w.persistConfigMap(ctx, key, stats); err != nil {
			return reconciler.Result{}, err
		}
		if err := w.writeAnnotation(ctx, key, stats); err != nil {
			if !errors.IsNotFound(err) {
				return reconciler.Result{}, err
			}
		}
	}
	return reconciler.Result{RequeueAfter: w.writeInterval}, nil
}

func (w *AnnotationWriter) persistConfigMap(ctx context.Context, key deploymentKey, stats *deploymentStats) error {
	data, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	cmName := configMapName(key)
	cm := &corev1.ConfigMap{}
	err = w.kubeClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: key.Namespace}, cm)
	if errors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: key.Namespace,
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "karpenter", "karpenter.sh/lifecycle-stats": "true"},
			},
			Data: map[string]string{"stats": string(data)},
		}
		return client.IgnoreAlreadyExists(w.kubeClient.Create(ctx, cm))
	}
	if err != nil {
		return err
	}
	cm.Data = map[string]string{"stats": string(data)}
	return w.kubeClient.Update(ctx, cm)
}

func (w *AnnotationWriter) writeAnnotation(ctx context.Context, key deploymentKey, stats *deploymentStats) error {
	deploy := &appsv1.Deployment{}
	if err := w.kubeClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, deploy); err != nil {
		return err
	}
	payload := annotationPayload{
		Version: "v1alpha1",
		Updated: w.clk.Now().UTC().Format(time.RFC3339),
		Startup: phaseSummary{Mu: stats.Startup.Mu, Sigma: stats.Startup.Sigma(), N: stats.Startup.N},
		Shutdown: phaseSummary{Mu: stats.Shutdown.Mu, Sigma: stats.Shutdown.Sigma(), N: stats.Shutdown.N},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	patch := client.MergeFrom(deploy.DeepCopy())
	if deploy.Annotations == nil {
		deploy.Annotations = map[string]string{}
	}
	deploy.Annotations[lifecycleTimingAnnotation] = string(data)
	return w.kubeClient.Patch(ctx, deploy, patch)
}

func (w *AnnotationWriter) recoverStats(ctx context.Context) error {
	cmList := &corev1.ConfigMapList{}
	if err := w.kubeClient.List(ctx, cmList,
		client.MatchingLabels{"karpenter.sh/lifecycle-stats": "true"},
	); err != nil {
		return err
	}
	for _, cm := range cmList.Items {
		raw, ok := cm.Data["stats"]
		if !ok {
			continue
		}
		var s deploymentStats
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			continue
		}
		key, ok := parseConfigMapName(cm.Name, cm.Namespace)
		if !ok {
			continue
		}
		w.aggregator.RestoreStats(key, &s)
	}
	return nil
}

func configMapName(key deploymentKey) string {
	return fmt.Sprintf("%s%s-%s", statsConfigMapPrefix, key.Namespace, key.Name)
}

func parseConfigMapName(name, namespace string) (deploymentKey, bool) {
	prefix := statsConfigMapPrefix + namespace + "-"
	if len(name) <= len(prefix) {
		return deploymentKey{}, false
	}
	return deploymentKey{Namespace: namespace, Name: name[len(prefix):]}, true
}

func (w *AnnotationWriter) Name() string {
	return "metrics.pod.lifecycle.annotationwriter"
}

func (w *AnnotationWriter) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(w.Name()).
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(w))
}
