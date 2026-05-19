// Copyright 2026 Jason Harris
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package grace implements the dangling/orphaned grace-period gating
// described in docs/design.md §5.2.
//
// Dangling and orphaned candidates are noisy at provisioning time —
// a PV exists in the API for a few seconds before its directory
// shows up, and a directory is created before its PV's claimRef
// settles. The grace period suppresses these transient signals: an
// item must be observed continuously for at least Period before the
// scan exposes it.
//
// A Tracker is per-kind (one for dangling, one for orphaned) so the
// caller doesn't have to namespace keys across categories.
package grace

import (
	"sync"
	"time"
)

// Tracker remembers the first time each key was observed and which
// keys were present in the most recent step. Keys absent from a
// step are forgotten — the timer resets if the candidate
// reappears.
type Tracker struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	period time.Duration
	// Now is the clock the Tracker uses; tests override it with a
	// deterministic stub. Defaults to time.Now.
	Now func() time.Time
}

// New returns a Tracker with the given grace period. A zero or
// negative period causes Step to return its input unchanged — the
// "grace disabled" mode.
func New(period time.Duration) *Tracker {
	return &Tracker{
		seen:   make(map[string]time.Time),
		period: period,
		Now:    time.Now,
	}
}

// Step records the current set of observed keys and returns the
// subset whose observation has been continuous for at least the
// configured grace period. Keys absent from the input are forgotten;
// keys present but newer than the period are held back.
//
// Step is safe to call concurrently with itself but each call
// represents one scan cycle — calling it more often than the scan
// loop runs effectively shortens the grace period.
func (t *Tracker) Step(keys []string) []string {
	if t.period <= 0 {
		out := make([]string, len(keys))
		copy(out, keys)
		return out
	}

	now := t.Now()
	current := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		current[k] = struct{}{}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for k := range t.seen {
		if _, ok := current[k]; !ok {
			delete(t.seen, k)
		}
	}
	for k := range current {
		if _, ok := t.seen[k]; !ok {
			t.seen[k] = now
		}
	}

	out := make([]string, 0, len(t.seen))
	for k, firstSeen := range t.seen {
		if !now.Before(firstSeen.Add(t.period)) {
			out = append(out, k)
		}
	}
	return out
}

// Pending returns the number of keys currently observed but not yet
// past the grace period. Useful for an internal debug metric;
// design.md §5.2 deliberately does not expose pending counts to
// Prometheus, so the exporter doesn't surface this directly.
func (t *Tracker) Pending() int {
	if t.period <= 0 {
		return 0
	}
	now := t.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, firstSeen := range t.seen {
		if now.Before(firstSeen.Add(t.period)) {
			n++
		}
	}
	return n
}
