//go:build linux

package util

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEMA_FirstSampleSetsState(t *testing.T) {
	e := NewEMA(0.5)
	out := e.Next(10)
	assert.Equal(t, 10.0, out, "first output should equal first input")
	// second call should blend now
	out2 := e.Next(20)
	assert.InDelta(t, 15.0, out2, 1e-9, "EMA(0.5) of 10 then 20 should be 15")
}

func TestEMA_SequenceAlphaPointFive(t *testing.T) {
	e := NewEMA(0.5)
	// inputs: 10, 20, 20, 40
	got := make([]float64, 0, 4)
	got = append(got, e.Next(10)) // 10
	got = append(got, e.Next(20)) // 0.5*20 + 0.5*10 = 15
	got = append(got, e.Next(20)) // 0.5*20 + 0.5*15 = 17.5
	got = append(got, e.Next(40)) // 0.5*40 + 0.5*17.5 = 28.75

	want := []float64{10, 15, 17.5, 28.75}
	require.Len(t, got, len(want))
	for i := range want {
		assert.InDelta(t, want[i], got[i], 1e-9, "i=%d", i)
	}
}

func TestEMA_AlphaOne_NoSmoothing(t *testing.T) {
	e := NewEMA(1.0)
	// First value passes through, then always equal to latest input
	assert.Equal(t, 10.0, e.Next(10))
	assert.Equal(t, 20.0, e.Next(20))
	assert.Equal(t, 5.0, e.Next(5))
}

func TestEMA_AlphaZero_HoldsInitialValue(t *testing.T) {
	e := NewEMA(0.0)
	// First sample sets state; subsequent outputs remain at that value
	assert.Equal(t, 10.0, e.Next(10))
	assert.Equal(t, 10.0, e.Next(20))
	assert.Equal(t, 10.0, e.Next(-5))
}

func TestEMA_ConvergesToConstantInput(t *testing.T) {
	e := NewEMA(0.3)
	_ = e.Next(0.0) // initialize at 0

	const target = 100.0
	const steps = 50

	prevErr := math.Abs(0 - target)
	var out float64
	for i := 0; i < steps; i++ {
		out = e.Next(target)
		errNow := math.Abs(out - target)
		// error should be non-increasing
		assert.LessOrEqual(t, errNow, prevErr+1e-12, "error should not increase at i=%d", i)
		// output should stay within the [min, max] hull of {initial, target}
		assert.GreaterOrEqual(t, out, 0.0-1e-12)
		assert.LessOrEqual(t, out, target+1e-12)
		prevErr = errNow
	}
	// allow a slightly looser delta for final closeness
	assert.InDelta(t, target, out, 1e-5)
}

func TestEMA_ClosedFormMatch(t *testing.T) {
	alpha := 0.3
	target := 100.0
	steps := 50

	e := NewEMA(alpha)
	_ = e.Next(0.0)

	var out float64
	for i := 0; i < steps; i++ {
		out = e.Next(target)
	}

	// y_n = target * (1 - (1-alpha)^n), given initial 0 before applying target
	want := target * (1 - math.Pow(1-alpha, float64(steps)))
	assert.InDelta(t, want, out, 1e-6)
}
