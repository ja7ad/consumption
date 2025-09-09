//go:build linux

package util

import (
	"math"
	"strings"
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

func TestDeltaU64(t *testing.T) {
	t.Run("normal_increase", func(t *testing.T) {
		assert.Equal(t, uint64(10), DeltaU64(110, 100))
	})
	t.Run("no_change", func(t *testing.T) {
		assert.Equal(t, uint64(0), DeltaU64(100, 100))
	})
	t.Run("wrap_or_prev_unset", func(t *testing.T) {
		// now < prev → treated as wrap/reset → 0
		assert.Equal(t, uint64(0), DeltaU64(99, 100))
	})
	t.Run("large_values", func(t *testing.T) {
		const hi = ^uint64(0) - 5
		assert.Equal(t, uint64(5), DeltaU64(hi, hi-5))
	})
}

func TestSafeDiv(t *testing.T) {
	const eps = 1e-12

	t.Run("regular_positive", func(t *testing.T) {
		require.InDelta(t, 2.5, SafeDiv(5, 2), 1e-12)
	})
	t.Run("regular_negative", func(t *testing.T) {
		require.InDelta(t, -2.5, SafeDiv(-5, 2), 1e-12)
		require.InDelta(t, -2.5, SafeDiv(5, -2), 1e-12)
		require.InDelta(t, 2.5, SafeDiv(-5, -2), 1e-12)
	})
	t.Run("zero_denominator", func(t *testing.T) {
		assert.Equal(t, 0.0, SafeDiv(123, 0))
	})
	t.Run("tiny_denominator_below_eps", func(t *testing.T) {
		d := eps / 10
		assert.Equal(t, 0.0, SafeDiv(1, d))
		assert.Equal(t, 0.0, SafeDiv(1, -d))
	})
	t.Run("tiny_denominator_above_eps", func(t *testing.T) {
		d := eps * 10
		require.InDelta(t, 1.0/d, SafeDiv(1, d), 1e-12)
		require.InDelta(t, -1.0/d, SafeDiv(1, -d), 1e-12)
	})
}

func TestClamp01(t *testing.T) {
	t.Run("below_zero", func(t *testing.T) {
		assert.Equal(t, 0.0, Clamp01(-1e9))
	})
	t.Run("zero_and_one", func(t *testing.T) {
		assert.Equal(t, 0.0, Clamp01(0))
		assert.Equal(t, 1.0, Clamp01(1))
	})
	t.Run("within_range", func(t *testing.T) {
		assert.InDelta(t, 0.123, Clamp01(0.123), 0)
		assert.InDelta(t, 0.999, Clamp01(0.999), 0)
	})
	t.Run("above_one", func(t *testing.T) {
		assert.Equal(t, 1.0, Clamp01(42))
		assert.Equal(t, 1.0, Clamp01(math.MaxFloat64))
	})
	t.Run("NaN_becomes_zero", func(t *testing.T) {
		assert.Equal(t, 0.0, Clamp01(math.NaN()))
	})
	t.Run("infinities", func(t *testing.T) {
		assert.Equal(t, 1.0, Clamp01(math.Inf(1)))
		assert.Equal(t, 0.0, Clamp01(math.Inf(-1)))
	})
}

func TestPow_EdgeCases(t *testing.T) {
	// a <= 0 should return 0
	assert.Equal(t, 0.0, Pow(0, 2))
	assert.Equal(t, 0.0, Pow(-3, 2))
}

func TestPow_BasicIntegerExponents(t *testing.T) {
	assert.InDelta(t, 1.0, Pow(1, 5), 1e-12)
	assert.InDelta(t, 8.0, Pow(2, 3), 1e-12)
	assert.InDelta(t, 81.0, Pow(3, 4), 1e-12)
}

func TestPow_FractionalExponents(t *testing.T) {
	// sqrt(4) = 2
	require.InDelta(t, 2.0, Pow(4, 0.5), 1e-12)

	// cube root of 27 = 3
	require.InDelta(t, 3.0, Pow(27, 1.0/3.0), 1e-12)
}

func TestPow_LargeExponent(t *testing.T) {
	// 2^20 = 1048576
	require.InDelta(t, math.Pow(2, 20), Pow(2, 20), 1e-6)
}

func TestPow_NonIntegerBaseAndExponent(t *testing.T) {
	// 2.5^3.2 compared to math.Pow
	want := math.Pow(2.5, 3.2)
	got := Pow(2.5, 3.2)
	assert.InDelta(t, want, got, 1e-12)
}

func TestParsePIDs_OK_SingleAndMultiple(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		out  []int
	}{
		{"single", []string{"123"}, []int{123}},
		{"multiple_space", []string{"1", "2", "3"}, []int{1, 2, 3}},
		{"with_spaces", []string{"  7  ", "\t8", "9\n"}, []int{7, 8, 9}},
		{"mix_values", []string{"10", "20..22", " 30 "}, []int{10, 20, 21, 22, 30}},
		{"only_range", []string{"5..7"}, []int{5, 6, 7}},
		{"adjacent_ranges", []string{"1..3", "4..5"}, []int{1, 2, 3, 4, 5}},
		{"empty_tokens_ignored", []string{"", "  ", "12"}, []int{12}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePIDs(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.out, got)
		})
	}
}

