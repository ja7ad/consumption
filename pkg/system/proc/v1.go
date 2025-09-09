//go:build linux

package proc

import (
	"runtime"

	"github.com/ja7ad/consumption/pkg/types"
)

// v1Collector samples utilization using only /proc:
//   - VM CPU: /proc/stat (active/total jiffies)
//   - Per-PID CPU: /proc/<pid>/stat (utime+stime jiffies)
//   - Per-PID IO:  /proc/<pid>/io (read_bytes/write_bytes)
//   - RAM proxies: /proc/<pid>/stat (minflt), /proc/<pid>/smaps_rollup|statm (RSS)
type v1Collector struct {
	clkTck   int
	pageSize int
	nproc    int

	// EMA smoothing for VM utilization (helps avoid spikes when dt is small).
	alpha     float64
	emaOK     bool
	emaPrevUV float64

	// VM prev counters (from /proc/stat)
	vmActivePrev uint64
	vmTotalPrev  uint64

	// Per-PID prev counters
	cpuPrev    map[int]uint64 // utime+stime (jiffies)
	rbytesPrev map[int]uint64
	wbytesPrev map[int]uint64
	rssPrev    map[int]uint64
	minfltPrev map[int]uint64
	majfltPrev map[int]uint64
}

func newV1(alpha float64) (Collector, error) {
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}
	active, total, err := ReadSystemCPU()
	if err != nil {
		return nil, err
	}
	return &v1Collector{
		clkTck:       ClockTicks(),
		pageSize:     PageSize(),
		nproc:        runtime.NumCPU(),
		alpha:        alpha,
		vmActivePrev: active,
		vmTotalPrev:  total,
		cpuPrev:      make(map[int]uint64),
		rbytesPrev:   make(map[int]uint64),
		wbytesPrev:   make(map[int]uint64),
		rssPrev:      make(map[int]uint64),
		minfltPrev:   make(map[int]uint64),
		majfltPrev:   make(map[int]uint64),
	}, nil
}

func (c *v1Collector) Close() error { return nil }

func (c *v1Collector) Sample(pids []int, dtSec float64) (Snapshot, error) {
	if len(pids) == 0 {
		return Snapshot{}, ErrNoPIDs
	}
	if !(dtSec > 0) {
		return Snapshot{}, ErrBadDt
	}

	// VM CPU deltas
	vmActiveNow, vmTotalNow, err := ReadSystemCPU()
	if err != nil {
		return Snapshot{}, err
	}
	dActive := deltaU64(vmActiveNow, c.vmActivePrev)
	dTotal := deltaU64(vmTotalNow, c.vmTotalPrev)
	uvm := safeDiv(float64(dActive), float64(dTotal)) // [0,1] nominal
	c.vmActivePrev, c.vmTotalPrev = vmActiveNow, vmTotalNow

	// EMA smoothing on VM utilization (optional)
	if c.alpha > 0 {
		if !c.emaOK {
			c.emaPrevUV = uvm
			c.emaOK = true
		} else {
			c.emaPrevUV = c.alpha*uvm + (1-c.alpha)*c.emaPrevUV
		}
		uvm = c.emaPrevUV
	}
	uvm = clamp01(uvm)

	// Aggregate per-PID deltas
	var (
		cpuJiffiesDelta uint64
		readDelta       uint64
		writeDelta      uint64
		refaultBytes    uint64 // v1: not available; keep 0
		rssChurnBytes   uint64
		alive           int
	)
	for _, pid := range pids {
		if !Exists(pid) {
			continue
		}
		alive++

		// CPU jiffies (utime+stime)
		ut, st, mn, mj, err := ReadProcStat(pid)
		if err == nil {
			j := ut + st
			cpuJiffiesDelta += deltaU64(j, c.cpuPrev[pid])
			c.cpuPrev[pid] = j
			// Minor faults (first-touch, no IO)
			dMn := deltaU64(mn, c.minfltPrev[pid])
			c.minfltPrev[pid] = mn
			// Major faults are ignored for RAM proxy (usually involve disk)
			dMj := deltaU64(mj, c.majfltPrev[pid])
			c.majfltPrev[pid] = mj
			_ = dMj
			// Convert minor faults to bytes (rough proxy)
			refaultBytes += dMn * uint64(c.pageSize)
		}

		// I/O bytes
		if rNow, wNow, err := ReadProcIO(pid); err == nil {
			readDelta += deltaU64(rNow, c.rbytesPrev[pid])
			writeDelta += deltaU64(wNow, c.wbytesPrev[pid])
			c.rbytesPrev[pid] = rNow
			c.wbytesPrev[pid] = wNow
		}

		// RSS churn (absolute delta)
		if rssNow, err := ReadProcRSS(pid); err == nil {
			prev := c.rssPrev[pid]
			if rssNow >= prev {
				rssChurnBytes += rssNow - prev
			} else {
				rssChurnBytes += prev - rssNow
			}
			c.rssPrev[pid] = rssNow
		}
	}
	if alive == 0 {
		return Snapshot{}, ErrAllExited
	}

	// Process CPU utilization (seconds from jiffies) → normalized to [0,1]
	cpuSecProc := float64(cpuJiffiesDelta) / float64(c.clkTck)
	uproc := safeDiv(cpuSecProc, float64(c.nproc)*dtSec)
	uproc = clamp01(uproc)

	return Snapshot{
		TimeSec:       dtSec,
		UVm:           uvm,
		UProc:         uproc,
		ReadBytes:     types.ToBytes(readDelta),
		WriteBytes:    types.ToBytes(writeDelta),
		RefaultBytes:  types.ToBytes(refaultBytes),  // v1 proxy via minor faults
		RSSChurnBytes: types.ToBytes(rssChurnBytes), // per-PID RSS absolute deltas
	}, nil
}
