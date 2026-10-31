//go:build unix

package osmmini

import "syscall"

// Process CPU time separates actual query work (including GC and kernel time)
// from scheduler waits on a development machine running other workloads.
func benchmarkCPUSeconds() float64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
}
