package metricstore

import "runtime"

const (
	mib = int64(1024 * 1024)
	gib = 1024 * mib
)

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

	// Size caches from the 256 MiB product floor upward. mmap stays off so
	// metrics.db pages are not kept twice (SQLite cache + mapping). Extra host
	// RAM does not buy a larger cache until well above 2 GiB.
	profile := sqliteResourceProfile{
		MemoryBytes:         memoryBytes,
		CPUCount:            cpuCount,
		WriterCacheKB:       4 * 1024,
		ReaderCacheKB:       2 * 1024,
		ReadPoolSize:        1,
		HeavyReadConcurrent: 1,
		MMapBytes:           0,
	}
	switch {
	case memoryBytes <= 256*mib:
		profile.WriterCacheKB = 1 * 1024
		profile.ReaderCacheKB = 1 * 1024
	case memoryBytes <= 512*mib:
		profile.WriterCacheKB = 2 * 1024
		profile.ReaderCacheKB = 1 * 1024
	case memoryBytes <= gib:
		// Defaults above: about 6 MiB of SQLite cache on 1 GiB hosts.
	case memoryBytes <= 2*gib:
		// Same as 1 GiB: a 2 GiB VPS should not look "large" to the cache.
	default:
		profile.WriterCacheKB = 8 * 1024
		profile.ReaderCacheKB = 4 * 1024
	}

	if cpuCount >= 2 && memoryBytes > 512*mib {
		profile.ReadPoolSize = 2
		profile.HeavyReadConcurrent = 2
	}
	return profile
}
