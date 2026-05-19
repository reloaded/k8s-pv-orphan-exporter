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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
)

func TestFromPV(t *testing.T) {
	tests := []struct {
		name        string
		pv          *corev1.PersistentVolume
		wantBackend inventory.Backend
		wantPaths   []inventory.ExpectedPath
	}{
		{
			name: "local PV with hostname affinity",
			pv: pvBuilder("pv-local", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					Local: &corev1.LocalVolumeSource{Path: "/opt/lpp/pv-local"},
				},
				NodeAffinity: hostnameAffinity("node-1"),
			}),
			wantBackend: inventory.BackendLocalPath,
			wantPaths:   []inventory.ExpectedPath{{Node: "node-1", Path: "/opt/lpp/pv-local"}},
		},
		{
			name: "local PV with no recognizable affinity has no expected paths",
			pv: pvBuilder("pv-orphan-affinity", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					Local: &corev1.LocalVolumeSource{Path: "/opt/lpp/x"},
				},
				NodeAffinity: nil,
			}),
			wantBackend: inventory.BackendLocalPath,
			wantPaths:   nil,
		},
		{
			name: "local PV with multi-node hostname In",
			pv: pvBuilder("pv-multi", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					Local: &corev1.LocalVolumeSource{Path: "/opt/lpp/pv-multi"},
				},
				NodeAffinity: hostnameAffinity("node-1", "node-2"),
			}),
			wantBackend: inventory.BackendLocalPath,
			wantPaths: []inventory.ExpectedPath{
				{Node: "node-1", Path: "/opt/lpp/pv-multi"},
				{Node: "node-2", Path: "/opt/lpp/pv-multi"},
			},
		},
		{
			name: "local PV with non-hostname affinity is unrecognized",
			pv: pvBuilder("pv-zone", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					Local: &corev1.LocalVolumeSource{Path: "/opt/lpp/x"},
				},
				NodeAffinity: &corev1.VolumeNodeAffinity{
					Required: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "topology.kubernetes.io/zone",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"us-east-1a"},
							}},
						}},
					},
				},
			}),
			wantBackend: inventory.BackendLocalPath,
			wantPaths:   nil,
		},
		{
			name: "hostPath PV applies to every node",
			pv: pvBuilder("pv-host", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: "/data/pv-host"},
				},
			}),
			wantBackend: inventory.BackendLocalPath,
			wantPaths:   []inventory.ExpectedPath{{Path: "/data/pv-host"}},
		},
		{
			name: "in-tree NFS PV",
			pv: pvBuilder("pv-nfs", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					NFS: &corev1.NFSVolumeSource{Server: "nfs.example", Path: "/export/pv-nfs"},
				},
			}),
			wantBackend: inventory.BackendNFS,
			wantPaths:   []inventory.ExpectedPath{{Path: "/export/pv-nfs"}},
		},
		{
			name: "nfs.csi.k8s.io with subDir",
			pv: pvBuilder("pv-csi", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver: "nfs.csi.k8s.io",
						VolumeAttributes: map[string]string{
							"server": "nfs.example",
							"share":  "/export",
							"subDir": "team-a/pvc-1",
						},
					},
				},
			}),
			wantBackend: inventory.BackendNFS,
			wantPaths:   []inventory.ExpectedPath{{Path: "team-a/pvc-1"}},
		},
		{
			name: "unrecognized CSI driver is unknown backend",
			pv: pvBuilder("pv-other-csi", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{Driver: "ebs.csi.aws.com"},
				},
			}),
			wantBackend: inventory.BackendUnknown,
			wantPaths:   nil,
		},
		{
			name:        "spec with no source is unknown backend",
			pv:          pvBuilder("pv-empty", corev1.PersistentVolumeSpec{}),
			wantBackend: inventory.BackendUnknown,
			wantPaths:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Empty Config: no NFS scanner configured, so NFS
			// paths stay raw (pre-Phase-3 behaviour). The
			// mount-join cases are covered by TestFromPV_NFSPaths.
			got := inventory.FromPV(tc.pv, inventory.Config{})
			if got.Backend != tc.wantBackend {
				t.Errorf("Backend: want %q, got %q", tc.wantBackend, got.Backend)
			}
			if !equalExpected(tc.wantPaths, got.ExpectedPaths) {
				t.Errorf("ExpectedPaths: want %v, got %v", tc.wantPaths, got.ExpectedPaths)
			}
		})
	}
}

func TestFromPV_ClaimAndStatus(t *testing.T) {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-x"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName:              "local-path",
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				Namespace: "default",
				Name:      "demo",
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				Local: &corev1.LocalVolumeSource{Path: "/opt/lpp/pv-x"},
			},
			NodeAffinity: hostnameAffinity("node-1"),
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	}
	got := inventory.FromPV(pv, inventory.Config{})
	if got.StorageClass != "local-path" {
		t.Errorf("StorageClass: want local-path, got %q", got.StorageClass)
	}
	if got.ReclaimPolicy != "Retain" {
		t.Errorf("ReclaimPolicy: want Retain, got %q", got.ReclaimPolicy)
	}
	if got.Phase != "Released" {
		t.Errorf("Phase: want Released, got %q", got.Phase)
	}
	if got.ClaimNamespace != "default" || got.ClaimName != "demo" {
		t.Errorf("ClaimRef: want default/demo, got %s/%s", got.ClaimNamespace, got.ClaimName)
	}
}

