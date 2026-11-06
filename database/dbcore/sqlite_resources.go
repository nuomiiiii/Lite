package dbcore

const (
	mib      = int64(1024 * 1024)
	gibibyte = 1024 * mib
)

func mainDatabaseCacheSizeKB() int {
	return mainDatabaseCacheSizeKBFor(detectEffectiveMemoryBytes())
}

func mainDatabaseCacheSizeKBFor(memoryBytes int64) int {
	if memoryBytes <= 0 {
		memoryBytes = gibibyte
	}
	// Page cache follows the product floor (256 MiB) and the working set, not
	// spare RAM. lite.db can grow with MCP/audit/sessions, but a 256 MiB box
	// cannot pre-size for a large future file.
	switch {
	case memoryBytes <= 256*mib:
		return 2 * 1024
	case memoryBytes <= 512*mib:
		return 4 * 1024
	case memoryBytes <= 2*gibibyte:
		return 8 * 1024
	default:
		return 12 * 1024
	}
}
