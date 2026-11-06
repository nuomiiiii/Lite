//go:build !linux

package cmd

func disableTransparentHugePages() error {
	return nil
}

func transparentHugePagesApplied() bool {
	return false
}
