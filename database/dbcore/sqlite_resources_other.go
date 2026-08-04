//go:build !linux

package dbcore

func detectEffectiveMemoryBytes() int64 {
	// Production containers run on Linux. Other platforms use the conservative
	// one-GiB profile so development machines do not retain an oversized cache.
	return gibibyte
}