// TestFromPV_NFSPaths covers issue #6: an NFS PV's server-side path
// must be rewritten to the path the scanner observes under its mount.
func TestFromPV_NFSPaths(t *testing.T) {
	tests := []struct {
		name      string
		pv        *corev1.PersistentVolume
		cfg       inventory.Config
		wantPaths []inventory.ExpectedPath
	}{
		{
			// Acceptance: mount=/mnt/nfs, subDir=team-a/pvc-1.
			name: "nfs.csi.k8s.io subDir joined with mount path",
			pv: pvBuilder("pv-csi", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver: "nfs.csi.k8s.io",
						VolumeAttributes: map[string]string{
							"server": "nfs.example",
							"share":  "/export/k8s",
							"subDir": "team-a/pvc-1",
						},
					},
				},
			}),
			cfg:       inventory.Config{NFS: inventory.NFSConfig{MountPath: "/mnt/nfs"}},
			wantPaths: []inventory.ExpectedPath{{Path: "/mnt/nfs/team-a/pvc-1"}},
		},
		{
			// Acceptance: spec.nfs.path=/export/k8s/team-a/pvc-1,
			// export-root=/export/k8s, mount=/mnt/nfs.
			name: "in-tree NFS path stripped of export root and joined with mount",
			pv: pvBuilder("pv-nfs", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					NFS: &corev1.NFSVolumeSource{Server: "nfs.example", Path: "/export/k8s/team-a/pvc-1"},
				},
			}),
			cfg: inventory.Config{NFS: inventory.NFSConfig{
				MountPath:  "/mnt/nfs",
				ExportRoot: "/export/k8s",
			}},
			wantPaths: []inventory.ExpectedPath{{Path: "/mnt/nfs/team-a/pvc-1"}},
		},
		{
			name: "configured server match keeps the PV in scope",
			pv: pvBuilder("pv-nfs", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					NFS: &corev1.NFSVolumeSource{Server: "nfs.example", Path: "/export/k8s/a"},
				},
			}),
			cfg: inventory.Config{NFS: inventory.NFSConfig{
				MountPath: "/mnt/nfs", ExportRoot: "/export/k8s", Server: "nfs.example",
			}},
			wantPaths: []inventory.ExpectedPath{{Path: "/mnt/nfs/a"}},
		},
		{
			name: "different NFS server yields no expected paths",
			pv: pvBuilder("pv-other", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					NFS: &corev1.NFSVolumeSource{Server: "other.example", Path: "/export/k8s/a"},
				},
			}),
			cfg: inventory.Config{NFS: inventory.NFSConfig{
				MountPath: "/mnt/nfs", ExportRoot: "/export/k8s", Server: "nfs.example",
			}},
			wantPaths: nil,
		},
		{
			name: "export-root boundary is component-wise (no /export/k8stra match)",
			pv: pvBuilder("pv-sib", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					NFS: &corev1.NFSVolumeSource{Server: "nfs.example", Path: "/export/k8stra/a"},
				},
			}),
			cfg: inventory.Config{NFS: inventory.NFSConfig{
				MountPath: "/mnt/nfs", ExportRoot: "/export/k8s",
			}},
			// Not under the export root: falls back to the raw
			// server-side path; the diff engine's root filter then
			// drops it (it isn't under /mnt/nfs).
			wantPaths: []inventory.ExpectedPath{{Path: "/export/k8stra/a"}},
		},
		{
			name: "csi without subDir yields no expected paths",
			pv: pvBuilder("pv-csi-nosub", corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver:           "nfs.csi.k8s.io",
						VolumeAttributes: map[string]string{"server": "nfs.example"},
					},
				},
			}),
			cfg:       inventory.Config{NFS: inventory.NFSConfig{MountPath: "/mnt/nfs"}},
			wantPaths: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inventory.FromPV(tc.pv, tc.cfg)
			if got.Backend != inventory.BackendNFS {
				t.Errorf("Backend: want %q, got %q", inventory.BackendNFS, got.Backend)
			}
			if !equalExpected(tc.wantPaths, got.ExpectedPaths) {
				t.Errorf("ExpectedPaths: want %v, got %v", tc.wantPaths, got.ExpectedPaths)
			}
		})
	}
}

// helpers

func pvBuilder(name string, spec corev1.PersistentVolumeSpec) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
}

func hostnameAffinity(nodes ...string) *corev1.VolumeNodeAffinity {
	return &corev1.VolumeNodeAffinity{
		Required: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      "kubernetes.io/hostname",
					Operator: corev1.NodeSelectorOpIn,
					Values:   nodes,
				}},
			}},
		},
	}
}

func equalExpected(a, b []inventory.ExpectedPath) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]inventory.ExpectedPath(nil), a...)
	bb := append([]inventory.ExpectedPath(nil), b...)
	sort.Slice(aa, func(i, j int) bool { return aa[i].Node+aa[i].Path < aa[j].Node+aa[j].Path })
	sort.Slice(bb, func(i, j int) bool { return bb[i].Node+bb[i].Path < bb[j].Node+bb[j].Path })
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
