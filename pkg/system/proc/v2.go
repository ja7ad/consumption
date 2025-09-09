//go:build linux

package proc

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ja7ad/consumption/pkg/types"
)

// v2Collector uses cgroup v2 unified hierarchy for accurate CPU attribution.
// - VM CPU from /sys/fs/cgroup/cpu.stat (usage_usec)
// - Group CPU from <grp>/cpu.stat (usage_usec)
// - Memory refaults from <grp>/memory.stat (workingset_refault)
// - Per-PID IO/RSS from /proc (same as v1)
type v2Collector struct {
	// Config
	alpha    float64 // EMA smoothing factor for U_vm (0..1)
	pageSize int
	nproc    int

	// Cgroup paths
	rootCG string // usually /sys/fs/cgroup
	grpCG  string // created temporary leaf cgroup

	// Prev counters
	vmUsageUsecPrev  uint64 // root usage_usec
	grpUsageUsecPrev uint64 // group usage_usec
	wsRefaultPrev    uint64 // group workingset_refault (count of pages)

	// EMA state for U_vm
	emaOK     bool
	emaPrevUV float64

	// Per-PID previous counters
	rbytesPrev map[int]uint64
	wbytesPrev map[int]uint64
	rssPrev    map[int]uint64
}

// newV2 constructs the v2 collector, creates a temp cgroup under /sys/fs/cgroup,
// and seeds the root vmUsageUsecPrev from root cpu.stat.
func newV2(alpha float64) (Collector, error) {
	root := "/sys/fs/cgroup"
	if _, err := os.Stat(root); err != nil {
		// If root isn't present, we can't run v2 collector.
		return nil, fmt.Errorf("cgroup v2 root not found: %w", err)
	}
	// Ensure this is actually cgroup2
	isV2, err := isCgroup2Mounted(root)
	if err != nil {
		return nil, err
	}
	if !isV2 {
		return nil, errors.New("cgroup v2 not mounted on /sys/fs/cgroup")
	}

	grp, err := createTempGroup(root)
	if err != nil {
		return nil, fmt.Errorf("create temp cgroup: %w", err)
	}

	// Seed VM usage from root cpu.stat
	vmUse, err := readCPUUsageUsec(filepath.Join(root, "cpu.stat"))
	if err != nil {
		_ = os.Remove(grp) // cleanup best effort
		return nil, fmt.Errorf("read root cpu.stat: %w", err)
	}

	return &v2Collector{
		alpha:           clamp01(alpha),
		pageSize:        PageSize(),
		nproc:           runtime.NumCPU(),
		rootCG:          root,
		grpCG:           grp,
		vmUsageUsecPrev: vmUse,

		rbytesPrev: make(map[int]uint64),
		wbytesPrev: make(map[int]uint64),
		rssPrev:    make(map[int]uint64),
	}, nil
}

func (c *v2Collector) Close() error {
	// Best effort: remove the temporary cgroup directory.
	// This will only succeed if it's empty (no processes).
	// If processes remain (caller stopped sampling early), removal will fail.
	return os.Remove(c.grpCG)
}

