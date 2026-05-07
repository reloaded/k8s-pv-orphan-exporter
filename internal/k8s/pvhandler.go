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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
)

// RegisterPVHandler hooks the PersistentVolume informer up to the
// supplied inventory: every Add/Update event runs the PV through
// inventory.FromPV and writes the resulting PVRef; every Delete
// removes it.
//
// The factory is not started here — the caller is responsible for
// factory.Start and factory.WaitForCacheSync so the lifecycle
// remains visible at the call site.
func RegisterPVHandler(factory informers.SharedInformerFactory, inv *inventory.Inventory) error {
	informer := factory.Core().V1().PersistentVolumes().Informer()
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if pv, ok := obj.(*corev1.PersistentVolume); ok {
				inv.Set(inventory.FromPV(pv))
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if pv, ok := newObj.(*corev1.PersistentVolume); ok {
				inv.Set(inventory.FromPV(pv))
			}
		},
		DeleteFunc: func(obj interface{}) {
			// On final state-unknown, the obj is a
			// DeletedFinalStateUnknown wrapping the last
			// observed PV. Unwrap so we still drop the right
			// inventory entry.
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			if pv, ok := obj.(*corev1.PersistentVolume); ok {
				inv.Delete(pv.Name)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("register PV handler: %w", err)
	}
	return nil
}
