//go:build linux

package cmd

import "golang.org/x/sys/unix"

func init() {
	// Process-local only. Do not log here: `version` probes must stay prefix-free.
	_ = disableTransparentHugePages()
}

func disableTransparentHugePages() error {
	return unix.Prctl(unix.PR_SET_THP_DISABLE, 1, 0, 0, 0)
}

func transparentHugePagesApplied() bool {
	return true
}

func transparentHugePagesDisabledFlag() (int, error) {
	return unix.PrctlRetInt(unix.PR_GET_THP_DISABLE, 0, 0, 0, 0)
}
