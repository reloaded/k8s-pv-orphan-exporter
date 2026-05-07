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

package inventory_test

import (
	"sort"
	"sync"
	"testing"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
)

func TestInventory_SetDeleteSnapshot(t *testing.T) {
	inv := inventory.NewInventory()

	if got := inv.Snapshot(); len(got) != 0 {
		t.Fatalf("fresh Snapshot len: want 0, got %d", len(got))
	}

	inv.Set(inventory.PVRef{Name: "pv-a", Backend: inventory.BackendLocalPath})
	inv.Set(inventory.PVRef{Name: "pv-b", Backend: inventory.BackendNFS})
	inv.Set(inventory.PVRef{Name: "pv-a", Backend: inventory.BackendLocalPath, StorageClass: "fast"}) // overwrite

	got := inv.Snapshot()
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	if len(got) != 2 {
		t.Fatalf("Snapshot len: want 2, got %d (%+v)", len(got), got)
	}
	if got[0].Name != "pv-a" || got[0].StorageClass != "fast" {
		t.Errorf("pv-a not overwritten: %+v", got[0])
	}
	if got[1].Name != "pv-b" {
		t.Errorf("pv-b missing: %+v", got[1])
	}

	inv.Delete("pv-a")
	got = inv.Snapshot()
	if len(got) != 1 || got[0].Name != "pv-b" {
		t.Errorf("after Delete: want [pv-b], got %+v", got)
	}

	inv.Delete("nonexistent") // no-op, must not panic
}

func TestInventory_ZeroNameDropped(t *testing.T) {
	inv := inventory.NewInventory()
	inv.Set(inventory.PVRef{}) // zero Name
	if got := inv.Snapshot(); len(got) != 0 {
		t.Errorf("zero-Name ref leaked: %+v", got)
	}
}

func TestInventory_SizeByBackend(t *testing.T) {
	inv := inventory.NewInventory()
	inv.Set(inventory.PVRef{Name: "a", Backend: inventory.BackendLocalPath})
	inv.Set(inventory.PVRef{Name: "b", Backend: inventory.BackendLocalPath})
	inv.Set(inventory.PVRef{Name: "c", Backend: inventory.BackendNFS})
	inv.Set(inventory.PVRef{Name: "d", Backend: inventory.BackendUnknown})

	sizes := inv.SizeByBackend()
	if sizes[inventory.BackendLocalPath] != 2 {
		t.Errorf("local-path: want 2, got %d", sizes[inventory.BackendLocalPath])
	}
	if sizes[inventory.BackendNFS] != 1 {
		t.Errorf("nfs: want 1, got %d", sizes[inventory.BackendNFS])
	}
	if sizes[inventory.BackendUnknown] != 1 {
		t.Errorf("unknown: want 1, got %d", sizes[inventory.BackendUnknown])
	}
}

func TestInventory_ConcurrentAccess(t *testing.T) {
	inv := inventory.NewInventory()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			inv.Set(inventory.PVRef{Name: "pv-w", Backend: inventory.BackendLocalPath})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			inv.Delete("pv-w")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = inv.Snapshot()
			_ = inv.SizeByBackend()
		}
	}()
	wg.Wait()
}
