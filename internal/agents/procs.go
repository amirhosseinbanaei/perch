package agents

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Proc is one process as a detector sees it.
type Proc struct {
	PID       int       `json:"pid"`
	PPID      int       `json:"ppid"`
	Args      []string  `json:"args"`
	Cwd       string    `json:"cwd,omitempty"`       // /proc/<pid>/cwd on Linux
	StartedAt time.Time `json:"startedAt,omitempty"` // zero when unknown
	// StartTicks is field 22 of /proc/<pid>/stat on Linux. Claude Code records the same value
	// as "procStart" in its session registry, which tells a reused pid from the real one.
	StartTicks string `json:"-"`
	// Descendants of a matched process, nearest first: an agent's registry may name a child
	// (Hermes' TUI gateway) rather than the process the matcher found.
	Descendants []int `json:"-"`
}

// ProcTable is one `ps -A -o pid=,ppid=,args=` run, indexed for walking a pane's descendants.
type ProcTable struct {
	procs    map[int]*Proc
	children map[int][]int
	procRoot string    // "/proc" on Linux, "" elsewhere
	bootTime time.Time // from /proc/stat btime
	fixed    bool      // built by NewProcTable: never look at the real system
}

// ListProcs runs ps once.
func ListProcs(ctx context.Context) (*ProcTable, error) {
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps -A: %w", err)
	}
	t := ParsePS(string(out))
	if runtime.GOOS == "linux" {
		t.procRoot = "/proc"
		t.bootTime = readBootTime("/proc/stat")
	}
	return t, nil
}

// ParsePS parses `ps -o pid=,ppid=,args=` output. Args split on whitespace is only a fallback:
// on Linux the exact argv comes from /proc/<pid>/cmdline when a process is looked at.
func ParsePS(out string) *ProcTable {
	t := &ProcTable{procs: map[int]*Proc{}, children: map[int][]int{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		t.procs[pid] = &Proc{PID: pid, PPID: ppid, Args: f[2:]}
		t.children[ppid] = append(t.children[ppid], pid)
	}
	for _, c := range t.children {
		sort.Ints(c)
	}
	return t
}

// NewProcTable builds a table from fixed processes, for tests.
func NewProcTable(procs ...Proc) *ProcTable {
	t := &ProcTable{procs: map[int]*Proc{}, children: map[int][]int{}, fixed: true}
	for i := range procs {
		p := procs[i]
		t.procs[p.PID] = &p
		t.children[p.PPID] = append(t.children[p.PPID], p.PID)
	}
	for _, c := range t.children {
		sort.Ints(c)
	}
	return t
}

// Limits on a pane's process walk, so a fork bomb in a pane cannot stall a refresh.
const (
	maxDepth       = 8
	maxDescendants = 256
)

// Descendants returns pid and its descendants, breadth first: nearest the pane first.
func (t *ProcTable) Descendants(pid int) []int {
	if _, ok := t.procs[pid]; !ok {
		return nil
	}
	out := []int{pid}
	level := []int{pid}
	for depth := 0; depth < maxDepth && len(level) > 0 && len(out) < maxDescendants; depth++ {
		var next []int
		for _, p := range level {
			next = append(next, t.children[p]...)
		}
		out = append(out, next...)
		level = next
	}
	if len(out) > maxDescendants {
		out = out[:maxDescendants]
	}
	return out
}

// Get returns a copy of a process, with its exact argv from /proc/<pid>/cmdline on Linux. The
// table is never written after ParsePS, so panes can be inspected concurrently.
func (t *ProcTable) Get(pid int) (Proc, bool) {
	q, ok := t.procs[pid]
	if !ok {
		return Proc{}, false
	}
	p := *q
	if t.procRoot != "" {
		if b, err := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(pid), "cmdline")); err == nil && len(b) > 0 {
			p.Args = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		}
	}
	return p, true
}

// Detail fills in the cwd and start time of a matched process: from /proc on Linux, and the
// start time through `ps -o etime=` elsewhere.
func (t *ProcTable) Detail(ctx context.Context, p Proc, now time.Time) Proc {
	if t.fixed {
		return p
	}
	if t.procRoot != "" {
		dir := filepath.Join(t.procRoot, strconv.Itoa(p.PID))
		p.Cwd, _ = os.Readlink(filepath.Join(dir, "cwd"))
		if b, err := os.ReadFile(filepath.Join(dir, "stat")); err == nil {
			p.StartTicks = statField(string(b), 22)
			if ticks, err := strconv.ParseInt(p.StartTicks, 10, 64); err == nil && !t.bootTime.IsZero() {
				// USER_HZ is 100 on every Linux architecture Go targets here.
				p.StartedAt = t.bootTime.Add(time.Duration(ticks) * (time.Second / 100))
			}
		}
		return p
	}
	if !p.StartedAt.IsZero() {
		return p
	}
	out, err := exec.CommandContext(ctx, "ps", "-o", "etime=", "-p", strconv.Itoa(p.PID)).Output()
	if err != nil {
		return p
	}
	if d, ok := ParseEtime(strings.TrimSpace(string(out))); ok {
		p.StartedAt = now.Add(-d)
	}
	return p
}

// ParseEtime parses ps etime: [[dd-]hh:]mm:ss.
func ParseEtime(s string) (time.Duration, bool) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, false
		}
		total = total*60 + n
	}
	return time.Duration(days*86400+total) * time.Second, true
}

// statField returns field n (1-based, as in proc(5)) of /proc/<pid>/stat. The command name in
// field 2 may hold spaces and parentheses, so fields are counted after its last ")".
func statField(stat string, n int) string {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 || n < 3 {
		return ""
	}
	f := strings.Fields(stat[i+1:])
	if n-3 >= len(f) {
		return ""
	}
	return f[n-3]
}

func readBootTime(path string) time.Time {
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if v, ok := bytes.CutPrefix(line, []byte("btime ")); ok {
			if n, err := strconv.ParseInt(string(bytes.TrimSpace(v)), 10, 64); err == nil {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}
