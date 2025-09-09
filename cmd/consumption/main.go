//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ja7ad/consumption/pkg/system/util"
	"github.com/ja7ad/consumption/pkg/types"
	"github.com/spf13/cobra"

	"github.com/ja7ad/consumption/pkg/consumption"
	"github.com/ja7ad/consumption/pkg/system/proc"
)

var (
	csvF *os.File

	pretty bool
	warmup int
)

type pidInfo struct {
	PID  int
	Name string
}

type opts struct {
	// sampling
	samples  int
	interval time.Duration
	ema      float64

	// model
	pIdle   float64
	pMax    float64
	gamma   float64
	er      float64
	ew      float64
	eMemRef float64
	eMemRSS float64
	alpha   float64

	// outputs
	csvPath  string
	jsonPath string
	htmlPath string
}

type row struct {
	At          time.Time   `json:"time"`
	UVm         float64     `json:"u_vm"`
	UProc       float64     `json:"u_proc"`
	PCPU        float64     `json:"p_cpu_w"`
	PDisk       float64     `json:"p_disk_w"`
	PRAM        float64     `json:"p_ram_w"`
	PIdleShare  float64     `json:"p_idle_share_w"`
	PTotal      float64     `json:"p_total_w"`
	EnergyCumJ  float64     `json:"e_cum_j"`
	ReadBytes   types.Bytes `json:"read_bytes"`
	WriteBytes  types.Bytes `json:"write_bytes"`
	RefaultB    types.Bytes `json:"refault_bytes"`
	RSSChurnB   types.Bytes `json:"rss_churn_bytes"`
	IntervalSec float64     `json:"interval_sec"`
}

