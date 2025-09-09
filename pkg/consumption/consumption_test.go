//go:build linux

package consumption

import (
	"fmt"
	"math"
	"testing"

	"github.com/ja7ad/consumption/pkg/system/proc"
	"github.com/ja7ad/consumption/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func expect(cfg *Config, s proc.Snapshot) (pcpu, pdisk, pram, ptotal float64) {
	uvm := s.UVm
	if uvm < 0 {
		uvm = 0
	}
	if uvm > 1 {
		uvm = 1
	}
	up := s.UProc
	if up < 0 {
		up = 0
	}
	if up > 1 {
		up = 1
	}

	pdyn := (cfg.PMax - cfg.PIdle) * math.Pow(uvm, cfg.Gamma)
	if uvm > 1e-12 {
		pcpu = (up / uvm) * pdyn
	}

	dt := math.Max(s.TimeSec, 1e-6)
	edisk := cfg.ER*float64(s.ReadBytes) + cfg.EW*float64(s.WriteBytes)
	pdisk = edisk / dt

	eram := cfg.EMemRef*float64(s.RefaultBytes) + cfg.EMemRSS*float64(s.RSSChurnBytes)
	pram = eram / dt

	var pidleShare float64
	if uvm > 1e-12 && cfg.Alpha > 0 {
		pidleShare = cfg.Alpha * cfg.PIdle * (up / uvm)
	}

	ptotal = pcpu + pdisk + pram + pidleShare
	return
}

func TestConsumption_Sequence_WithLogs(t *testing.T) {
	cfg := &Config{
		PIdle:   5,
		PMax:    20,
		Gamma:   1.3,
		ER:      4.8e-8,
		EW:      9.5e-8,
		EMemRef: 7e-10,
		EMemRSS: 3e-10,
		Alpha:   0.1, // small idle share
	}
	acc := New(cfg)

	// synthetic snapshots (dt ~ 1s each) with increasing activity
	const MB = 1 << 20
	snaps := []proc.Snapshot{
		{TimeSec: 1.0, UVm: 0.10, UProc: 0.05, ReadBytes: 1 * MB, WriteBytes: 0, RefaultBytes: 64 * 1024, RSSChurnBytes: 128 * 1024},
		{TimeSec: 1.0, UVm: 0.25, UProc: 0.12, ReadBytes: 2 * MB, WriteBytes: 1 * MB, RefaultBytes: 256 * 1024, RSSChurnBytes: 512 * 1024},
		{TimeSec: 1.0, UVm: 0.50, UProc: 0.25, ReadBytes: 4 * MB, WriteBytes: 2 * MB, RefaultBytes: 512 * 1024, RSSChurnBytes: 1 * MB},
		{TimeSec: 1.0, UVm: 0.80, UProc: 0.40, ReadBytes: 8 * MB, WriteBytes: 4 * MB, RefaultBytes: 1 * MB, RSSChurnBytes: 2 * MB},
	}

	var sumPCPU, sumPDisk, sumPRAM, sumPT float64
	var sumE float64

	t.Logf("# tick,  U_vm,  U_proc |   P_cpu(W)   P_disk(W)   P_ram(W)  |  P_total(W)   E_cum(J)")
	for i, s := range snaps {
		res := acc.Apply(s)
		sumPCPU += res.PCPU
		sumPDisk += res.PDisk
		sumPRAM += res.PRAM
		sumPT += res.PTotal
		sumE += res.PTotal * s.TimeSec

		// Cross-check with expected math
		expPCPU, expPDisk, expPRAM, expPT := expect(cfg, s)
		require.InDelta(t, expPCPU, res.PCPU, 1e-9, "pcpu mismatch at tick %d", i)
		require.InDelta(t, expPDisk, res.PDisk, 1e-9, "pdisk mismatch at tick %d", i)
		require.InDelta(t, expPRAM, res.PRAM, 1e-9, "pram mismatch at tick %d", i)
		require.InDelta(t, expPT, res.PTotal, 1e-9, "ptotal mismatch at tick %d", i)

		// log this tick
		t.Logf("%5d, %5.2f,   %5.2f | %10.4f %11.4f %10.4f | %11.4f %11.4f",
			i+1, s.UVm, s.UProc, res.PCPU, res.PDisk, res.PRAM, res.PTotal, acc.EnergyCumJ())
	}

	// Energy accumulation
	assert.InDelta(t, sumE, acc.EnergyCumJ(), 1e-9)

	// Averages from accumulator should match means
	avg := acc.Averages()
	n := float64(len(snaps))
	assert.InDelta(t, sumPCPU/n, avg.PCPU, 1e-12)
	assert.InDelta(t, sumPDisk/n, avg.PDisk, 1e-12)
	assert.InDelta(t, sumPRAM/n, avg.PRAM, 1e-12)
	assert.InDelta(t, sumPT/n, avg.PTotal, 1e-12)

	// final summary log
	t.Log("---- summary (averages) ----")
	t.Logf("avg P(cpu)  : %.6f W", avg.PCPU)
	t.Logf("avg P(disk) : %.6f W", avg.PDisk)
	t.Logf("avg P(ram)  : %.6f W", avg.PRAM)
	t.Logf("avg P(total): %.6f W", avg.PTotal)
	t.Logf("E_cum       : %.6f J", acc.EnergyCumJ())
}

