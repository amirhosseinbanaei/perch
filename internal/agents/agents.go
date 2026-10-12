// Package agents finds the coding agent running in a tmux pane and reports its status, session
// id, title and working path. It is the only package that reads agent state files.
//
// The detector table (Table) holds one entry per agent. Adding an agent is one new entry: a
// matcher built from its executable and package names, and an Enrich function, usually the
// shared stateful enricher configured with the agent's state directory and screen patterns.
package agents

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Status is a closed set.
type Status string

// The statuses. Waiting means the agent needs a person: a permission or trust dialog, or a
// question.
const (
	Starting Status = "starting"
	Working  Status = "working"
	Waiting  Status = "waiting"
	Idle     Status = "idle"
	Exited   Status = "exited"
	Unknown  Status = "unknown"
)

// Statuses lists every status, in the order the CLI documents them.
func Statuses() []Status { return []Status{Starting, Working, Waiting, Idle, Exited, Unknown} }

// Where a field came from.
const (
	SrcRegistry  = "registry"   // Claude Code's ~/.claude/sessions/<pid>.json
	SrcArgv      = "argv"       // the process's own arguments
	SrcStateDir  = "state-dir"  // a session or transcript file in the agent's state directory
	SrcScreen    = "screen"     // the pane's visible screen
	SrcPaneTitle = "pane-title" // tmux #{pane_title}
	SrcActivity  = "activity"   // tmux #{window_activity} or the process's age
	SrcProcCwd   = "proc-cwd"   // /proc/<pid>/cwd
	SrcPanePath  = "pane-path"  // tmux #{pane_current_path}
)

// Confidence levels.
const (
	High = "high"
	Low  = "low"
)

// Info is what a detector reports for one pane.
type Info struct {
	Agent       string            `json:"agent"`
	DisplayName string            `json:"displayName"`
	Status      Status            `json:"status"`
	SessionID   string            `json:"sessionId"`
	Title       string            `json:"title"`
	Path        string            `json:"path"`
	PID         int               `json:"pid"`
	StartedAt   time.Time         `json:"startedAt"`
	Source      map[string]string `json:"source"` // field → where it came from
	Confidence  string            `json:"confidence"`
	Note        string            `json:"note,omitempty"`     // why a field is missing, or what the dialog is
	ExitedAt    *time.Time        `json:"exitedAt,omitempty"` // set while an exited agent is still shown
}

