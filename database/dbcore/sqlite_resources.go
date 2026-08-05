package dbcore

const gibibyte = int64(1024 * 1024 * 1024)

func mainDatabaseCacheSizeKB() int {
	return mainDatabaseCacheSizeKBFor(detectEffectiveMemoryBytes())
}

func mainDatabaseCacheSizeKBFor(memoryBytes int64) int {
	if memoryBytes <= 0 {
		memoryBytes = gibibyte
	}
	switch {
	case memoryBytes <= gibibyte:
		return 8 * 1024
	case memoryBytes <= 2*gibibyte:
		return 12 * 1024
	default:
		return 16 * 1024
	}
}
