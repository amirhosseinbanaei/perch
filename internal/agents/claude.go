package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Claude Code, checked on this machine against version 2.1.296 (2026-10-12):
// https://github.com/anthropics/claude-code — the executable is a native ELF binary named
// "claude" (~/.local/bin/claude, or the VS Code extension's .../native-binary/claude); the older
// npm install runs node on @anthropic-ai/claude-code/cli.js.
//
// Each running process writes $CLAUDE_CONFIG_DIR/sessions/<pid>.json (default ~/.claude) with
// pid, sessionId, cwd, startedAt, procStart, name, status ("busy" | "idle"). The transcript is
// projects/<cwd-slug>/<sessionId>.jsonl; its {"type":"custom-title"} and {"type":"ai-title"}
// lines carry the titles, the last of each winning.
func claudeDetector() Detector {
	m := matcher{names: []string{"claude"}, packages: []string{"@anthropic-ai/claude-code"}}
	return Detector{
		ID:          "claude",
		DisplayName: "Claude Code",
		Executables: []string{"claude"},
		Match:       m.match,
		Enrich:      enrichClaude,
	}
}

// claudeStartGrace is how long a new process may run without a registry file and still be
// "starting".
const claudeStartGrace = 5 * time.Second

type claudeRegistry struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`
	ProcStart string `json:"procStart"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

