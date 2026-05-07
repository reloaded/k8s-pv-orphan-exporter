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
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
)

// NewInformerFactory wraps client-go's SharedInformerFactory with a
// resync period. Phase 1 only constructs the factory; Phase 2 wires
// the PersistentVolume informer into the inventory.
func NewInformerFactory(cs kubernetes.Interface, resync time.Duration) informers.SharedInformerFactory {
	return informers.NewSharedInformerFactory(cs, resync)
}
