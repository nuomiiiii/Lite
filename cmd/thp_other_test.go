//go:build !linux

package cmd

import "testing"

func TestDisableTransparentHugePagesIsNoop(t *testing.T) {
	if err := disableTransparentHugePages(); err != nil {
		t.Fatalf("disable transparent huge pages: %v", err)
	}
	if transparentHugePagesApplied() {
		t.Fatal("non-linux build should not claim THP disable was applied")
	}
}
