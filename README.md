# Consumption

[![Go Reference](https://pkg.go.dev/badge/github.com/ja7ad/consumption.svg)](https://pkg.go.dev/github.com/ja7ad/consumption)
[![Go Report Card](https://goreportcard.com/badge/github.com/ja7ad/consumption)](https://goreportcard.com/report/github.com/ja7ad/consumption)

A lightweight Linux tool to **monitor processes and estimate their power/energy consumption**  
using CPU utilization, disk I/O, and memory activity as proxies.  

---

## Table of Contents
- [Features](#features)
- [Installation](#installation)
- [Releases](#releases)
- [Usage Examples](#usage-examples)
- [Algorithm](#algorithm)

## Features

* **Process-level monitoring**

    * Accepts single PIDs, multiple PIDs, or ranges (`1000..1010`).
    * Works with process trees via `pstree` expansion.

* **Multiple output formats**

    * Human-readable table (default).
    * CSV and JSON streams for machine processing.
    * Self-contained HTML report with summary and per-tick table.

* **Configurable model**

    * Idle power, max power, and CPU nonlinearity exponent.
    * Disk energy per byte read/write.
    * Memory RSS churn and refault energy.
    * Adjustable idle-share distribution (`--alpha`).

* **Post-processing tools**

    * `calc` subcommand computes averages from a saved CSV/JSON report.

* **Safe defaults**

    * Ships with reasonable coefficients for typical laptop/server workloads.
    * All parameters are overrideable via CLI flags.

---

## Installation

Install directly from source using `go install`:

```bash
go install github.com/ja7ad/consumption/cmd/consumption@latest
````

This will place the `consumption` binary in your `$GOPATH/bin` or `$HOME/go/bin`.

> This tool requires cgroup v1 or v2 with sudo privileges to read process stats.

---

## Releases

Prebuilt binaries are available on the [Releases page](https://github.com/ja7ad/consumption/releases/latest).
Download the latest release for your platform and place it in your `$PATH`.

---

## Usage Examples

### Monitor a single process

```bash
consumption -s 10 -i 1s $(pidof nginx)
```

Collects **10 samples** at **1 second interval** for the `nginx` process.

---

### Monitor a process tree (e.g., JetBrains GoLand IDE)

```bash
consumption -s 20 -i 1s $(pstree -p $(pidof goland) | grep -o '([0-9]\+)' | tr -d '()' | tr '\n' ' ')
```

Expands all child PIDs of GoLand and monitors them together.

---

### Continuous monitoring until stopped

```bash
consumption -s 0 -i 500ms -- $(pidof postgres)
```

Runs **indefinitely** (until `Ctrl-C`), sampling every 500ms.

---

### Save reports to CSV and JSON

```bash
consumption --csv out.csv --json out.json -- $(pidof redis-server)
```

Outputs per-tick rows to both **CSV** and **JSON**.

---

### Generate an HTML summary report

```bash
consumption --html report.html -- $(pidof mysqld)
```

Produces a full HTML report with averages, energy totals, and per-tick data.

---

### Post-process a report file

```bash
consumption calc out.csv
```

Reads an existing CSV/JSON report and calculates average consumption:

```
consumption avg (over 18 samples of ~1s):
- watt (cpu):    0.013 W
- watt (disk):   0.000 W
- watt (ram):    0.000 W
- watt (total):  0.013 W
```

---

## Algorithm

⚠️ Inside a KVM or cloud VM you cannot read real voltage or exact watts of a single process — the hypervisor owns hardware counters (RAPL/PKG/DRAM).  
Instead, **Consumption** builds a *model* that estimates process power by combining CPU utilization, I/O, and memory activity with calibrated coefficients.

### 1. Symbols

- $N$: number of vCPUs
- $\Delta t$: sampling interval (seconds)
- $U_{\text{proc}}$: process CPU utilization fraction  
  $U_{\text{proc}}=\dfrac{\text{CPU time of process in sec during }\Delta t}{N \cdot \Delta t}$
- $U_{\text{vm}}$: total VM CPU utilization fraction  
  $U_{\text{vm}}=\dfrac{\text{CPU time of all tasks}}{N \cdot \Delta t}$
- $P_{\text{idle}}$: idle power in Watts (configurable)
- $P_{\text{max}}$: max power in Watts at 100% utilization
- $\gamma$: CPU nonlinearity exponent
- Disk bytes: $B_r$ (read), $B_w$ (write)
- Memory proxies:
    - `RefaultB` = refaulted bytes
    - `RSSChurnB` = RSS churn bytes
- Energy per byte coefficients:
    - $e_r$ (disk read), $e_w$ (disk write)
    - $e_{\text{ref}}$ (RAM refault), $e_{\text{rss}}$ (RSS churn)

### 2. VM power model

Dynamic VM power grows nonlinearly with utilization:

$$
P_{\text{dyn}}(U) = (P_{\text{max}} - P_{\text{idle}})\cdot U^\gamma
$$

Total VM power at utilization $U_{\text{vm}}$:

$$
P_{\text{vm}}(U_{\text{vm}}) = P_{\text{idle}} + P_{\text{dyn}}(U_{\text{vm}})
$$

### 3. Attribute CPU power to process

Each process is charged a share of VM dynamic power:

$$
P_{\text{cpu,proc}} =
\begin{cases}
\dfrac{U_{\text{proc}}}{U_{\text{vm}}} \cdot P_{\text{dyn}}(U_{\text{vm}}), & U_{\text{vm}}>0 \\
0, & U_{\text{vm}}=0
\end{cases}
$$

### 4. Optional idle power distribution

If policy requires spreading idle cost, an $\alpha$ factor applies:

$$
P_{\text{idle,share}} = \alpha \cdot P_{\text{idle}} \cdot \dfrac{U_{\text{proc}}}{U_{\text{vm}}}
$$

- $\alpha=0$: no idle charged (default)
- $\alpha=1$: full idle proportionally shared

### 5. Disk and memory power

Convert per-byte activity to Joules, then divide by $\Delta t$:

$$
P_{\text{disk}} = \frac{e_r \cdot B_r + e_w \cdot B_w}{\Delta t}
$$

$$
P_{\text{ram}} = \frac{e_{\text{ref}} \cdot \text{RefaultB} + e_{\text{rss}} \cdot \text{RSSChurnB}}{\Delta t}
$$

### 6. Total process power and energy

Total instantaneous power:

$$
P_{\text{proc}} = P_{\text{cpu,proc}} + P_{\text{disk}} + P_{\text{ram}} + P_{\text{idle,share}}
$$

Cumulative energy (Joules) is the time integral:

$$
E_{\text{proc}} = \sum \, P_{\text{proc}} \cdot \Delta t
$$

### 7. Future extensions

- **Network I/O**: add a term $e_n \cdot N_b$ with $e_n$ = energy per byte transferred.
- **Device-specific coefficients**: tune $e_r$, $e_w$, $e_{\text{ref}}$, $e_{\text{rss}}$ for different hardware.