// ClaudeDir is $CLAUDE_CONFIG_DIR, else ~/.claude.
func (in *Inspector) ClaudeDir() string {
	if d := in.env("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(in.Home, ".claude")
}

func (in *Inspector) claudeRegistry(p Proc) (claudeRegistry, bool) {
	b, err := os.ReadFile(filepath.Join(in.ClaudeDir(), "sessions", strconv.Itoa(p.PID)+".json"))
	if err != nil {
		return claudeRegistry{}, false
	}
	var r claudeRegistry
	if json.Unmarshal(b, &r) != nil || r.SessionID == "" {
		return claudeRegistry{}, false
	}
	// A file left behind by a dead process whose pid was reused is not this process's.
	if r.PID != 0 && r.PID != p.PID {
		return claudeRegistry{}, false
	}
	if r.ProcStart != "" && p.StartTicks != "" && r.ProcStart != p.StartTicks {
		return claudeRegistry{}, false
	}
	return r, true
}

// claudeArgvSession reads --resume/-r/--session-id from argv.
func claudeArgvSession(args []string) string {
	return flagValue(args, []string{"--resume", "-r", "--session-id"}, uuidRE)
}

func enrichClaude(ctx context.Context, in *Inspector, p Proc, pane Pane) Info {
	info := Info{Source: map[string]string{}, Confidence: High, Status: Unknown}
	now := in.now()
	reg, ok := in.claudeRegistry(p)
	switch {
	case ok:
		info.SessionID, info.Source["sessionId"] = reg.SessionID, SrcRegistry
		if reg.Cwd != "" {
			info.Path, info.Source["path"] = reg.Cwd, SrcRegistry
		}
		switch reg.Status {
		case "busy":
			info.Status, info.Source["status"] = Working, SrcRegistry
		case "idle":
			info.Status, info.Source["status"] = Idle, SrcRegistry
		}
		if t, ok := in.claudeTitle(reg.SessionID); ok {
			info.Title, info.Source["title"] = t, SrcStateDir
		} else if reg.Name != "" {
			info.Title, info.Source["title"] = reg.Name, SrcRegistry
		}
	case !p.StartedAt.IsZero() && now.Sub(p.StartedAt) < claudeStartGrace:
		info.Status, info.Source["status"] = Starting, SrcActivity
		info.Source["sessionId"] = "no session registry yet"
	default:
		if id := claudeArgvSession(p.Args); id != "" {
			info.SessionID, info.Source["sessionId"] = id, SrcArgv
		} else {
			info.Source["sessionId"] = "no " + filepath.Join(in.ClaudeDir(), "sessions", strconv.Itoa(p.PID)+".json")
		}
	}
	if info.SessionID != "" && info.Title == "" {
		if t, ok := in.claudeTitle(info.SessionID); ok {
			info.Title, info.Source["title"] = t, SrcStateDir
		}
	}

	if kind, msg, ok := ClaudeDialog(screenOf(ctx, pane)); ok {
		info.Status, info.Source["status"] = Waiting, SrcScreen
		info.Note = kind + ": " + msg
	}
	if info.Status == Unknown {
		info.Status, info.Source["status"] = activityStatus(now, pane.Activity), SrcActivity
		info.Confidence = Low
	}
	if info.Title == "" && pane.Title != "" {
		info.Title, info.Source["title"] = pane.Title, SrcPaneTitle
	}
	if info.Path == "" {
		pathOf(p, pane, &info)
	}
	return info
}

// Claude Code 2.1.296 dialogs, ported from tmux-claude.py (screens captured 2026-10-11):
//
//	trust:   "Yes, I trust this folder" with "❯ No, exit" / "❯ Yes, I trust this folder"
//	outside: "Do you want to proceed?" with "Yes, allow reading from <dir> during this session"
//	permission: "Do you want to proceed|make this edit|create|allow" with "❯ 1." or "❯ Yes"
//	auto-mode: "Enable auto mode?" or an "auto mode … opt in" line, with "❯ 1." or "❯ Yes"
var (
	claudeTrustRE      = regexp.MustCompile(`Yes, I trust this folder`)
	claudeTrustCursor  = regexp.MustCompile(`❯\s*(No, exit|Yes, I trust this folder)`)
	claudeProceedRE    = regexp.MustCompile(`Do you want to proceed\?`)
	claudeOutsideRE    = regexp.MustCompile(`(?m)^\s*(?:❯\s*)?(\d+)\.\s*Yes, allow reading from (\S+) during this session\s*$`)
	claudePermissionRE = regexp.MustCompile(`(?i)Do you want to (proceed|make this edit|create|allow)[^\n]*`)
	claudeAutoModeRE   = regexp.MustCompile(`(?i)Enable auto mode\?[^\n]*|auto mode[^\n]*(opt in|opt-in)[^\n]*`)
	claudeChoiceCursor = regexp.MustCompile(`❯\s*(1\.|Yes)`)
)

// ClaudeDialog reports a Claude Code dialog on the visible screen: its kind (trust, outside,
// permission, auto-mode) and a one-line message.
func ClaudeDialog(screen string) (kind, msg string, ok bool) {
	if screen == "" {
		return "", "", false
	}
	if claudeTrustRE.MatchString(screen) && claudeTrustCursor.MatchString(screen) {
		return "trust", "the workspace trust dialog is open", true
	}
	if claudeProceedRE.MatchString(screen) {
		if m := claudeOutsideRE.FindStringSubmatch(screen); m != nil {
			return "outside", "a read outside the working directories asks for " + m[2], true
		}
	}
	if !claudeChoiceCursor.MatchString(screen) {
		return "", "", false
	}
	if m := claudePermissionRE.FindString(screen); m != "" {
		return "permission", strings.TrimSpace(m), true
	}
	if m := claudeAutoModeRE.FindString(screen); m != "" {
		return "auto-mode", strings.TrimSpace(m), true
	}
	return "", "", false
}

// ── transcript titles ────────────────────────────────────────────────────────

type titleEntry struct {
	mtime time.Time
	size  int64
	title string
	ok    bool
}

const (
	titleTail    = 64 << 10
	titleMaxRead = 64 << 20
)

// claudeTitle returns the transcript's last customTitle, else its last aiTitle. The transcript
// is read from the end: a 64 KiB tail, extended only when no title is in it. The result is
// cached per transcript until its mtime or size changes.
func (in *Inspector) claudeTitle(sessionID string) (string, bool) {
	if !uuidRE.MatchString(sessionID) {
		return "", false
	}
	p := in.transcriptPath(sessionID)
	if p == "" {
		return "", false
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", false
	}
	in.mu.Lock()
	e, hit := in.titles[p]
	in.mu.Unlock()
	if hit && e.mtime.Equal(st.ModTime()) && e.size == st.Size() {
		return e.title, e.ok
	}
	title, ok := readTitle(p, st.Size())
	in.mu.Lock()
	if in.titles == nil {
		in.titles = map[string]titleEntry{}
	}
	in.titles[p] = titleEntry{mtime: st.ModTime(), size: st.Size(), title: title, ok: ok}
	in.mu.Unlock()
	return title, ok
}

// transcriptPath globs projects/*/<sessionId>.jsonl, without rebuilding the cwd slug.
func (in *Inspector) transcriptPath(sessionID string) string {
	in.mu.Lock()
	p, ok := in.paths[sessionID]
	in.mu.Unlock()
	if ok {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	matches, _ := filepath.Glob(filepath.Join(in.ClaudeDir(), "projects", "*", sessionID+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	in.mu.Lock()
	if in.paths == nil {
		in.paths = map[string]string{}
	}
	in.paths[sessionID] = matches[0]
	in.mu.Unlock()
	return matches[0]
}

func readTitle(p string, size int64) (string, bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	for n := int64(titleTail); ; n *= 2 {
		if n > size {
			n = size
		}
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, size-n); err != nil && err != io.EOF {
			return "", false
		}
		if n < size { // drop the partial first line
			if i := bytes.IndexByte(buf, '\n'); i >= 0 {
				buf = buf[i+1:]
			}
		}
		custom, ai := scanTitles(buf)
		if custom != "" {
			return custom, true
		}
		if ai != "" {
			return ai, true
		}
		if n >= size || n >= titleMaxRead {
			return "", false
		}
	}
}

func scanTitles(buf []byte) (custom, ai string) {
	for _, line := range bytes.Split(buf, []byte("\n")) {
		isCustom := bytes.Contains(line, []byte(`"custom-title"`))
		isAI := bytes.Contains(line, []byte(`"ai-title"`))
		if !isCustom && !isAI {
			continue
		}
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		switch {
		case rec.Type == "custom-title" && rec.CustomTitle != "":
			custom = rec.CustomTitle
		case rec.Type == "ai-title" && rec.AITitle != "":
			ai = rec.AITitle
		}
	}
	return custom, ai
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// flagValue returns the value of the first of flags in args ("--flag v" or "--flag=v") that
// valid accepts. A flag given without a valid value is skipped: never guess an id.
func flagValue(args []string, flags []string, valid *regexp.Regexp) string {
	for i, a := range args {
		for _, f := range flags {
			var v string
			switch {
			case a == f && i+1 < len(args):
				v = args[i+1]
			case strings.HasPrefix(a, f+"="):
				v = a[len(f)+1:]
			default:
				continue
			}
			if valid == nil || valid.MatchString(v) {
				return v
			}
		}
	}
	return ""
}