func main() {
	var o opts

	root := &cobra.Command{
		Use:   "consumption [PID|PID..PID]...",
		Short: "Process power/energy estimation service",
		Long: `The consumption tool monitors Linux processes (by PID or process-tree)
and estimates their resource-based power draw (CPU, disk I/O, RAM proxies).
It samples utilization via /proc or cgroup (v1/v2) and applies a configurable
model to compute instantaneous watts and cumulative joules.
		
Copyright (c) 2024 Javad Rajabzadeh Inc. All rights reserved.
		
* GitHub: https://github.com/ja7ad/consumption

Examples:
  consumption -s 20 -i 1s $(pstree -p $(pidof goland) | grep -o '([0-9]\+)' | tr -d '()' | tr '\n' ' ')
  consumption --csv out.csv --json out.json 12345 23456 30000..30032`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), o, args)
		},
	}

	root.Flags().BoolVar(&pretty, "pretty", true, "format output as a table instead of CSV-like lines")
	root.Flags().IntVar(&warmup, "warmup", 1, "number of initial samples to skip from display and averages")
	root.Flags().IntVarP(&o.samples, "samples", "s", 5, "number of samples to collect (0 = run until Ctrl-C)")
	root.Flags().DurationVarP(&o.interval, "interval", "i", time.Second, "sampling interval (e.g. 1s, 500ms)")
	root.Flags().Float64Var(&o.ema, "ema", 0.5, "EMA alpha for VM utilization smoothing [0..1]")

	root.Flags().Float64Var(&o.pIdle, "p-idle", 5.0, "idle power in Watts")
	root.Flags().Float64Var(&o.pMax, "p-max", 20.0, "max power in Watts at 100% utilization")
	root.Flags().Float64Var(&o.gamma, "gamma", 1.3, "CPU nonlinearity exponent")
	root.Flags().Float64Var(&o.er, "er", 4.8e-8, "disk read energy per byte (J/B)")
	root.Flags().Float64Var(&o.ew, "ew", 9.5e-8, "disk write energy per byte (J/B)")
	root.Flags().Float64Var(&o.eMemRef, "e-mem-ref", 7e-10, "RAM refault energy per byte (J/B)")
	root.Flags().Float64Var(&o.eMemRSS, "e-mem-rss", 3e-10, "RAM RSS churn energy per byte (J/B)")
	root.Flags().Float64Var(&o.alpha, "alpha", 0.0, "fraction of idle to charge proportionally [0..1]")

	root.Flags().StringVar(&o.csvPath, "csv", "", "write per-tick rows to CSV file")
	root.Flags().StringVar(&o.jsonPath, "json", "", "write per-tick rows to JSON file")
	root.Flags().StringVar(&o.htmlPath, "html", "", "write per-tick rows and summary to HTML file")

	if err := root.Execute(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, o opts, args []string) error {
	pids, err := util.ParsePIDs(args)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return fmt.Errorf("no PIDs provided")
	}
	if o.interval <= 0 {
		return fmt.Errorf("interval must be > 0")
	}
	if o.ema < 0 || o.ema > 1 {
		return fmt.Errorf("ema must be in [0,1]")
	}
	if o.alpha < 0 || o.alpha > 1 {
		return fmt.Errorf("alpha must be in [0,1]")
	}

	// Print a little host header like the bash script vibe
	host, kernel, cpus, mem := util.SystemSummary()
	fmt.Printf(_console, host, kernel, cpus, mem, time.Now().Format("2006-01-02 15:04:05"))

	// Build config & components
	cfg := consumption.Config{
		PIdle:   o.pIdle,
		PMax:    o.pMax,
		Gamma:   o.gamma,
		ER:      o.er,
		EW:      o.ew,
		EMemRef: o.eMemRef,
		EMemRSS: o.eMemRSS,
		Alpha:   o.alpha,
	}
	acc := consumption.New(&cfg)

	col, err := proc.NewCollector(o.ema)
	if err != nil {
		return fmt.Errorf("collector: %w", err)
	}
	defer col.Close()

	var tw *tabwriter.Writer
	if pretty {
		tw = newTable()
		printTableHeader(tw)
	} else {
		fmt.Println("# time, U_vm, U_proc, P_cpu(W), P_disk(W), P_ram(W), P_total(W), E_cum(J)")
	}

	// file outputs
	var (
		csvW  *csv.Writer
		jsonF *os.File
		htmlF *os.File
	)
	if o.csvPath != "" {
		if err := os.MkdirAll(filepath.Dir(o.csvPath), 0o755); err == nil {
			if f, er := os.Create(o.csvPath); er == nil {
				csvF = f                // keep file
				csvW = csv.NewWriter(f) // wrap writer
				_ = csvW.Write([]string{
					"time", "u_vm", "u_proc", "p_cpu_w", "p_disk_w", "p_ram_w", "p_total_w",
					"e_cum_j", "read_bytes", "write_bytes", "refault_bytes", "rss_churn_bytes", "interval_sec",
				})
				csvW.Flush()
			}
		}
	}
	if o.jsonPath != "" {
		if err := os.MkdirAll(filepath.Dir(o.jsonPath), 0o755); err == nil {
			jsonF, _ = os.Create(o.jsonPath)
			if jsonF != nil {
				_, _ = jsonF.WriteString("[\n")
			}
		}
	}
	if o.htmlPath != "" {
		if err := os.MkdirAll(filepath.Dir(o.htmlPath), 0o755); err == nil {
			htmlF, _ = os.Create(o.htmlPath)
		}
	}

	// We’ll collect rows for JSON/HTML finalization
	var rows []row

	// Ctrl-C handling
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()

	writeN := 0 // number of rows actually written (used for JSON commas)

	sampleN := 0
	for {
		select {
		case <-ctx.Done():
			slog.Info("interrupted")
			goto END

		case <-ticker.C:
			dt := o.interval.Seconds()

			snap, err := col.Sample(pids, dt)
			if err != nil {
				if errorsIsAny(err, proc.ErrAllExited) {
					fmt.Println("# All PIDs exited")
					goto END
				}
				slog.Warn("sample error", "err", err)
				continue
			}

			sampleN++

			// --- Warmup: skip printing and accumulation
			if warmup > 0 && sampleN <= warmup {
				continue
			}

			// Only now mutate the accumulator
			res := acc.Apply(snap)

			// idle share (for CSV/JSON/HTML completeness)
			var pidleShare float64
			if snap.UVm > 1e-12 && cfg.Alpha > 0 {
				share := snap.UProc / snap.UVm
				if share < 0 {
					share = 0
				} else if share > 1 {
					share = 1
				}
				pidleShare = cfg.Alpha * cfg.PIdle * share
			}

			now := time.Now()

			// stdout
			if pretty {
				printTableRow(tw, now, snap.UVm, snap.UProc, res.PCPU, res.PDisk, res.PRAM, res.PTotal, acc.EnergyCumJ())
			} else {
				printCsvLike(now.Format(time.RFC3339), snap.UVm, snap.UProc, res.PCPU, res.PDisk, res.PRAM, res.PTotal, acc.EnergyCumJ())
			}

			// row for files
			r := row{
				At:          now,
				UVm:         util.Clamp01(snap.UVm),
				UProc:       util.Clamp01(snap.UProc),
				PCPU:        res.PCPU,
				PDisk:       res.PDisk,
				PRAM:        res.PRAM,
				PIdleShare:  pidleShare,
				PTotal:      res.PTotal,
				EnergyCumJ:  acc.EnergyCumJ(),
				ReadBytes:   snap.ReadBytes,
				WriteBytes:  snap.WriteBytes,
				RefaultB:    snap.RefaultBytes,
				RSSChurnB:   snap.RSSChurnBytes,
				IntervalSec: dt,
			}
			rows = append(rows, r)

			// CSV row
			if csvW != nil {
				_ = csvW.Write([]string{
					now.Format(time.RFC3339),
					util.FmtFloat(r.UVm), util.FmtFloat(r.UProc),
					util.FmtFloat(r.PCPU), util.FmtFloat(r.PDisk), util.FmtFloat(r.PRAM),
					util.FmtFloat(r.PTotal), util.FmtFloat(r.EnergyCumJ),
					strconv.FormatUint(r.ReadBytes.ToUin64(), 10),
					strconv.FormatUint(r.WriteBytes.ToUin64(), 10),
					strconv.FormatUint(r.RefaultB.ToUin64(), 10),
					strconv.FormatUint(r.RSSChurnB.ToUin64(), 10),
					util.FmtFloat(r.IntervalSec),
				})
				csvW.Flush()
			}

			// JSON streaming (comma separated)
			if jsonF != nil {
				b, _ := json.MarshalIndent(r, "  ", "  ")
				if writeN > 0 {
					_, _ = jsonF.WriteString(",\n")
				}
				_, _ = jsonF.Write(b)
				writeN++
			}

			// stop condition counts only post-warmup samples
			if o.samples > 0 && (sampleN-warmup) >= o.samples {
				goto END
			}
		}
	}

