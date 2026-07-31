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
	}{
		{"small-many-cpu", 512 * 1024 * 1024, 8, 8 * 1024, 8 * 1024, 1, 1},
		{"one-gib-many-cpu", gib, 8, 12 * 1024, 10 * 1024, 3, 3},
		{"two-gib-one-cpu", 2 * gib, 1, 16 * 1024, 12 * 1024, 1, 1},
		{"large-many-cpu", 8 * gib, 16, 20 * 1024, 16 * 1024, 3, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sqliteResourceProfileFor(test.memory, test.cpus)
			if got.WriterCacheKB != test.writerKB || got.ReaderCacheKB != test.readerKB ||
				got.ReadPoolSize != test.readers || got.HeavyReadConcurrent != test.concurrent {
				t.Fatalf("profile = %#v", got)
			}
		})
	}
}
