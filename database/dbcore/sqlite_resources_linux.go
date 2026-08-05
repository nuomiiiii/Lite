//go:build linux

package dbcore

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func detectEffectiveMemoryBytes() int64 {
	memoryBytes := linuxPhysicalMemoryBytes()
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		if limit := readPositiveMemoryLimit(path); limit > 0 && (memoryBytes <= 0 || limit < memoryBytes) {
			memoryBytes = limit
		}
	}
	return memoryBytes
}

func linuxPhysicalMemoryBytes() int64 {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0
	}
	return int64(info.Totalram) * int64(info.Unit)
}

func readPositiveMemoryLimit(path string) int64 {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value := strings.TrimSpace(string(content))
	if value == "" || value == "max" {
		return 0
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || n >= 1<<60 {
		return 0
	}
	return n
}
