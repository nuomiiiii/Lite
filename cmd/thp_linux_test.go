//go:build linux

package cmd

import "testing"

func TestDisableTransparentHugePages(t *testing.T) {
	if err := disableTransparentHugePages(); err != nil {
		t.Fatalf("disable transparent huge pages: %v", err)
	}
	if !transparentHugePagesApplied() {
		t.Fatal("linux build should report THP disable as applied")
	}
	got, err := transparentHugePagesDisabledFlag()
	if err != nil {
		t.Fatalf("PR_GET_THP_DISABLE: %v", err)
	}
	if got != 1 {
		t.Fatalf("PR_GET_THP_DISABLE = %d, want 1", got)
	}
}
