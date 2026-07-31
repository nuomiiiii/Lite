package metricstore

import "runtime"

const gib = int64(1024 * 1024 * 1024)

type sqliteResourceProfile struct {
	MemoryBytes         int64
	CPUCount            int
	WriterCacheKB       int
	ReaderCacheKB       int
	ReadPoolSize        int
	HeavyReadConcurrent int
	MMapBytes           int64
}

func detectSQLiteResourceProfile() sqliteResourceProfile {
	memoryBytes, cpuCount := detectEffectiveResources()
	return sqliteResourceProfileFor(memoryBytes, cpuCount)
}

func sqliteResourceProfileFor(memoryBytes int64, cpuCount int) sqliteResourceProfile {
	if memoryBytes <= 0 {
		memoryBytes = gib
	}
	if cpuCount <= 0 {
		cpuCount = runtime.NumCPU()
	}
	if cpuCount <= 0 {
		cpuCount = 1
	}

	profile := sqliteResourceProfile{
		MemoryBytes:         memoryBytes,
		CPUCount:            cpuCount,
		WriterCacheKB:       12 * 1024,
		ReaderCacheKB:       10 * 1024,
		ReadPoolSize:        2,
		HeavyReadConcurrent: 2,
		MMapBytes:           128 * 1024 * 1024,
	}
	switch {
	case memoryBytes <= 512*1024*1024:
		profile.WriterCacheKB = 8 * 1024
		profile.ReaderCacheKB = 8 * 1024
		profile.MMapBytes = 64 * 1024 * 1024
	case memoryBytes <= gib:
		// The defaults above keep total SQLite heap/cache usage around 32-48 MiB.
	case memoryBytes <= 2*gib:
		profile.WriterCacheKB = 16 * 1024
		profile.ReaderCacheKB = 12 * 1024
		profile.MMapBytes = 192 * 1024 * 1024
	default:
		profile.WriterCacheKB = 20 * 1024
		profile.ReaderCacheKB = 16 * 1024
		profile.MMapBytes = 256 * 1024 * 1024
	}

	switch {
	case cpuCount <= 1:
		profile.ReadPoolSize = 1
		profile.HeavyReadConcurrent = 1
	case cpuCount == 2:
		profile.ReadPoolSize = 2
		profile.HeavyReadConcurrent = 2
	default:
		profile.ReadPoolSize = 3
		profile.HeavyReadConcurrent = 3
	}
	// A small memory limit always wins over a large CPU allocation.
	if memoryBytes <= 512*1024*1024 {
		profile.ReadPoolSize = 1
		profile.HeavyReadConcurrent = 1
	}
	return profile
}
