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

//go:build integration

// Package integration exercises the local-path scanner end-to-end:
// PVs are pushed through a fake informer into the inventory,
// directories are created/removed on a real temp filesystem, and
// the diff engine's output is asserted.
//
// This test file is only compiled with `go test -tags=integration`,
// so it runs in nightly CI but not on every PR. A future kind-based
// variant (design.md §13) will exercise the same surface against a
// real cluster + local-path-provisioner.
package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/diff"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/k8s"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner/localpath"
)

const nodeName = "test-node-1"

// TestLocalPathPipeline drives the full Phase 2 pipeline against a
// fake clientset and a real temp directory: a PV with no backing
// directory must surface as dangling, a directory with no PV must
// surface as orphaned, and a normal pair must produce neither.
func TestLocalPathPipeline(t *testing.T) {
	root := t.TempDir()

	livePVDir := filepath.Join(root, "pvc-live_default_demo")
	if err := os.Mkdir(livePVDir, 0o755); err != nil {
		t.Fatalf("mkdir live: %v", err)
	}
	strayDir := filepath.Join(root, "pvc-stray_kube-system_other")
	if err := os.Mkdir(strayDir, 0o755); err != nil {
		t.Fatalf("mkdir stray: %v", err)
	}

	livePV := makeLocalPV("pv-live", livePVDir, nodeName)
	danglingPV := makeLocalPV("pv-dangling", filepath.Join(root, "pvc-missing_default_x"), nodeName)

	cs := fake.NewClientset(livePV, danglingPV)
	factory := informers.NewSharedInformerFactory(cs, 0)
	inv := inventory.NewInventory()

	if err := k8s.RegisterPVHandler(factory, inv, inventory.Config{}); err != nil {
		t.Fatalf("RegisterPVHandler: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(ctx.Done(),
		factory.Core().V1().PersistentVolumes().Informer().HasSynced) {
		t.Fatal("informer cache failed to sync")
	}

	if size := len(inv.Snapshot()); size != 2 {
		t.Fatalf("inventory size: want 2, got %d", size)
	}

	s := localpath.New(localpath.Config{
		StorageRoots: []string{root},
		NodeName:     nodeName,
		MaxDepth:     1,
	})
	scanResult, err := s.Scan(ctx)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	result := diff.Compute(inv.Snapshot(), scanResult)

	if names := danglingNames(result.Dangling); !equal(names, []string{"pv-dangling"}) {
		t.Errorf("dangling: want [pv-dangling], got %v", names)
	}
	if paths := orphanPaths(result.Orphaned); !equal(paths, []string{strayDir}) {
		t.Errorf("orphaned: want [%q], got %v", strayDir, paths)
	}
}

// TestLocalPathPipeline_PVAddedAfterScan verifies the watch path:
// scan now, add a PV, scan again, the new PV's existing directory
// no longer appears as orphaned.
func TestLocalPathPipeline_PVAddedAfterScan(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pvc-late_default_demo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cs := fake.NewClientset()
	factory := informers.NewSharedInformerFactory(cs, 0)
	inv := inventory.NewInventory()
	if err := k8s.RegisterPVHandler(factory, inv, inventory.Config{}); err != nil {
		t.Fatalf("RegisterPVHandler: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(ctx.Done(),
		factory.Core().V1().PersistentVolumes().Informer().HasSynced) {
		t.Fatal("informer cache failed to sync")
	}

	s := localpath.New(localpath.Config{
		StorageRoots: []string{root},
		NodeName:     nodeName,
		MaxDepth:     1,
	})

	scanResult, err := s.Scan(ctx)
	if err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	first := diff.Compute(inv.Snapshot(), scanResult)
	if len(first.Orphaned) != 1 {
		t.Fatalf("first scan orphan count: want 1, got %d", len(first.Orphaned))
	}

	pv := makeLocalPV("pv-late", dir, nodeName)
	if _, err := cs.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create PV: %v", err)
	}

	// Wait for the inventory to observe the new PV.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(inv.Snapshot()) < 1 {
		time.Sleep(50 * time.Millisecond)
	}
	if len(inv.Snapshot()) != 1 {
		t.Fatalf("inventory size after Create: want 1, got %d", len(inv.Snapshot()))
	}

	scanResult2, err := s.Scan(ctx)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	second := diff.Compute(inv.Snapshot(), scanResult2)
	if len(second.Orphaned) != 0 {
		t.Errorf("second scan orphan count: want 0, got %d (%+v)", len(second.Orphaned), second.Orphaned)
	}
	if len(second.Dangling) != 0 {
		t.Errorf("second scan dangling count: want 0, got %d (%+v)", len(second.Dangling), second.Dangling)
	}
}