func (c *v2Collector) Sample(pids []int, dtSec float64) (Snapshot, error) {
	if len(pids) == 0 {
		return Snapshot{}, ErrNoPIDs
	}
	if !(dtSec > 0) {
		return Snapshot{}, ErrBadDt
	}

	// Move PIDs into our group (idempotent; ignore EPERM/ENOENT per PID)
	alive := 0
	for _, pid := range pids {
		if !Exists(pid) {
			continue
		}
		if err := writePIDtoCgroup(c.grpCG, pid); err == nil {
			alive++
		} else {
			// Ignore if we fail to move — we'll still account IO/RSS via /proc
			// but CPU/memory accounting will miss that pid this tick.
			alive++
		}
	}
	if alive == 0 {
		return Snapshot{}, ErrAllExited
	}

	// CPU usage (VM/root and group) from cpu.stat
	vmUseNow, err := readCPUUsageUsec(filepath.Join(c.rootCG, "cpu.stat"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read root cpu.stat: %w", err)
	}
	grpUseNow, err := readCPUUsageUsec(filepath.Join(c.grpCG, "cpu.stat"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read group cpu.stat: %w", err)
	}

	dVMusec := deltaU64(vmUseNow, c.vmUsageUsecPrev)
	dGRPusec := deltaU64(grpUseNow, c.grpUsageUsecPrev)
	c.vmUsageUsecPrev, c.grpUsageUsecPrev = vmUseNow, grpUseNow

	// Utilizations
	// vm seconds over dt and nproc
	uVm := safeDiv(float64(dVMusec)/1e6, float64(c.nproc)*dtSec)
	// group seconds normalized the same (NOTE: this is already "absolute" group utilization,
	// but we report it as UProc in [0,1] relative to total capacity)
	uProc := safeDiv(float64(dGRPusec)/1e6, float64(c.nproc)*dtSec)

	// EMA smoothing on VM utilization (optional)
	if c.alpha > 0 {
		if !c.emaOK {
			c.emaPrevUV = uVm
			c.emaOK = true
		} else {
			c.emaPrevUV = c.alpha*uVm + (1-c.alpha)*c.emaPrevUV
		}
		uVm = c.emaPrevUV
	}
	uVm = clamp01(uVm)
	uProc = clamp01(uProc)

	// Memory refaults (workingset_refault) from memory.stat
	wsRefNow, err := readWorkingsetRefault(filepath.Join(c.grpCG, "memory.stat"))
	if err != nil {
		// Some kernels may not expose it (unlikely on v2). If missing, treat as zero.
		wsRefNow = c.wsRefaultPrev
	}
	dWsRef := deltaU64(wsRefNow, c.wsRefaultPrev)
	c.wsRefaultPrev = wsRefNow
	refaultBytes := dWsRef * uint64(c.pageSize)

	// Per-PID IO + RSS churn (via /proc)
	var readDelta, writeDelta, rssChurn uint64
	aliveCount := 0
	for _, pid := range pids {
		if !Exists(pid) {
			continue
		}
		aliveCount++

		// IO
		if rNow, wNow, err := ReadProcIO(pid); err == nil {
			readDelta += deltaU64(rNow, c.rbytesPrev[pid])
			writeDelta += deltaU64(wNow, c.wbytesPrev[pid])
			c.rbytesPrev[pid] = rNow
			c.wbytesPrev[pid] = wNow
		}
		// RSS churn
		if rssNow, err := ReadProcRSS(pid); err == nil {
			prev := c.rssPrev[pid]
			if rssNow >= prev {
				rssChurn += (rssNow - prev)
			} else {
				rssChurn += (prev - rssNow)
			}
			c.rssPrev[pid] = rssNow
		}
	}
	if aliveCount == 0 {
		// Race: all died between move and read; treat as exited.
		return Snapshot{}, ErrAllExited
	}

	return Snapshot{
		TimeSec:       dtSec,
		UVm:           uVm,
		UProc:         uProc,
		ReadBytes:     types.ToBytes(readDelta),
		WriteBytes:    types.ToBytes(writeDelta),
		RefaultBytes:  types.ToBytes(refaultBytes),
		RSSChurnBytes: types.ToBytes(rssChurn),
	}, nil
}

// ---- cgroup v2 helpers ----

// isCgroup2Mounted returns true if the given path is a cgroup2 mount.
func isCgroup2Mounted(path string) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, fmt.Errorf("open mountinfo: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// mountinfo has "... <mountpoint> ... - <fstype> <source> <opts>"
		sep := " - "
		i := strings.LastIndex(line, sep)
		if i < 0 {
			continue
		}
		pre := strings.Fields(line[:i])
		if len(pre) < 5 {
			continue
		}
		mountPoint := pre[4]
		tail := strings.Fields(line[i+len(sep):])
		if len(tail) < 1 {
			continue
		}
		fstype := tail[0]
		if mountPoint == path && fstype == "cgroup2" {
			return true, nil
		}
	}
	return false, sc.Err()
}

// createTempGroup makes a unique sub-cgroup under root (e.g., /sys/fs/cgroup/consumption.<pid>.<rand>)
func createTempGroup(root string) (string, error) {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := fmt.Sprintf("consumption.%d.%s", os.Getpid(), hex.EncodeToString(suffix))
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// writePIDtoCgroup moves a PID into the given cgroup by writing to <grp>/cgroup.procs.
func writePIDtoCgroup(grp string, pid int) error {
	f, err := os.OpenFile(filepath.Join(grp, "cgroup.procs"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strconv.Itoa(pid))
	if err == nil {
		_, err = f.WriteString("\n")
	}
	return err
}

// readCPUUsageUsec parses cpu.stat and returns usage_usec.
func readCPUUsageUsec(cpuStatPath string) (uint64, error) {
	f, err := os.Open(cpuStatPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "usage_usec ") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				v, err := strconv.ParseUint(fs[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return v, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("cpu.stat: usage_usec not found")
}

// readWorkingsetRefault parses memory.stat and returns workingset_refault (count of pages).
func readWorkingsetRefault(memStatPath string) (uint64, error) {
	f, err := os.Open(memStatPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "workingset_refault ") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				v, err := strconv.ParseUint(fs[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return v, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	// Not all kernels expose it; treat missing as zero with a sentinel error if you want.
	return 0, errors.New("memory.stat: workingset_refault not found")
}
