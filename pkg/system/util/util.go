//go:build linux

package util

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
