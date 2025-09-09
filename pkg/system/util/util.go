//go:build linux

package util

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

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

func DeltaU64(now, prev uint64) uint64 {
	if now >= prev {
		return now - prev
	}
	// counter wrapped or prev unset
	return 0
}

func SafeDiv(n, d float64) float64 {
	const eps = 1e-12
	if d > eps || d < -eps {
		return n / d
	}
	return 0
}

func Clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	// guard against NaN
	if math.IsNaN(x) {
		return 0
	}
	return x
}

func Pow(a, b float64) float64 {
	if a <= 0 {
		return 0
	}
	return math.Exp(b * math.Log(a))
}

func ParsePIDs(args []string) ([]int, error) {
	var out []int
	for _, tok := range args {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if strings.Contains(tok, "..") {
			parts := strings.SplitN(tok, "..", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("bad range: %q", tok)
			}
			a, err1 := strconv.Atoi(parts[0])
			b, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil || b < a {
				return nil, fmt.Errorf("bad range: %q", tok)
			}
			for i := a; i <= b; i++ {
				out = append(out, i)
			}
		} else {
			id, err := strconv.Atoi(tok)
			if err != nil {
				return nil, fmt.Errorf("bad pid: %q", tok)
			}
			out = append(out, id)
		}
	}
	return out, nil
}

func PrintHostInfo() {
	hn, _ := os.Hostname()
	kernel := uname()
	mem := MemTotalKB()
	fmt.Printf("# host: %s | kernel: %s | cpus: %d | mem: %.1f GiB\n",
		hn, kernel, runtime.NumCPU(), float64(mem)/(1024*1024))
}

func uname() string {
	b, err := os.ReadFile("/proc/version")
	if err == nil {
		return strings.TrimSpace(string(b))
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

func MemTotalKB() uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "MemTotal:") {
			fs := strings.Fields(ln)
			if len(fs) >= 2 {
				v, _ := strconv.ParseUint(fs[1], 10, 64)
				return v
			}
		}
	}
	return 0
}

func FmtFloat(f float64) string {
	// avoid -0.000 and very long tails
	if math.Abs(f) < 0.0005 {
		return "0.000"
	}
	return fmt.Sprintf("%.3f", f)
}

func SystemSummary() (host, kernel, cpus, mem string) {
	// Hostname
	host, _ = os.Hostname()

	// Kernel release
	uname := unix.Utsname{}
	_ = unix.Uname(&uname)
	kernel = charsToString(uname.Release[:])

	// CPUs
	cpus = fmt.Sprintf("%.2f", float64(runtime.NumCPU())/float64(runtime.NumCPU()))

	// Memory
	info := &unix.Sysinfo_t{}
	_ = unix.Sysinfo(info)
	mem = fmt.Sprintf("%.1f%%", float64(info.Totalram)*float64(info.Unit)/(1024*1024*1024))

	return
}

func charsToString(ca []byte) string {
	n := make([]byte, 0, len(ca))
	for _, c := range ca {
		if c == 0 {
			break
		}
		n = append(n, c)
	}
	return string(n)
}

// PidNames resolve process names once (before sampling loop)
func PidNames(pids []int) map[int]string {
	out := make(map[int]string, len(pids))
	for _, pid := range pids {
		name := readComm(pid)
		if name == "" {
			name = readCmdline(pid)
		}
		if name == "" {
			name = fmt.Sprintf("pid %d", pid)
		}
		out[pid] = name
	}
	return out
}

func readComm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readCmdline(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return ""
	}
	parts := strings.Split(string(b), "\x00")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	// take basename of argv[0]
	return filepath.Base(parts[0])
}
