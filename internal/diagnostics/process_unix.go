//go:build linux || darwin

package diagnostics

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func processMetrics() (float64, int64, string) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, 0, "unavailable: getrusage failed"
	}
	cpu := float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/self/statm")
		if err != nil {
			return cpu, 0, "cpu available; RSS unavailable: procfs unreadable"
		}
		fields := strings.Fields(string(data))
		if len(fields) > 1 {
			pages, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				return cpu, pages * int64(os.Getpagesize()), "available: current RSS via procfs"
			}
		}
		return cpu, 0, "cpu available; RSS unavailable: invalid procfs"
	}
	return cpu, usage.Maxrss, "available: peak RSS via getrusage (darwin)"
}
