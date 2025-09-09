// Package proc provides lightweight, zero-dependency process/resource sampling
// on Linux for estimating per-process (or process-tree) resource usage over time.
// It is designed to feed higher-level power/energy models (see pkg/consumption).
//
// Overview
//
//   - Collector interface:
//     Sample(pids []int, dtSec float64) (Snapshot, error)
//     Close() error
//
//     Sample returns a Snapshot representing utilization and byte deltas over the
//     last sampling window (dtSec). You typically call Sample in a loop with a
//     ticker (dt ≈ INTERVAL). Close performs backend cleanup (e.g., removes a
//     temporary cgroup v2 leaf), best-effort.
//
//   - Backends:
//
//   - cgroup v2 (preferred): uses unified hierarchy for accurate CPU & memory
//     attribution via cpu.stat (usage_usec) and memory.stat (workingset_refault).
//
//   - cgroup v1 (fallback): emulates utilization using /proc, and approximates
//     RAM activity using minor faults × page size as a proxy (no true refaults).
//
//   - Snapshot fields:
//     TimeSec        : sampling window duration in seconds (≈ dtSec you pass)
//     UVm, UProc     : utilization in [0,1] (VM/system and process group)
//     ReadBytes      : sum of /proc/<pid>/io read_bytes deltas
//     WriteBytes     : sum of /proc/<pid>/io write_bytes deltas
//     RefaultBytes   : v2: workingset_refault * pagesize; v1: minor faults * pagesize (proxy)
//     RSSChurnBytes  : sum of |ΔRSS| per pid (from smaps_rollup/statm)
//
//   - Errors (errs.go):
//     ErrNoPIDs    : Sample called with empty pid slice
//     ErrBadDt     : dtSec <= 0
//     ErrAllExited : none of the provided pids are alive at sampling time
//
//   - Smoothing (EMA):
//     v1 and v2 collectors can be constructed with an alpha ∈ [0,1] to apply an
//     exponential moving average to VM utilization (U_vm). alpha=0 disables.
//
// # Cgroup v2 behavior
//
// The v2 collector creates a temporary leaf cgroup under /sys/fs/cgroup
// (consumption.<pid>.<rand>) and (best-effort) moves the provided PIDs into it
// by writing to cgroup.procs. On each Sample:
//   - VM CPU comes from root cpu.stat (usage_usec).
//   - Process-group CPU comes from the temp cgroup's cpu.stat (usage_usec).
//   - Workingset refaults come from temp memory.stat (workingset_refault).
//   - I/O & RSS are still read per-PID from /proc.
//
// Close() attempts to remove the temporary cgroup. This will only succeed if it
// is empty and permissions allow it (best-effort, safe to ignore errors).
//
// # Cgroup v1 behavior
//
// Without cgroup v2, the v1 collector derives:
//   - UVm from /proc/stat CPU time deltas (normalized by NumCPU*dt).
//   - UProc from per-PID utime+stime deltas (normalized by NumCPU*dt).
//   - RefaultBytes ≈ minor faults * page size (proxy for cache refaults).
//   - I/O & RSS as in v2, per-PID from /proc.
//
// This provides stable signals but CPU attribution isn’t scheduler-precise.
//
// Utilization definitions
//
//	UVm   = (Δ VM CPU seconds) / (NumCPU * dt)
//	UProc = (Δ Group CPU seconds) / (NumCPU * dt)
//
// Both values are clamped to [0,1] to avoid rare first-tick spikes.
// Δ VM CPU seconds:
//   - v2: Δ usage_usec(root) / 1e6
//   - v1: Δ active jiffies (/proc/stat) * (1/CLK_TCK)
//
// Δ Group CPU seconds:
//   - v2: Δ usage_usec(temp cgroup) / 1e6
//   - v1: Σpids Δ(utime+stime)/CLK_TCK
//
// RAM proxies
//
//	RefaultBytes (v2): workingset_refault * pagesize
//	RefaultBytes (v1): minflt * pagesize (best-effort proxy)
//	RSSChurnBytes    : Σpids |ΔRSS|; RSS from smaps_rollup when available, else statm.
//
// Permissions & portability
//
//   - v2 requires cgroup v2 mounted on /sys/fs/cgroup and permission to create a
//     sub-cgroup and move PIDs (often requires root or proper delegation).
//   - v1 needs only /proc.
//   - Both backends are read-only to /proc; v2 writes to cgroup.procs when possible.
//
// Factory & version selection
//
//	NewCollector(alpha float64) (Collector, error) chooses the backend:
//	  - v2 (or hybrid): tries v2 first, falls back to v1 if not implemented/available.
//	  - v1: uses v1.
//
// Callers don’t need to check cgroup version explicitly.
//
// Example: one-shot sampling
//
//	/*
//	col, err := proc.NewCollector(0.3) // EMA α=0.3 on VM utilization
//	if err != nil { log.Fatal(err) }
//	defer col.Close()
//
//	pids := []int{os.Getpid()}
//	dt := 1.0
//	snap, err := col.Sample(pids, dt)
//	if err != nil { log.Fatal(err) }
//	fmt.Printf("U_vm=%.3f U_proc=%.3f bytes: R=%d W=%d ref=%d rssΔ=%d\n",
//	    snap.UVm, snap.UProc, snap.ReadBytes, snap.WriteBytes, snap.RefaultBytes, snap.RSSChurnBytes)
//	*/
//
// Example: loop with ticker (feed consumption model)
//
//	/*
//	col, _ := proc.NewCollector(0.5)
//	defer col.Close()
//
//	cfg := consumption.DefaultConfig()
//	acc := consumption.New(cfg)
//
//	pids := []int{rootPID} // optionally expand children beforehand
//	ticker := time.NewTicker(time.Second)
//	defer ticker.Stop()
//
//	for i := 0; i < 20; i++ { // ~SAMPLES
//	    <-ticker.C
//	    snap, err := col.Sample(pids, 1.0) // INTERVAL≈1s
//	    if err != nil {
//	        if errors.Is(err, proc.ErrAllExited) { break }
//	        log.Printf("sample error: %v", err)
//	        continue
//	    }
//	    res := acc.Apply(snap) // pkg/consumption
//	    log.Printf("P(cpu)=%.3fW P(disk)=%.3fW P(ram)=%.3fW P(total)=%.3fW E=%.3fJ",
//	        res.PCPU, res.PDisk, res.PRAM, res.PTotal, acc.EnergyCumJ())
//	}
//	*/
//
// Example: expanding a process tree
//
//	/*
//	// Use /proc/<pid>/task/*/children to discover descendants (non-recursive by design).
//	// You can layer a small BFS/queue on top to collect the full tree before sampling.
//	queue := []int{rootPID}
//	seen  := map[int]struct{}{rootPID:{}}
//	for len(queue) > 0 {
//	    pid := queue[0]; queue = queue[1:]
//	    kids, _ := proc.ReadProcChildren(pid)
//	    for _, k := range kids {
//	        if _, ok := seen[k]; ok { continue }
//	        seen[k] = struct{}{}
//	        queue = append(queue, k)
//	    }
//	}
//	var pids []int
//	for pid := range seen { pids = append(pids, pid) }
//	*/
//
// Testing guidance
//
//   - v1 tests are hermetic (read from /proc) and require no privileges.
//   - v2 tests should SKIP if /sys/fs/cgroup is not a cgroup2 mount.
//   - Some kernels may omit memory.stat:workingset_refault; treat missing as zero.
//   - Avoid asserting specific non-zero values on idle runners; induce a small
//     workload (touch memory, do a few writes, burn a bit of CPU) to get signal.
//
// Performance notes
//
//   - Sampling cost grows roughly with number of PIDs due to /proc reads.
//   - v2 adds O(1) extra reads (two files: cpu.stat & memory.stat) and one write
//     per sampling tick per PID (cgroup.procs) when permissions allow.
//   - Consider light EMA smoothing (α≈0.3–0.7) to stabilize UVm on bursty hosts.
//
// See also
//
//   - pkg/consumption for converting Snapshot to power/energy using your coefficients.
//   - types.Bytes for human-friendly byte formatting in UIs.
//
// Package import path: github.com/ja7ad/consumption/pkg/system/proc
package proc
