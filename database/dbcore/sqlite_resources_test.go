package dbcore

import "testing"

func TestMainDatabaseCacheSizeKBBoundsMemory(t *testing.T) {
	tests := []struct {
		name   string
		memory int64
		wantKB int
	}{
		{name: "unknown", memory: 0, wantKB: 8 * 1024},
		{name: "half-gibibyte", memory: gibibyte / 2, wantKB: 8 * 1024},
		{name: "one-gibibyte", memory: gibibyte, wantKB: 8 * 1024},
		{name: "two-gibibytes", memory: 2 * gibibyte, wantKB: 12 * 1024},
		{name: "four-gibibytes", memory: 4 * gibibyte, wantKB: 16 * 1024},
		{name: "large", memory: 8 * gibibyte, wantKB: 16 * 1024},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mainDatabaseCacheSizeKBFor(test.memory); got != test.wantKB {
				t.Fatalf("cache size = %d KiB, want %d KiB", got, test.wantKB)
			}
		})
	}
}
