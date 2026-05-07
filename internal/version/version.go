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

// Package version exposes build-time identity for the exporter binary.
//
// All values are intended to be overridden at build time via -ldflags
// "-X github.com/reloaded/k8s-pv-orphan-exporter/internal/version.Version=...".
package version

import "runtime"

var (
	Version   = "dev"
	Revision  = "unknown"
	Branch    = "unknown"
	BuildDate = "unknown"
)

// GoVersion returns the Go runtime version the binary was built with.
func GoVersion() string {
	return runtime.Version()
}
