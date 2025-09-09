//go:build linux

package proc

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeltaU64(t *testing.T) {
	t.Run("normal_increase", func(t *testing.T) {
		assert.Equal(t, uint64(10), deltaU64(110, 100))
	})
	t.Run("no_change", func(t *testing.T) {
		assert.Equal(t, uint64(0), deltaU64(100, 100))
	})
	t.Run("wrap_or_prev_unset", func(t *testing.T) {
		// now < prev → treated as wrap/reset → 0
		assert.Equal(t, uint64(0), deltaU64(99, 100))
	})
	t.Run("large_values", func(t *testing.T) {
		const hi = ^uint64(0) - 5
		assert.Equal(t, uint64(5), deltaU64(hi, hi-5))
	})
}

func TestSafeDiv(t *testing.T) {
	const eps = 1e-12

	t.Run("regular_positive", func(t *testing.T) {
		require.InDelta(t, 2.5, safeDiv(5, 2), 1e-12)
	})
	t.Run("regular_negative", func(t *testing.T) {
		require.InDelta(t, -2.5, safeDiv(-5, 2), 1e-12)
		require.InDelta(t, -2.5, safeDiv(5, -2), 1e-12)
		require.InDelta(t, 2.5, safeDiv(-5, -2), 1e-12)
	})
	t.Run("zero_denominator", func(t *testing.T) {
		assert.Equal(t, 0.0, safeDiv(123, 0))
	})
	t.Run("tiny_denominator_below_eps", func(t *testing.T) {
		d := eps / 10
		assert.Equal(t, 0.0, safeDiv(1, d))
		assert.Equal(t, 0.0, safeDiv(1, -d))
	})
	t.Run("tiny_denominator_above_eps", func(t *testing.T) {
		d := eps * 10
		require.InDelta(t, 1.0/d, safeDiv(1, d), 1e-12)
		require.InDelta(t, -1.0/d, safeDiv(1, -d), 1e-12)
	})
}

func TestClamp01(t *testing.T) {
	t.Run("below_zero", func(t *testing.T) {
		assert.Equal(t, 0.0, clamp01(-1e9))
	})
	t.Run("zero_and_one", func(t *testing.T) {
		assert.Equal(t, 0.0, clamp01(0))
		assert.Equal(t, 1.0, clamp01(1))
	})
	t.Run("within_range", func(t *testing.T) {
		assert.InDelta(t, 0.123, clamp01(0.123), 0)
		assert.InDelta(t, 0.999, clamp01(0.999), 0)
	})
	t.Run("above_one", func(t *testing.T) {
		assert.Equal(t, 1.0, clamp01(42))
		assert.Equal(t, 1.0, clamp01(math.MaxFloat64))
	})
	t.Run("NaN_becomes_zero", func(t *testing.T) {
		assert.Equal(t, 0.0, clamp01(math.NaN()))
	})
	t.Run("infinities", func(t *testing.T) {
		assert.Equal(t, 1.0, clamp01(math.Inf(1)))
		assert.Equal(t, 0.0, clamp01(math.Inf(-1)))
	})
}
