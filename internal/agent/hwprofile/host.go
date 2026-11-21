package hwprofile

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
)

// What the host offers for profiles, read from /proc and /sys.

// Hugepages reads the huge page pool from /proc/meminfo content: pages in it,
// pages free, and the page size in KiB.
func Hugepages(meminfo string) (total, free, pageKiB int64) {
	sc := bufio.NewScanner(strings.NewReader(meminfo))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "HugePages_Total:":
			total = n
		case "HugePages_Free:":
			free = n
		case "Hugepagesize:":
			pageKiB = n
		}
	}
	return total, free, pageKiB
}

// HostHugepagesMiB is the host's huge page pool and what is free of it, in MiB.
func HostHugepagesMiB() (totalMiB, freeMiB int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	total, free, kib := Hugepages(string(b))
	return total * kib / 1024, free * kib / 1024
}

// HostOnlineCPUs are the host CPUs the kernel runs.
func HostOnlineCPUs() ([]int, error) {
	b, err := os.ReadFile("/sys/devices/system/cpu/online")
	if err != nil {
		return nil, err
	}
	return models.ParseCPUSet(string(b))
}

// HostNUMANode is the NUMA node of a host CPU, -1 when the host does not say.
func HostNUMANode(cpu int) int {
	matches, _ := filepath.Glob("/sys/devices/system/cpu/cpu" + strconv.Itoa(cpu) + "/node*")
	for _, m := range matches {
		if n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(m), "node")); err == nil {
			return n
		}
	}
	return -1
}

// Exists reports a file that is there.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