// TestLocalPathPipeline_PVUpdated_RewritesExpectedPaths drives the
// Update event on the informer: a live PV's spec.local.path changes,
// the inventory's expected path must flip cleanly so the OLD
// directory becomes an orphan and the NEW (still-missing) one
// becomes dangling on the next scan.
func TestLocalPathPipeline_PVUpdated_RewritesExpectedPaths(t *testing.T) {
	root := t.TempDir()
	dirA := filepath.Join(root, "pvc-A_default_demo")
	if err := os.Mkdir(dirA, 0o755); err != nil {
		t.Fatalf("mkdir A: %v", err)
	}
	dirB := filepath.Join(root, "pvc-B_default_demo") // PV will be re-pointed here; left absent on disk

	pv := makeLocalPV("pv-mutable", dirA, nodeName)

	cs := fake.NewClientset(pv)
	factory := informers.NewSharedInformerFactory(cs, 0)
	inv := inventory.NewInventory()
	if err := k8s.RegisterPVHandler(factory, inv, inventory.Config{}); err != nil {
		t.Fatalf("RegisterPVHandler: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(ctx.Done(),
		factory.Core().V1().PersistentVolumes().Informer().HasSynced) {
		t.Fatal("informer cache failed to sync")
	}

	s := localpath.New(localpath.Config{
		StorageRoots: []string{root},
		NodeName:     nodeName,
		MaxDepth:     1,
	})

	scan1, err := s.Scan(ctx)
	if err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	first := diff.Compute(inv.Snapshot(), scan1)
	if len(first.Dangling)+len(first.Orphaned) != 0 {
		t.Fatalf("scan 1: want no dangling/orphans, got dangling=%v orphans=%v",
			danglingNames(first.Dangling), orphanPaths(first.Orphaned))
	}

	// Update the PV's path A -> B. Fetch first so we send back the
	// tracker's current resourceVersion (defensive; fake's lossless
	// here, but kind would care).
	current, err := cs.CoreV1().PersistentVolumes().Get(ctx, "pv-mutable", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get pv-mutable: %v", err)
	}
	current.Spec.Local.Path = dirB
	if _, err := cs.CoreV1().PersistentVolumes().Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update pv-mutable: %v", err)
	}

	// Wait for the inventory to reflect the new ExpectedPath.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := inv.Snapshot()
		if len(snap) == 1 && len(snap[0].ExpectedPaths) == 1 && snap[0].ExpectedPaths[0].Path == dirB {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap := inv.Snapshot(); len(snap) != 1 || snap[0].ExpectedPaths[0].Path != dirB {
		t.Fatalf("inventory did not observe Update: snap=%+v", snap)
	}

	scan2, err := s.Scan(ctx)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	second := diff.Compute(inv.Snapshot(), scan2)

	if names := danglingNames(second.Dangling); !equal(names, []string{"pv-mutable"}) {
		t.Errorf("scan 2 dangling: want [pv-mutable], got %v", names)
	}
	if paths := orphanPaths(second.Orphaned); !equal(paths, []string{dirA}) {
		t.Errorf("scan 2 orphaned: want [%q], got %v", dirA, paths)
	}
}

// TestLocalPathPipeline_PVDeleted_FlipsToOrphaned drives the Delete
// event: a matched PV is removed via the API, and on the next scan
// its formerly-claimed directory must surface as orphaned.
func TestLocalPathPipeline_PVDeleted_FlipsToOrphaned(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pvc-gone_default_demo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	pv := makeLocalPV("pv-gone", dir, nodeName)

	cs := fake.NewClientset(pv)
	factory := informers.NewSharedInformerFactory(cs, 0)
	inv := inventory.NewInventory()
	if err := k8s.RegisterPVHandler(factory, inv, inventory.Config{}); err != nil {
		t.Fatalf("RegisterPVHandler: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(ctx.Done(),
		factory.Core().V1().PersistentVolumes().Informer().HasSynced) {
		t.Fatal("informer cache failed to sync")
	}

	s := localpath.New(localpath.Config{
		StorageRoots: []string{root},
		NodeName:     nodeName,
		MaxDepth:     1,
	})

	scan1, err := s.Scan(ctx)
	if err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	if first := diff.Compute(inv.Snapshot(), scan1); len(first.Orphaned) != 0 {
		t.Fatalf("scan 1: want no orphans, got %v", orphanPaths(first.Orphaned))
	}

	if err := cs.CoreV1().PersistentVolumes().Delete(ctx, "pv-gone", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete pv-gone: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(inv.Snapshot()) > 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if got := len(inv.Snapshot()); got != 0 {
		t.Fatalf("inventory size after Delete: want 0, got %d", got)
	}

	scan2, err := s.Scan(ctx)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	second := diff.Compute(inv.Snapshot(), scan2)
	if paths := orphanPaths(second.Orphaned); !equal(paths, []string{dir}) {
		t.Errorf("scan 2 orphaned: want [%q], got %v", dir, paths)
	}
}

// makeLocalPV is a small builder for a Local-volume PV pinned to
// node by spec.nodeAffinity (kubernetes.io/hostname In).
func makeLocalPV(name, path, node string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "local-path",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				Local: &corev1.LocalVolumeSource{Path: path},
			},
			NodeAffinity: &corev1.VolumeNodeAffinity{
				Required: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      "kubernetes.io/hostname",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{node},
						}},
					}},
				},
			},
		},
	}
}

func danglingNames(d []diff.DanglingPV) []string {
	out := make([]string, 0, len(d))
	for _, x := range d {
		out = append(out, x.PV.Name)
	}
	return out
}

func orphanPaths(o []diff.OrphanedDir) []string {
	out := make([]string, 0, len(o))
	for _, x := range o {
		out = append(out, x.Path)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
