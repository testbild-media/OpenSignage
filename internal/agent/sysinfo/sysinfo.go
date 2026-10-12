// Package sysinfo reads device metrics (CPU, memory, temperature, disk, hardware
// model). Linux reads /proc and /sys; other platforms report what they can.
package sysinfo

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Memory in bytes.
type Memory struct {
	Total     uint64
	Available uint64
}

// Used returns Total − Available.
func (m Memory) Used() uint64 {
	if m.Available > m.Total {
		return 0
	}
	return m.Total - m.Available
}

// procRoot and sysRoot are replaceable in tests.
var (
	procRoot = "/proc"
	sysRoot  = "/sys"
)

// ReadMemory parses /proc/meminfo.
func ReadMemory() (Memory, bool) {
	f, err := os.Open(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		return Memory{}, false
	}
	defer f.Close()
	var m Memory
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			m.Total = v * 1024
		case "MemAvailable:":
			m.Available = v * 1024
		}
	}
	return m, m.Total > 0
}

// Temperature returns the highest thermal zone temperature in °C.
func Temperature() (float64, bool) {
	zones, _ := filepath.Glob(filepath.Join(sysRoot, "class", "thermal", "thermal_zone*", "temp"))
	best, ok := 0.0, false
	for _, z := range zones {
		b, err := os.ReadFile(z)
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil || v <= 0 {
			continue
		}
		c := v / 1000
		if !ok || c > best {
			best, ok = c, true
		}
	}
	return best, ok
}

// CPU measures total CPU usage between calls of Percent.
type CPU struct {
	mu         sync.Mutex
	idle, tot  uint64
	haveSample bool
}

// Percent returns the CPU usage since the previous call (0 on the first call).
func (c *CPU) Percent() float64 {
	idle, total, ok := readCPU()
	if !ok {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { c.idle, c.tot, c.haveSample = idle, total, true }()
	if !c.haveSample || total <= c.tot {
		return 0
	}
	dt := float64(total - c.tot)
	di := float64(idle - c.idle)
	p := (dt - di) / dt * 100
	if p < 0 {
		p = 0
	}
	return float64(int(p*10)) / 10
}

func readCPU() (idle, total uint64, ok bool) {
	f, err := os.Open(filepath.Join(procRoot, "stat"))
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, s := range fields[1:] {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			continue
		}
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}
	return idle, total, true
}

// Model returns the hardware model (device tree on ARM boards, DMI on PCs).
func Model() string {
	for _, p := range []string{
		filepath.Join(procRoot, "device-tree", "model"),
		filepath.Join(sysRoot, "firmware", "devicetree", "base", "model"),
		filepath.Join(sysRoot, "class", "dmi", "id", "product_name"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(strings.TrimRight(string(b), "\x00")); s != "" {
				return s
			}
		}
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

// AutoPerf resolves the "auto" performance profile: Raspberry Pi 3 (and smaller) or
// less than 1.5 GB RAM → low, Raspberry Pi 4/5 → standard, other machines → high.
func AutoPerf() string {
	model := strings.ToLower(Model())
	mem, _ := ReadMemory()
	switch {
	case strings.Contains(model, "raspberry pi 3"), strings.Contains(model, "raspberry pi 2"),
		strings.Contains(model, "raspberry pi zero"):
		return "low"
	case mem.Total > 0 && mem.Total < 1536<<20:
		return "low"
	case strings.Contains(model, "raspberry pi"):
		return "standard"
	}
	return "high"
}

// ProcessTreeRSS sums the resident memory (bytes) of pid and all its descendants. Shared
// pages count once per process, so for multi-process programs like Chromium the sum is far
// above the real use; prefer ProcessTreePSS.
func ProcessTreeRSS(pid int) uint64 {
	var sum uint64
	for _, p := range ProcessTree(pid) {
		sum += processRSS(p)
	}
	return sum
}

// ProcessTreePSS sums the proportional set size (bytes) of pid and all its descendants:
// shared pages are split between the processes that map them, so the sum is the memory the
// tree really uses. Falls back to RSS for processes without smaps_rollup (old kernels).
func ProcessTreePSS(pid int) uint64 {
	var sum uint64
	for _, p := range ProcessTree(pid) {
		if v, ok := processPSS(p); ok {
			sum += v
		} else {
			sum += processRSS(p)
		}
	}
	return sum
}

func processPSS(pid int) (uint64, bool) {
	f, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "smaps_rollup"))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "Pss:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v, err := strconv.ParseUint(fields[1], 10, 64)
				return v * 1024, err == nil
			}
		}
	}
	return 0, false
}

// ProcessTree lists pid and all its descendants (by PPid in /proc).
func ProcessTree(pid int) []int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if pp, ok := parentPID(p); ok {
			children[pp] = append(children[pp], p)
		}
	}
	out := []int{pid}
	for i := 0; i < len(out) && len(out) < 4096; i++ {
		out = append(out, children[out[i]]...)
	}
	sort.Ints(out[1:])
	return out
}

func parentPID(pid int) (int, bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // comm may contain spaces and parentheses
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	pp, err := strconv.Atoi(fields[1])
	return pp, err == nil
}

func processRSS(pid int) uint64 {
	f, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				return v * 1024
			}
		}
	}
	return 0
}

// Uptime of the system in seconds (0 when unknown).
func Uptime() float64 {
	b, err := os.ReadFile(filepath.Join(procRoot, "uptime"))
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// DirSize sums the sizes of the regular files below dir (at most maxFiles files are
// counted; 0 when dir does not exist).
func DirSize(dir string) int64 {
	const maxFiles = 100000
	var n int64
	files := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
			files++
			if files >= maxFiles {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return n
}