END:
	// finalize files
	if csvW != nil {
		csvW.Flush()
	}
	if csvF != nil {
		_ = csvF.Close()
	}
	if jsonF != nil {
		_, _ = jsonF.WriteString("\n]\n")
		_ = jsonF.Close()
	}

	if htmlF != nil {
		names := util.PidNames(pids)

		if err := writeHTML(htmlF, rows, acc.Averages(), acc.EnergyCumJ(), names); err != nil {
			slog.Error("write html", "err", err)
		}
		_ = htmlF.Close()
	}

	avg := acc.Averages()
	fmt.Println()
	fmt.Printf("consumption avg (over %d samples of ~%s):\n", sampleN, o.interval)
	fmt.Printf("- watt (cpu):    %.3f W\n", avg.PCPU)
	fmt.Printf("- watt (disk):   %.3f W\n", avg.PDisk)
	fmt.Printf("- watt (ram):    %.3f W\n", avg.PRAM)
	fmt.Printf("- watt (total):  %.3f W\n", avg.PTotal)
	fmt.Println()

	return nil
}

func errorsIsAny(err error, targets ...error) bool {
	for _, t := range targets {
		if t != nil && (errors.Is(t, err) || (t != nil && errorsIs(err, t))) {
			return true
		}
	}
	return false
}

func errorsIs(err, target error) bool {
	// Go 1.20+: errors.Is; inline to avoid import noise here
	type causer interface{ Unwrap() error }
	for {
		if errors.Is(err, target) {
			return true
		}
		x, ok := err.(causer)
		if !ok {
			return false
		}
		err = x.Unwrap()
	}
}

func writeHTML(f *os.File, rows []row, avg consumption.Result, energy float64, names map[int]string) error {
	type view struct {
		Rows   []row
		Avg    consumption.Result
		Energy float64
		PIDs   []pidInfo
	}

	var pidList []pidInfo
	for pid, name := range names {
		pidList = append(pidList, pidInfo{PID: pid, Name: name})
	}
	// optional: stable order
	slices.SortFunc(pidList, func(a, b pidInfo) int {
		switch {
		case a.PID < b.PID:
			return -1
		case a.PID > b.PID:
			return 1
		default:
			return 0
		}
	})

	var buf bytes.Buffer
	data := view{
		Rows:   rows,
		Avg:    avg,
		Energy: energy,
		PIDs:   pidList,
	}
	if err := tpl.Execute(&buf, data); err != nil {
		return err
	}
	_, err := f.Write(buf.Bytes())
	return err
}

func newTable() *tabwriter.Writer {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	return tw
}

func printTableHeader(tw *tabwriter.Writer) {
	fmt.Fprintln(tw, "TIME\tU_vm\tU_proc\tP_cpu (W)\tP_disk (W)\tP_ram (W)\tP_total (W)\tE_cum (J)")
	fmt.Fprintln(tw, "----\t----\t------\t---------\t----------\t---------\t-----------\t---------")
	tw.Flush()
}

