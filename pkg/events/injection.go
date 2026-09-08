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

package events

import "context"

// recorderKeyType is the context key type for an events.Recorder attached to
// a request-scoped context. A named type prevents key collisions with other
// context keys stored under interface{}.
type recorderKeyType struct{}

var recorderKey = recorderKeyType{}

// WithRecorder attaches r to ctx. A nil Recorder is stored as-is so
// FromContext can return it and callers can treat the absence of a Recorder
// uniformly with the absence of a wired-up controller.
func WithRecorder(ctx context.Context, r Recorder) context.Context {
	return context.WithValue(ctx, recorderKey, r)
}

// FromContext returns the events.Recorder attached to ctx by WithRecorder,
// or nil if no Recorder was attached. Call sites MUST nil-check before
// publishing so a mis-wired ctx logs but does not panic.
func FromContext(ctx context.Context) Recorder {
	r, _ := ctx.Value(recorderKey).(Recorder)
	return r
}