func TestConsumption_ZeroAndClampPaths_WithLogs(t *testing.T) {
	cfg := &Config{
		PIdle: 5, PMax: 20, Gamma: 1.3,
		ER: 4.8e-8, EW: 9.5e-8, EMemRef: 7e-10, EMemRSS: 3e-10,
		Alpha: 0.2,
	}
	acc := New(cfg)

	cases := []proc.Snapshot{
		// U_vm=0 → no CPU allocation; only disk/ram contribute
		{TimeSec: 1, UVm: 0, UProc: 0.9, ReadBytes: 2_000_000, WriteBytes: 1_000_000},
		// clamp UProc<0 and U_vm>1
		{TimeSec: 1, UVm: 1.5, UProc: -0.5, ReadBytes: 0, WriteBytes: 0},
	}

	for i, s := range cases {
		res := acc.Apply(s)
		expPCPU, expPDisk, expPRAM, expPT := expect(cfg, s)

		// res should match expected formula (with clamps)
		require.InDelta(t, expPCPU, res.PCPU, 1e-9, "pcpu (case %d)", i)
		require.InDelta(t, expPDisk, res.PDisk, 1e-9, "pdisk (case %d)", i)
		require.InDelta(t, expPRAM, res.PRAM, 1e-9, "pram (case %d)", i)
		require.InDelta(t, expPT, res.PTotal, 1e-9, "ptotal (case %d)", i)

		t.Logf("case %d: U_vm=%.2f U_proc=%.2f -> P(cpu)=%.6f P(disk)=%.6f P(ram)=%.6f P(total)=%.6f E_cum=%.6f",
			i+1, s.UVm, s.UProc, res.PCPU, res.PDisk, res.PRAM, res.PTotal, acc.EnergyCumJ())
	}
}

func TestConsumption_AveragesOverMany_WithLogs(t *testing.T) {
	cfg := &Config{
		PIdle: 5, PMax: 20, Gamma: 1.3,
		ER: 4.8e-8, EW: 9.5e-8,
		EMemRef: 7e-10, EMemRSS: 3e-10,
		Alpha: 0.0,
	}
	acc := New(cfg)

	// 20 samples like your CLI defaults (INTERVAL=1, SAMPLES=20) but synthetic
	var totalPT float64
	for i := 0; i < 20; i++ {
		uvm := 0.3 + 0.02*float64(i%5) // 0.30..0.38
		up := 0.1 + 0.01*float64(i%3)  // 0.10..0.12
		rb := uint64(200_000 * (1 + (i % 4)))
		wb := uint64(100_000 * (1 + (i % 3)))
		s := proc.Snapshot{
			TimeSec: 1.0, UVm: uvm, UProc: up,
			ReadBytes: types.ToBytes(rb), WriteBytes: types.ToBytes(wb),
			RefaultBytes: 32 * 1024, RSSChurnBytes: 64 * 1024,
		}
		res := acc.Apply(s)
		totalPT += res.PTotal
		t.Logf("tick %02d: UVm=%.3f UProc=%.3f -> Ptotal=%.6fW E_cum=%.6fJ",
			i+1, uvm, up, res.PTotal, acc.EnergyCumJ())
	}

	avg := acc.Averages()
	require.Greater(t, avg.PTotal, 0.0)
	assert.InDelta(t, totalPT/20.0, avg.PTotal, 1e-12)

	t.Log("---- 20-sample summary ----")
	t.Logf("avg P(total): %.6f W", avg.PTotal)
	t.Logf("E_cum       : %.6f J", acc.EnergyCumJ())
}

func ExampleAccumulator_logging() {
	cfg := &Config{PIdle: 5, PMax: 20, Gamma: 1.3, ER: 4.8e-8, EW: 9.5e-8, EMemRef: 7e-10, EMemRSS: 3e-10}
	acc := New(cfg)
	s := proc.Snapshot{TimeSec: 1, UVm: 0.5, UProc: 0.25}
	r := acc.Apply(s)
	fmt.Printf("P(cpu)=%.3fW P(total)=%.3fW E=%.3fJ\n", r.PCPU, r.PTotal, acc.EnergyCumJ())
}
