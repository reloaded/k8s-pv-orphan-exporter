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

package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
)

// TestPVEventHandlers_DeletedFinalStateUnknownTombstone covers the
// watch-resync edge case: the informer occasionally delivers a Delete
// not with the original *PersistentVolume but with a
// cache.DeletedFinalStateUnknown wrapping the last observed object
// (happens after a missed delete event during a watch interruption).
//
// fake.NewClientset can't reliably synthesise this — its informer
// always delivers the typed object on Delete — so the tombstone path
// has to be exercised by invoking the handler func directly.
func TestPVEventHandlers_DeletedFinalStateUnknownTombstone(t *testing.T) {
	inv := inventory.NewInventory()
	cfg := inventory.Config{}
	h := pvEventHandlers(inv, cfg)

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-x"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/data/pv-x"},
			},
		},
	}

	// Seed the inventory the way a real Add event would.
	h.AddFunc(pv)
	if got := len(inv.Snapshot()); got != 1 {
		t.Fatalf("after AddFunc: inventory size want 1, got %d", got)
	}

	// Now deliver a Delete wrapping the PV in a tombstone — the path
	// the informer takes on resync after a missed watch event.
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "pv-x",
		Obj: pv,
	}
	h.DeleteFunc(tombstone)

	if got := len(inv.Snapshot()); got != 0 {
		t.Errorf("after DeleteFunc(tombstone): inventory size want 0, got %d", got)
	}
}

// TestPVEventHandlers_DeleteFunc_NonPVObjectIgnored guards against a
// panic if a future client-go version delivers something unexpected
// — the type assertions in DeleteFunc must fail safely.
func TestPVEventHandlers_DeleteFunc_NonPVObjectIgnored(t *testing.T) {
	inv := inventory.NewInventory()
	h := pvEventHandlers(inv, inventory.Config{})

	// Bare struct: not *PersistentVolume, not DeletedFinalStateUnknown.
	h.DeleteFunc(struct{}{})

	// Tombstone wrapping a non-PV (also weird; should still be safe).
	h.DeleteFunc(cache.DeletedFinalStateUnknown{Key: "x", Obj: "not a pv"})

	if got := len(inv.Snapshot()); got != 0 {
		t.Errorf("inventory unexpectedly mutated: %d", got)
	}
}
