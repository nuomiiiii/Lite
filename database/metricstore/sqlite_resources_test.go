package metricstore

import "testing"

func TestSQLiteResourceProfilesBoundMemoryAndConcurrency(t *testing.T) {
	tests := []struct {
		name       string
		memory     int64
		cpus       int
		writerKB   int
		readerKB   int
		readers    int
		concurrent int
		mmap       int64
		maxTotalKB int
	}{
		{"256-mib-one-cpu", 256 * mib, 1, 1 * 1024, 1 * 1024, 1, 1, 0, 2 * 1024},
		{"just-over-256-mib", 256*mib + 1, 1, 2 * 1024, 1 * 1024, 1, 1, 0, 3 * 1024},
		{"256-mib-many-cpu", 256 * mib, 8, 1 * 1024, 1 * 1024, 1, 1, 0, 2 * 1024},
		{"small-many-cpu", 512 * mib, 8, 2 * 1024, 1 * 1024, 1, 1, 0, 3 * 1024},
		{"one-gib-many-cpu", gib, 8, 4 * 1024, 2 * 1024, 2, 2, 0, 8 * 1024},
		{"two-gib-one-cpu", 2 * gib, 1, 4 * 1024, 2 * 1024, 1, 1, 0, 6 * 1024},
		{"large-many-cpu", 8 * gib, 16, 8 * 1024, 4 * 1024, 2, 2, 0, 16 * 1024},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sqliteResourceProfileFor(test.memory, test.cpus)
			if got.WriterCacheKB != test.writerKB || got.ReaderCacheKB != test.readerKB ||
				got.ReadPoolSize != test.readers || got.HeavyReadConcurrent != test.concurrent ||
				got.MMapBytes != test.mmap {
				t.Fatalf("profile = %#v", got)
			}
			totalCacheKB := got.WriterCacheKB + got.ReaderCacheKB*got.ReadPoolSize
			if totalCacheKB > test.maxTotalKB {
				t.Fatalf("sqlite cache budget = %d KiB, want <= %d KiB", totalCacheKB, test.maxTotalKB)
			}
			if totalCacheKB > 16*1024 {
				t.Fatalf("sqlite cache budget = %d KiB, want <= 16 MiB", totalCacheKB)
			}
		})
	}
}
