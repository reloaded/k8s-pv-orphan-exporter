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

// This file extends the integration suite to the Phase 3 NFS
// pipeline: NFS PVs (in-tree and nfs.csi.k8s.io) are pushed through a
// fake informer with the scanner-instance NFSConfig, a real temp
// directory stands in for the mounted export, and the diff engine's
// dangling/orphaned/archived output is asserted — including the
// issue-#6 server-side-path → mount-path rewrite and the watch path.
//
// Like local_path_test.go it uses fake.NewClientset rather than a
// real kind cluster; the kind + sidecar-NFS variant (design.md §13)
// is future work tracked separately.
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
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner/nfs"
)

const (
	nfsServer     = "nfs.example"
	nfsExportRoot = "/export/k8s"
)

// TestNFSPipeline drives the full Phase 3 pipeline against a fake
// clientset and a real temp directory standing in for the mounted
// export:
//   - an in-tree NFS PV whose directory exists  → neither
//   - a CSI nfs.csi.k8s.io PV whose dir is gone  → dangling
//   - a directory referenced by no PV            → orphaned
//   - an "archived-" directory                   → archived
//
// It also exercises issue #6: the in-tree PV's server-side
// spec.nfs.path and the CSI subDir are both rewritten to the path the
// scanner actually observes under the mount.
func TestNFSPipeline(t *testing.T) {
	mount := t.TempDir()

	liveDir := filepath.Join(mount, "live")   // matches pv-live (in-tree)
	strayDir := filepath.Join(mount, "stray") // no PV → orphan
	archived := filepath.Join(mount, "archived-team-a-pvc-000")
	for _, d := range []string{liveDir, strayDir, archived} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	livePV := makeInTreeNFSPV("pv-live", nfsServer, nfsExportRoot+"/live")
	danglingPV := makeCSINFSPV("pv-dangling", nfsServer, "missing") // <mount>/missing not created

	cfg := inventory.Config{NFS: inventory.NFSConfig{
		MountPath:  mount,
		ExportRoot: nfsExportRoot,
		Server:     nfsServer,
	}}

	cs := fake.NewClientset(livePV, danglingPV)
	factory := informers.NewSharedInformerFactory(cs, 0)
	inv := inventory.NewInventory()
	if err := k8s.RegisterPVHandler(factory, inv, cfg); err != nil {
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

	s := nfs.New(nfs.Config{
		MountPath:      mount,
		ArchivedPrefix: "archived-",
		MaxDepth:       1,
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
	if paths := archivedPaths(result.Archived); !equal(paths, []string{archived}) {
		t.Errorf("archived: want [%q], got %v", archived, paths)
	}
}

// TestNFSPipeline_PVAddedAfterScan verifies the watch path for NFS: a
// stray directory stops being orphaned once a matching CSI PV is
// created and observed by the informer.
func TestNFSPipeline_PVAddedAfterScan(t *testing.T) {
	mount := t.TempDir()
	dir := filepath.Join(mount, "late")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := inventory.Config{NFS: inventory.NFSConfig{MountPath: mount, Server: nfsServer}}

	cs := fake.NewClientset()
	factory := informers.NewSharedInformerFactory(cs, 0)
	inv := inventory.NewInventory()
	if err := k8s.RegisterPVHandler(factory, inv, cfg); err != nil {
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

	s := nfs.New(nfs.Config{MountPath: mount, ArchivedPrefix: "archived-", MaxDepth: 1})

	scanResult, err := s.Scan(ctx)
	if err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	if first := diff.Compute(inv.Snapshot(), scanResult); len(first.Orphaned) != 1 {
		t.Fatalf("first scan orphan count: want 1, got %d", len(first.Orphaned))
	}

	pv := makeCSINFSPV("pv-late", nfsServer, "late")
	if _, err := cs.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create PV: %v", err)
	}

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

func makeInTreeNFSPV(name, server, path string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "nfs",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				NFS: &corev1.NFSVolumeSource{Server: server, Path: path},
			},
		},
	}
}

func makeCSINFSPV(name, server, subDir string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "nfs-csi",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver: "nfs.csi.k8s.io",
					VolumeAttributes: map[string]string{
						"server": server,
						"share":  nfsExportRoot,
						"subDir": subDir,
					},
				},
			},
		},
	}
}

func archivedPaths(a []diff.ArchivedDir) []string {
	out := make([]string, 0, len(a))
	for _, x := range a {
		out = append(out, x.Path)
	}
	return out
}