func printTableRow(tw *tabwriter.Writer, ts time.Time, uvm, up, pcpu, pdisk, pram, ptotal, ecum float64) {
	// fixed decimals; aligned by tabs; no thousands separators to keep it simple/portable
	fmt.Fprintf(tw, "%s\t%.4f\t%.4f\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\n",
		ts.Format("2006-01-02 15:04:05"), util.Clamp01(uvm), util.Clamp01(up),
		pcpu, pdisk, pram, ptotal, ecum,
	)
	tw.Flush()
}

func printCsvLike(now string, uvm, up, pcpu, pdisk, pram, ptotal, ecum float64) {
	fmt.Printf("%s, %.4f, %.4f, %.3f, %.3f, %.3f, %.3f, %.3f\n",
		now, util.Clamp01(uvm), util.Clamp01(up), pcpu, pdisk, pram, ptotal, ecum)
}

var tpl = template.Must(template.New("rep").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8">
<title>Consumption Report</title>
<style>
body{font-family:system-ui,Segoe UI,Roboto,Helvetica,Arial,sans-serif;margin:20px}
h1,h2{margin:0 0 8px}
table{border-collapse:collapse;width:100%;font-size:14px}
th,td{border:1px solid #ddd;padding:6px 8px;text-align:right}
th:first-child,td:first-child{text-align:left}
ul{margin:6px 0 14px;padding-left:20px}
code{background:#f5f5f5;padding:2px 4px;border-radius:4px}
.small{color:#555}
.badge{display:inline-block;background:#eef;border:1px solid #ccd;padding:2px 6px;border-radius:6px;margin-right:6px;}
</style>

<h1><a href="https://github.com/ja7ad/consumption" target="_blank" rel="noopener noreferrer" style="color:inherit;text-decoration:none;">Consumption Report</a></h1>

<p class="small">
Rows: {{len .Rows}} &nbsp;|&nbsp;
Avg P(total): {{printf "%.3f" .Avg.PTotal}} W &nbsp;|&nbsp;
Energy: {{printf "%.3f" .Energy}} J
</p>

{{if .PIDs}}
<h2>Processes</h2>
<ul>
{{range .PIDs}}
  <li><span class="badge">PID {{.PID}}</span> {{.Name}}</li>
{{end}}
</ul>
{{end}}

<h2>Summary</h2>
<ul>
<li>Avg P(cpu): {{printf "%.3f" .Avg.PCPU}} W</li>
<li>Avg P(disk): {{printf "%.3f" .Avg.PDisk}} W</li>
<li>Avg P(ram): {{printf "%.3f" .Avg.PRAM}} W</li>
<li>Avg P(total): {{printf "%.3f" .Avg.PTotal}} W</li>
<li>Energy: {{printf "%.3f" .Energy}} J</li>
</ul>

<h2>Per-tick</h2>
<table>
<thead>
<tr>
<th>time</th><th>U_vm</th><th>U_proc</th>
<th>P_cpu(W)</th><th>P_disk(W)</th><th>P_ram(W)</th><th>P_total(W)</th><th>E_cum(J)</th>
<th>read B</th><th>write B</th><th>refault B</th><th>rssΔ B</th>
</tr>
</thead>
<tbody>
{{range .Rows}}
<tr>
<td style="text-align:left">{{.At.Format "2006-01-02 15:04:05"}}</td>
<td>{{printf "%.4f" .UVm}}</td>
<td>{{printf "%.4f" .UProc}}</td>
<td>{{printf "%.3f" .PCPU}}</td>
<td>{{printf "%.3f" .PDisk}}</td>
<td>{{printf "%.3f" .PRAM}}</td>
<td>{{printf "%.3f" .PTotal}}</td>
<td>{{printf "%.3f" .EnergyCumJ}}</td>
<td>{{.ReadBytes}}</td>
<td>{{.WriteBytes}}</td>
<td>{{.RefaultB}}</td>
<td>{{.RSSChurnB}}</td>
</tr>
{{end}}
</tbody>
</table>
</html>`))

const _console = `Consumption - Process Power/Energy Estimation Tool
Copyright (c) 2024 Javad Rajabzadeh Inc. All rights reserved.

* GitHub: https://github.com/ja7ad/consumption

       Host: %s
       Kernel: %s 
       CPUs: %s
       Mem: %s

Consumption report as of %s:

`
