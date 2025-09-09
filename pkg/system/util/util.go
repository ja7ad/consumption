//go:build linux

package util

import "math"

type EMA struct {
	alpha, prev float64
	ok          bool
}

func NewEMA(alpha float64) *EMA { return &EMA{alpha: alpha} }
func (e *EMA) Next(v float64) float64 {
	if !e.ok {
		e.prev, e.ok = v, true
		return v
	}
	e.prev = e.alpha*v + (1-e.alpha)*e.prev
	return e.prev
}

func DeltaU64(now, prev uint64) uint64 {
	if now >= prev {
		return now - prev
	}
	// counter wrapped or prev unset
	return 0
}

func SafeDiv(n, d float64) float64 {
	const eps = 1e-12
	if d > eps || d < -eps {
		return n / d
	}
	return 0
}

func Clamp01(x float64) float64 {
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

func Pow(a, b float64) float64 {
	if a <= 0 {
		return 0
	}
	return math.Exp(b * math.Log(a))
}