// SourceString renders Source as "status=registry id=registry …" in a fixed order.
func (i Info) SourceString() string {
	var parts []string
	for _, k := range []string{"agent", "status", "sessionId", "title", "path"} {
		if v, ok := i.Source[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

// Pane is what a detector sees of the tmux pane its process runs in.
type Pane struct {
	ID       string
	Path     string // #{pane_current_path}
	Title    string // #{pane_title}
	Activity time.Time
	// Screen returns the visible screen (capture-pane without history). The snapshot package
	// supplies it, so this package never runs tmux itself.
	Screen func(ctx context.Context) string
}

// Detector is one agent's entry in the table.
type Detector struct {
	ID          string
	DisplayName string
	// Executables are the names `new --agent` and `doctor` look for on PATH.
	Executables []string
	Match       func(p Proc) bool
	Enrich      func(ctx context.Context, in *Inspector, p Proc, pane Pane) Info
}

// Inspector holds what detectors share across refreshes: the environment, the clock and the
// transcript-title cache. It is safe for concurrent use.
type Inspector struct {
	Home   string
	Getenv func(string) string
	Now    func() time.Time

	mu     sync.Mutex
	titles map[string]titleEntry // by transcript path
	paths  map[string]string     // Claude sessionId → transcript path
}

// NewInspector uses the process environment and the real clock.
func NewInspector() *Inspector {
	home, _ := os.UserHomeDir()
	return &Inspector{Home: home, Getenv: os.Getenv, Now: time.Now}
}

func (in *Inspector) env(k string) string {
	if in.Getenv == nil {
		return ""
	}
	return in.Getenv(k)
}

func (in *Inspector) now() time.Time {
	if in.Now == nil {
		return time.Now()
	}
	return in.Now()
}

// Detect walks the pane's process tree, nearest first, and returns the first process a detector
// matches, enriched. nil means no agent runs in the pane.
func (in *Inspector) Detect(ctx context.Context, table []Detector, procs *ProcTable, panePID int, pane Pane) *Info {
	for _, pid := range procs.Descendants(panePID) {
		p, ok := procs.Get(pid)
		if !ok {
			continue
		}
		for _, d := range table {
			if !d.Match(p) {
				continue
			}
			p = procs.Detail(ctx, p, in.now())
			p.Descendants = procs.Descendants(p.PID)[1:]
			info := d.Enrich(ctx, in, p, pane)
			info.Agent, info.DisplayName, info.PID, info.StartedAt = d.ID, d.DisplayName, p.PID, p.StartedAt
			if info.Source == nil {
				info.Source = map[string]string{}
			}
			info.Source["agent"] = SrcArgv
			if info.SessionID == "" {
				// Never a guessed id: say why there is none.
				why := info.Source["sessionId"]
				if why == "" {
					why = "unknown"
				}
				info.Source["sessionId"] = "none (" + why + ")"
			}
			if info.Status == "" {
				info.Status = Unknown
			}
			return &info
		}
	}
	return nil
}

// Lookup returns the detector with this id.
func Lookup(table []Detector, id string) (Detector, bool) {
	for _, d := range table {
		if d.ID == id {
			return d, true
		}
	}
	return Detector{}, false
}

// ── matching ─────────────────────────────────────────────────────────────────

var runModuleRE = regexp.MustCompile(`runpy\.run_module\(\s*['"]([\w.]+)['"]`)

// interpreters run a script named by a later argument.
var interpreterRE = regexp.MustCompile(`^(node|nodejs|bun|deno|uv|python|python[23](\.\d+)?)$`)

// matcher matches a process by the start of its args only, never by a substring anywhere:
// argv[0]'s basename is a known name; or argv[0] is an interpreter and the script it runs has a
// known basename, sits under a known package directory, or (python -m) is a known module.
type matcher struct {
	names    []string         // executable basenames
	patterns []*regexp.Regexp // more executable basenames, e.g. platform-suffixed binaries
	packages []string         // package path, matched by whole path segments: "@openai/codex"
	modules  []string         // python -m module, or its first dotted part
}

func (m matcher) match(p Proc) bool {
	if len(p.Args) == 0 {
		return false
	}
	exe := path.Base(p.Args[0])
	if m.exe(exe) {
		return true
	}
	if !interpreterRE.MatchString(exe) {
		return false
	}
	script, module := scriptOf(exe, p.Args[1:])
	if module != "" {
		for _, mod := range m.modules {
			if module == mod || strings.HasPrefix(module, mod+".") {
				return true
			}
		}
		return false
	}
	if script == "" {
		return false
	}
	if m.exe(path.Base(script)) {
		return true
	}
	return m.inPackage(script)
}

func (m matcher) exe(base string) bool {
	for _, n := range m.names {
		if base == n {
			return true
		}
	}
	for _, re := range m.patterns {
		if re.MatchString(base) {
			return true
		}
	}
	return false
}

// inPackage reports whether a script path runs through one of the package directories, compared
// segment by segment ("…/node_modules/@openai/codex/bin/codex.js").
func (m matcher) inPackage(script string) bool {
	segs := strings.Split(filepath.ToSlash(script), "/")
	for _, pkg := range m.packages {
		want := strings.Split(pkg, "/")
		for i := 0; i+len(want) <= len(segs); i++ {
			ok := true
			for j, w := range want {
				if segs[i+j] != w {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}

// scriptOf finds what an interpreter runs: the first argument that is not an option (and not a
// runner subcommand such as `uv run`), or the module after python's -m.
func scriptOf(interp string, args []string) (script, module string) {
	sub := map[string]bool{}
	switch interp {
	case "uv":
		sub = map[string]bool{"run": true, "tool": true}
	case "deno", "bun":
		sub = map[string]bool{"run": true, "x": true}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-m" && strings.HasPrefix(interp, "python"):
			if i+1 < len(args) {
				return "", args[i+1]
			}
			return "", ""
		case a == "-c" && strings.HasPrefix(interp, "python"):
			// A launcher that runs a module from inline code (Hermes' source install does
			// `python -I -c "…runpy.run_module('hermes_cli.main', …)"`): only the module named
			// in that one call counts, never other text in the code.
			if i+1 < len(args) {
				if m := runModuleRE.FindStringSubmatch(args[i+1]); m != nil {
					return "", m[1]
				}
			}
			return "", ""
		case a == "--":
			continue
		case strings.HasPrefix(a, "-"):
			continue
		case sub[a]:
			delete(sub, a)
			continue
		default:
			return a, ""
		}
	}
	return "", ""
}

// ── shared helpers ───────────────────────────────────────────────────────────

// activityStatus is the fallback when nothing better is known: screen activity within the last
// 3 s means working.
func activityStatus(now, activity time.Time) Status {
	if activity.IsZero() {
		return Unknown
	}
	if now.Sub(activity) <= 3*time.Second {
		return Working
	}
	return Idle
}

// pathOf picks the working path: the process's cwd, else the pane's.
func pathOf(p Proc, pane Pane, info *Info) {
	switch {
	case p.Cwd != "":
		info.Path, info.Source["path"] = p.Cwd, SrcProcCwd
	case pane.Path != "":
		info.Path, info.Source["path"] = pane.Path, SrcPanePath
	}
}

func screenOf(ctx context.Context, pane Pane) string {
	if pane.Screen == nil {
		return ""
	}
	return pane.Screen(ctx)
}

// Table returns the five detectors, in the order they are tried for each process.
func Table() []Detector {
	return []Detector{claudeDetector(), codexDetector(), hermesDetector(), geminiDetector(), opencodeDetector()}
}
