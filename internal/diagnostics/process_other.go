//go:build !linux && !darwin

package diagnostics

func processMetrics() (float64, int64, string) { return 0, 0, "unavailable: unsupported OS" }
