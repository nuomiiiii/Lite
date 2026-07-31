//go:build !linux

package metricstore

import "runtime"

func detectEffectiveResources() (int64, int) {
	// Production containers run on Linux where cgroup and physical-memory
	// limits are available. Other platforms use the conservative 1 GiB profile.
	return gib, runtime.NumCPU()
}
