//go:build linux

package consumption

import (
	"math"

	"github.com/ja7ad/consumption/pkg/system/proc"
	"github.com/ja7ad/consumption/pkg/system/util"
)

// Accumulator keeps running energy and averages.
type Accumulator struct {
	cfg        *Config
	energyCumJ float64
	count      int
	sumPCPU    float64
	sumPDisk   float64
	sumPRAM    float64
	sumPTotal  float64
}

// New creates an accumulator with the given config.
// Fields > 0 (or valid ranges) in cfg override defaults.
// Notes:
//   - Alpha in [0..1] is accepted verbatim (0 is a valid choice).
//   - EMemRef/EMemRSS: zero is treated as an intentional "disable" and respected.
//   - Negative values are treated as "unset" and defaulted.
//   - PIdle/PMax/Gamma/ER/EW must be > 0 to override defaults.
func New(cfg *Config) *Accumulator {
	base := _defaultConfig()

	// No user cfg: use defaults as-is.
	if cfg == nil {
		return &Accumulator{cfg: base}
	}

	merged := *base

	// Positive-only overrides
	if cfg.PIdle > 0 {
		merged.PIdle = cfg.PIdle
	}
	if cfg.PMax > 0 {
		merged.PMax = cfg.PMax
	}
	if cfg.Gamma > 0 {
		merged.Gamma = cfg.Gamma
	}
	if cfg.ER > 0 {
		merged.ER = cfg.ER
	}
	if cfg.EW > 0 {
		merged.EW = cfg.EW
	}

	// RAM proxies: allow zero to intentionally disable, default only if negative.
	if cfg.EMemRef >= 0 {
		merged.EMemRef = cfg.EMemRef
	}
	if cfg.EMemRSS >= 0 {
		merged.EMemRSS = cfg.EMemRSS
	}

	// Alpha must be in [0..1]; 0 is a valid "no idle share".
	if cfg.Alpha >= 0 && cfg.Alpha <= 1 {
		merged.Alpha = cfg.Alpha
	}

	// Optional sanity: ensure PMax >= PIdle; if not, clamp to avoid nonsense.
	if merged.PMax < merged.PIdle {
		merged.PMax = merged.PIdle
	}

	return &Accumulator{cfg: &merged}
}

// Apply runs the model on a single snapshot (one tick), returns the power split,
// and updates cumulative energy/averages.
//
// It assumes snap.TimeSec ~ your sampling interval (dt). Energy is accumulated as:
//
//	E_cum += P_total * dt
func (a *Accumulator) Apply(snap proc.Snapshot) Result {
	uvm := util.Clamp01(snap.UVm)
	up := util.Clamp01(snap.UProc)

	// CPU dynamic power at VM level
	pdyn := (a.cfg.PMax - a.cfg.PIdle) * util.Pow(uvm, a.cfg.Gamma)

	// Attribute dynamic CPU power by share
	var pcpu float64
	if uvm > 1e-12 {
		pcpu = (up / uvm) * pdyn
	}

	// Disk + RAM power from energy / dt
	dt := math.Max(snap.TimeSec, 1e-6)
	edisk := a.cfg.ER*float64(snap.ReadBytes) + a.cfg.EW*float64(snap.WriteBytes)
	pdisk := edisk / dt

	eram := a.cfg.EMemRef*float64(snap.RefaultBytes) + a.cfg.EMemRSS*float64(snap.RSSChurnBytes)
	pram := eram / dt

	// Optional idle share
	var pidleShare float64
	if uvm > 1e-12 && a.cfg.Alpha > 0 {
		pidleShare = a.cfg.Alpha * a.cfg.PIdle * (up / uvm)
	}

	ptot := pcpu + pdisk + pram + pidleShare

	// Update cumulatives/averages
	a.energyCumJ += ptot * dt
	a.count++
	a.sumPCPU += pcpu
	a.sumPDisk += pdisk
	a.sumPRAM += pram
	a.sumPTotal += ptot

	return Result{PCPU: pcpu, PDisk: pdisk, PRAM: pram, PTotal: ptot}
}

// EnergyCumJ returns cumulative energy in Joules.
func (a *Accumulator) EnergyCumJ() float64 { return a.energyCumJ }

// Averages returns average powers over all applied samples.
func (a *Accumulator) Averages() Result {
	if a.count == 0 {
		return Result{}
	}
	n := float64(a.count)
	return Result{
		PCPU:   a.sumPCPU / n,
		PDisk:  a.sumPDisk / n,
		PRAM:   a.sumPRAM / n,
		PTotal: a.sumPTotal / n,
	}
}
