//go:build linux

package proc

import "math"

func deltaU64(now, prev uint64) uint64 {
	if now >= prev {
		return now - prev
	}
	// counter wrapped or prev unset
	return 0
}

func safeDiv(n, d float64) float64 {
	const eps = 1e-12
	if d > eps || d < -eps {
		return n / d
	}
	return 0
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	// guard against NaN
	if math.IsNaN(x) {
		return 0
	}
	return x
}
