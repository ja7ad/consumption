package types

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBytes_Humanized_Boundaries(t *testing.T) {
	cases := []struct {
		in   Bytes
		want string
	}{
		{Bytes(0), "0 B"},
		{Bytes(1), "1 B"},
		{Bytes(1023), "1023 B"},                   // just below 1 KiB
		{Bytes(1024), "1.00 KB"},                  // exactly 1 KiB
		{Bytes(1024*1024 - 1), "1024.00 KB"},      // just below 1 MiB
		{Bytes(1024 * 1024), "1.00 MB"},           // exactly 1 MiB
		{Bytes(1024*1024*1024 - 1), "1024.00 MB"}, // just below 1 GiB
		{Bytes(1024 * 1024 * 1024), "1.00 GB"},    // exactly 1 GiB
		{Bytes(1<<40 - 1), "1024.00 GB"},          // just below 1 TiB
		{Bytes(1 << 40), "1.00 TB"},               // exactly 1 TiB
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("case_%d_%d", i, uint64(tc.in)), func(t *testing.T) {
			got := tc.in.Humanized()
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBytes_Humanized_NonRound(t *testing.T) {
	// 1536 B = 1.50 KB
	assert.Equal(t, "1.50 KB", Bytes(1536).Humanized())

	// 12.345 MB ≈ 12.35 MB
	b := Bytes(uint64(math.Round(12.345 * float64(1<<20))))
	assert.Equal(t, "12.35 MB", b.Humanized())

	// 2.75 GB ≈ 2.75 GB
	b = Bytes(uint64(math.Round(2.75 * float64(1<<30))))
	assert.Equal(t, "2.75 GB", b.Humanized())
}

func TestBytes_UnitAccessors(t *testing.T) {
	const (
		KiB = 1024.0
		MiB = 1024.0 * 1024.0
		GiB = 1024.0 * 1024.0 * 1024.0
	)
	// Exact boundaries
	assert.InDelta(t, 1.0, Bytes(1024).KB(), 1e-12)
	assert.InDelta(t, 1.0, Bytes(1<<20).MB(), 1e-12)
	assert.InDelta(t, 1.0, Bytes(1<<30).GB(), 1e-12)

	// Non-integers
	b := Bytes(1536) // 1.5 KiB
	assert.InDelta(t, 1.5, b.KB(), 1e-12)
	assert.InDelta(t, 1.5/KiB, b.MB(), 1e-12)
	assert.InDelta(t, 1.5/MiB, b.GB(), 1e-12)

	// Large number
	b = Bytes(5 * (1 << 30))                     // 5 GiB
	assert.InDelta(t, (5*GiB)/KiB, b.KB(), 1e-6) // big floats; loosen slightly
	assert.InDelta(t, 5*GiB/MiB, b.MB(), 1e-6)
	assert.InDelta(t, 5.0, b.GB(), 1e-12)
}

func TestBytes_Humanized_TinyValues(t *testing.T) {
	// Ensure sub-KiB remain in bytes
	for _, v := range []uint64{2, 10, 255, 512, 1023} {
		want := fmt.Sprintf("%d B", v)
		assert.Equal(t, want, Bytes(v).Humanized())
	}
}
