//go:build !unix

package osmmini

// Wall-clock and allocation metrics remain available on other platforms.
func benchmarkCPUSeconds() float64 { return 0 }
