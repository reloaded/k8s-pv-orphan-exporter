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

//go:build unix

package nfs

import (
	"os"
	"syscall"
)

// deviceID returns the underlying device number for a FileInfo, used
// by the cross-filesystem check. The boolean is false on platforms
// where the underlying syscall.Stat_t is not available.
//
// Kept package-local (a near-twin of the local-path copy) rather than
// shared: it is ~10 lines, build-tag-sensitive, and factoring it out
// would mean touching the just-merged Phase 2 scanner for no
// behavioural gain.
func deviceID(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return uint64(st.Dev), true
}
