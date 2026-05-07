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

// Package k8s wraps client-go for the exporter: kubeconfig / in-cluster
// resolution, a typed clientset constructor, and a SharedInformerFactory
// builder. It is intentionally thin — it exists so the rest of the
// codebase never has to import client-go directly.
package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ClientConfig describes how to build a Kubernetes client.
type ClientConfig struct {
	// Kubeconfig is the path to a kubeconfig file. Empty means the
	// process expects to be running inside a pod and uses
	// in-cluster config.
	Kubeconfig string
	QPS        float32
	Burst      int
}

// NewClient builds a typed clientset from cfg. If cfg.Kubeconfig is
// empty and the process is not in a cluster, this returns an error
// from rest.InClusterConfig.
func NewClient(cfg ClientConfig) (kubernetes.Interface, error) {
	rcfg, err := restConfig(cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}
	if cfg.QPS > 0 {
		rcfg.QPS = cfg.QPS
	}
	if cfg.Burst > 0 {
		rcfg.Burst = cfg.Burst
	}
	cs, err := kubernetes.NewForConfig(rcfg)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}
	return cs, nil
}

func restConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}