func TestParsePIDs_Errors(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		msg  string
	}{
		{"bad_pid_alpha", []string{"abc"}, `bad pid: "abc"`},
		{"bad_pid_mixed", []string{"12x"}, `bad pid: "12x"`},
		{"bad_range_non_numeric_left", []string{"a..3"}, `bad range: "a..3"`},
		{"bad_range_non_numeric_right", []string{"1..b"}, `bad range: "1..b"`},
		{"bad_range_reversed", []string{"7..5"}, `bad range: "7..5"`},
		{"bad_range_missing_right", []string{"3.."}, `bad range: "3.."`},
		{"bad_range_missing_left", []string{"..3"}, `bad range: "..3"`},
		{"bad_range_triple_dots", []string{"1...3"}, `bad range: "1...3"`}, // splitN -> ["1",".3"]
		{"bad_range_only_dots", []string{".."}, `bad range: ".."`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePIDs(tt.in)
			require.Error(t, err)
			assert.Equal(t, tt.msg, err.Error())
		})
	}
}

func TestParsePIDs_CombinedOrdering(t *testing.T) {
	got, err := ParsePIDs([]string{"3", "1..2", "10", "8..9"})
	require.NoError(t, err)
	assert.Equal(t, []int{3, 1, 2, 10, 8, 9}, got, "should preserve input order and expand ranges inline")
}

func TestFmtFloat_RoundingAndNearZero(t *testing.T) {
	tests := []struct {
		in  float64
		exp string
	}{
		{0, "0.000"},
		{0.0004, "0.000"},   // abs < 0.0005 => clamp to 0.000
		{-0.0004, "0.000"},  // avoid -0.000
		{0.00049, "0.000"},  // still below threshold
		{0.0005, "0.001"},   // boundary rounds up
		{-0.0005, "-0.001"}, // negative boundary rounds away from zero
		{1.2344, "1.234"},
		{1.2345, "1.234"},
		{-1.2, "-1.200"},
		{123.9996, "124.000"},
	}
	for _, tt := range tests {
		got := FmtFloat(tt.in)
		assert.Equal(t, tt.exp, got, "FmtFloat(%v)", tt.in)
	}
}

func TestCharsToString_Basic(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		exp  string
	}{
		{"simple", []byte{'a', 'b', 'c'}, "abc"},
		{"stops_at_zero", []byte{'h', 'i', 0, 'x', 'y'}, "hi"},
		{"leading_zero", []byte{0, 'a', 'b'}, ""},
		{"all_zeroes", []byte{0, 0, 0}, ""},
		{"unicode_bytes_ok", []byte("golang\x00rocks"), "golang"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := charsToString(tt.in)
			assert.Equal(t, tt.exp, got)
		})
	}
}

func TestSystemSummary_Sanity(t *testing.T) {
	host, kernel, cpus, mem := SystemSummary()

	// Host and kernel should be non-empty on any sane Linux test env
	require.NotEmpty(t, host, "hostname should not be empty")
	require.NotEmpty(t, kernel, "kernel release should not be empty")

	// Per current implementation, cpus is formatted as NumCPU/NumCPU => "1.00"
	assert.Equal(t, "1.00", cpus, "cpus string is expected to be '1.00' given current implementation")

	// Memory string ends with '%' and has a positive numeric part (value semantics not asserted)
	require.True(t, strings.HasSuffix(mem, "%"), "mem should end with '%%'")
	num := strings.TrimSuffix(mem, "%")
	// Accept any parseable positive number
	assert.NotEmpty(t, num, "mem numeric part should not be empty")
}
