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

package grace_test

import (
	"sort"
	"testing"
	"time"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/grace"
)

func TestTracker_HoldsBackUntilGraceElapses(t *testing.T) {
	now := time.Unix(1000, 0)
	tr := grace.New(5 * time.Minute)
	tr.Now = func() time.Time { return now }

	// First observation: still pending.
	if got := tr.Step([]string{"a"}); len(got) != 0 {
		t.Errorf("first step: want empty, got %v", got)
	}
	if tr.Pending() != 1 {
		t.Errorf("Pending after first step: want 1, got %d", tr.Pending())
	}

	// Re-observed within grace: still pending.
	now = now.Add(2 * time.Minute)
	if got := tr.Step([]string{"a"}); len(got) != 0 {
		t.Errorf("within grace: want empty, got %v", got)
	}

	// Past grace boundary: emerges.
	now = now.Add(4 * time.Minute)
	got := tr.Step([]string{"a"})
	if !equal(got, []string{"a"}) {
		t.Errorf("past grace: want [a], got %v", got)
	}
	if tr.Pending() != 0 {
		t.Errorf("Pending after emergence: want 0, got %d", tr.Pending())
	}
}

func TestTracker_DisappearedKeyResets(t *testing.T) {
	now := time.Unix(1000, 0)
	tr := grace.New(5 * time.Minute)
	tr.Now = func() time.Time { return now }

	tr.Step([]string{"a"})
	now = now.Add(4 * time.Minute)
	tr.Step([]string{"a"}) // still pending

	// Disappear.
	now = now.Add(1 * time.Minute)
	if got := tr.Step([]string{}); len(got) != 0 {
		t.Errorf("after disappear: want empty, got %v", got)
	}

	// Reappear: timer resets.
	if got := tr.Step([]string{"a"}); len(got) != 0 {
		t.Errorf("immediately after reappear: want empty, got %v", got)
	}
	now = now.Add(4 * time.Minute) // 4m past reappear, still under 5m
	if got := tr.Step([]string{"a"}); len(got) != 0 {
		t.Errorf("4m past reappear: want empty (timer reset), got %v", got)
	}
	now = now.Add(2 * time.Minute) // 6m past reappear
	if got := tr.Step([]string{"a"}); !equal(got, []string{"a"}) {
		t.Errorf("6m past reappear: want [a], got %v", got)
	}
}

func TestTracker_ZeroPeriodPassesThrough(t *testing.T) {
	tr := grace.New(0)
	got := tr.Step([]string{"a", "b"})
	sort.Strings(got)
	if !equal(got, []string{"a", "b"}) {
		t.Errorf("zero period: want [a b], got %v", got)
	}
}

func TestTracker_NegativePeriodPassesThrough(t *testing.T) {
	tr := grace.New(-1 * time.Second)
	got := tr.Step([]string{"x"})
	if !equal(got, []string{"x"}) {
		t.Errorf("negative period: want [x], got %v", got)
	}
}

func TestTracker_MultipleKeysIndependentTimers(t *testing.T) {
	now := time.Unix(0, 0)
	tr := grace.New(5 * time.Minute)
	tr.Now = func() time.Time { return now }

	tr.Step([]string{"a"})
	now = now.Add(3 * time.Minute)
	tr.Step([]string{"a", "b"}) // b just appeared
	now = now.Add(3 * time.Minute)

	// At t=6m: a has been around 6m (past 5m grace), b has been
	// around 3m (still under).
	got := tr.Step([]string{"a", "b"})
	sort.Strings(got)
	if !equal(got, []string{"a"}) {
		t.Errorf("6m: want [a], got %v", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
