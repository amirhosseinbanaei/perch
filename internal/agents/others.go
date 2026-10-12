package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The four agents that are not installed on the build machine. Every fact below was read from
// the agent's source at the tag named in its comment (2026-10-12); the screen patterns are
// unverified on a live session.

// stateful configures the shared enricher for an agent without a live registry like Claude's.
type stateful struct {
	// argvID reads a session id from the process's own arguments.
	argvID func(args []string) string
	// state finds the session in the agent's state directory: id, title, and a note when it
	// cannot. It must never guess: no match means no id.
	state func(in *Inspector, p Proc, cwd string) (id, title, note string)
	// working matches the agent's busy footer on the visible screen.
	working *regexp.Regexp
	// waiting holds dialog patterns: each pair matches only when both of its parts are on screen
	// (a dialog title and its choices), so old text alone does not count.
	waiting [][2]*regexp.Regexp
}

func (s stateful) enrich(ctx context.Context, in *Inspector, p Proc, pane Pane) Info {
	info := Info{Source: map[string]string{}, Confidence: High}
	pathOf(p, pane, &info)
	if id := s.argvID(p.Args); id != "" {
		info.SessionID, info.Source["sessionId"] = id, SrcArgv
	}
	if s.state != nil {
		id, title, note := s.state(in, p, info.Path)
		if info.SessionID == "" && id != "" {
			info.SessionID, info.Source["sessionId"] = id, SrcStateDir
		}
		if title != "" {
			info.Title, info.Source["title"] = title, SrcStateDir
		}
		if info.SessionID == "" && note != "" {
			info.Source["sessionId"] = note
		}
	}
	if info.SessionID == "" && info.Source["sessionId"] == "" {
		info.Source["sessionId"] = "no session id in argv"
	}
	if info.Title == "" && pane.Title != "" {
		info.Title, info.Source["title"] = pane.Title, SrcPaneTitle
	}

	screen := screenOf(ctx, pane)
	for _, pair := range s.waiting {
		if pair[0].MatchString(screen) && pair[1].MatchString(screen) {
			info.Status, info.Source["status"] = Waiting, SrcScreen
			info.Note = strings.TrimSpace(pair[0].FindString(screen))
			return info
		}
	}
	if s.working != nil && s.working.MatchString(screen) {
		info.Status, info.Source["status"] = Working, SrcScreen
		return info
	}
	info.Status, info.Source["status"] = activityStatus(in.now(), pane.Activity), SrcActivity
	info.Confidence = Low
	return info
}

// ── Codex ────────────────────────────────────────────────────────────────────

// OpenAI Codex CLI, checked against rust-v0.162.1 (2026-10-09):
// https://github.com/openai/codex/blob/rust-v0.162.1/codex-cli/bin/codex.js#L16-L96 — the npm
// wrapper `node …/@openai/codex/bin/codex.js` spawns the native binary, named plain "codex"
// (vendor/<triple>/bin/codex). The release tarballs are codex-<triple>.tar.gz, so a
// platform-suffixed "codex-x86_64-unknown-linux-musl" is matched too.
// Sessions: $CODEX_HOME (default ~/.codex)/sessions/YYYY/MM/DD/rollout-<time>-<uuid>.jsonl, whose
// first line is {"type":"session_meta","payload":{"id","cwd",…}}
// (codex-rs/rollout/src/recorder.rs#L1740-L1758, protocol/src/protocol.rs#L3152-L3182). Titles:
// $CODEX_HOME/session_index.jsonl {"id","thread_name"}, last wins
// (rollout/src/session_index.rs#L20-L29). argv: `codex resume <id>`, `codex fork <id>`
// (cli/src/main.rs#L206-L225). Screen: "(12s • esc to interrupt)"
// (tui/src/status_indicator_widget.rs#L252-L258); approvals "Would you like to run the following
// command?" etc. with "Yes, proceed" / "Press enter to confirm or esc to cancel"
// (tui/src/bottom_pane/approval_overlay.rs#L254-L295, #L645-L649, #L845).
func codexDetector() Detector {
	m := matcher{
		names:    []string{"codex"},
		patterns: []*regexp.Regexp{regexp.MustCompile(`^codex-(x86_64|aarch64|arm64|armv7)-(unknown-linux-(musl|gnu|gnueabihf)|apple-darwin)$`)},
		packages: []string{"@openai/codex"},
	}
	s := stateful{
		argvID:  codexArgvID,
		state:   codexState,
		working: regexp.MustCompile(`to interrupt\)`),
		waiting: [][2]*regexp.Regexp{{
			regexp.MustCompile(`Would you like to (run the following command|make the following edits|grant these permissions|send input to terminal[^?]*)\?|Do you want to approve network access[^\n]*|[^\n]* needs your approval\.`),
			regexp.MustCompile(`Yes, proceed|Press enter to confirm or esc to cancel`),
		}},
	}
	return Detector{ID: "codex", DisplayName: "Codex CLI", Executables: []string{"codex"}, Match: m.match, Enrich: s.enrich}
}

func codexArgvID(args []string) string {
	for i, a := range args {
		if (a == "resume" || a == "fork") && i+1 < len(args) && uuidRE.MatchString(args[i+1]) {
			return args[i+1]
		}
	}
	return ""
}

// CodexHome is $CODEX_HOME, else ~/.codex.
func (in *Inspector) CodexHome() string {
	if d := in.env("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(in.Home, ".codex")
}

func codexState(in *Inspector, p Proc, cwd string) (id, title, note string) {
	home := in.CodexHome()
	root := filepath.Join(home, "sessions")
	if p.StartedAt.IsZero() {
		return "", "", "process start time unknown; not matching " + root
	}
	// Rollouts live under the local date they started; look from the process's start day on.
	var dirs []string
	for d := p.StartedAt.Add(-24 * time.Hour); !d.After(in.now()); d = d.Add(24 * time.Hour) {
		dirs = append(dirs, filepath.Join(root, d.Format("2006"), d.Format("01"), d.Format("02")))
	}
	var files []string
	for _, d := range dirs {
		m, _ := filepath.Glob(filepath.Join(d, "rollout-*.jsonl"))
		files = append(files, m...)
	}
	best := newestAfter(files, p.StartedAt, func(path string) (string, bool) {
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if !firstLineJSON(path, &rec) || rec.Type != "session_meta" || rec.Payload.ID == "" {
			return "", false
		}
		return rec.Payload.ID, cwd != "" && rec.Payload.Cwd == cwd
	})
	if best == "" {
		return "", "", "no rollout in " + root + " for this cwd since the process started"
	}
	return best, codexTitle(home, best), ""
}

func codexTitle(home, id string) string {
	f, err := os.Open(filepath.Join(home, "session_index.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var title string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var rec struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.ID == id && rec.ThreadName != "" {
			title = rec.ThreadName
		}
	}
	return title
}

// ── Hermes ───────────────────────────────────────────────────────────────────

// Hermes Agent (Nous Research), checked against v0.21.7 (2026-10-12):
// https://github.com/NousResearch/hermes-agent/blob/v0.21.7/pyproject.toml#L557-L560 — console
// scripts "hermes" and "hermes-agent"; a source install's ~/.local/bin/hermes runs
// `python -I -c "…runpy.run_module('hermes_cli.main', …)"` (hermes_cli/_launchers.py#L38-L69).
// Sessions are rows in SQLite ($HERMES_HOME/state.db), which this program does not read; the live
// registry $HERMES_HOME/runtime/active_sessions.json maps a pid to its session
// (hermes_cli/active_sessions.py#L175-L176, #L460-L476). In TUI mode that pid is the
// `python -m tui_gateway.entry` grandchild. HERMES_HOME defaults to ~/.hermes
// (hermes_constants.py#L51-L58). Ids look like 20261012_091502_a1b2c3 (hermes_state_ids.py).
// argv: -r/--resume <id|title|latest> (hermes_cli/_parser.py#L175-L186). Screen strings come from
// locales/en.yaml#L2121-L2196 and ui-tui/src/i18n/en/{chrome,overlays}.ts.
func hermesDetector() Detector {
	m := matcher{
		names:   []string{"hermes", "hermes-agent"},
		modules: []string{"hermes_cli", "tui_gateway"},
	}
	s := stateful{
		argvID:  func(args []string) string { return flagValue(args, []string{"--resume", "-r"}, hermesIDRE) },
		state:   hermesState,
		working: regexp.MustCompile(`msg=interrupt|Ctrl\+C to interrupt|running…`),
		waiting: [][2]*regexp.Regexp{
			{regexp.MustCompile(`Dangerous Command|DANGEROUS COMMAND[^\n]*|approval required`), regexp.MustCompile(`Allow once|Choice \[o/s/a/D\]`)},
		},
	}
	return Detector{ID: "hermes", DisplayName: "Hermes Agent", Executables: []string{"hermes"}, Match: m.match, Enrich: s.enrich}
}

var hermesIDRE = regexp.MustCompile(`^\d{8}_\d{6}_[0-9a-f]{6}$`)

// HermesHome is $HERMES_HOME, else ~/.hermes.
func (in *Inspector) HermesHome() string {
	if d := in.env("HERMES_HOME"); d != "" {
		return d
	}
	return filepath.Join(in.Home, ".hermes")
}

func hermesState(in *Inspector, p Proc, _ string) (id, title, note string) {
	path := filepath.Join(in.HermesHome(), "runtime", "active_sessions.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", "no " + path
	}
	var reg struct {
		Entries []struct {
			SessionID string `json:"session_id"`
			PID       int    `json:"pid"`
			Metadata  struct {
				LiveSessionID string `json:"live_session_id"`
			} `json:"metadata"`
		} `json:"entries"`
	}
	if json.Unmarshal(b, &reg) != nil {
		return "", "", "unreadable " + path
	}
	pids := append([]int{p.PID}, p.Descendants...)
	for _, pid := range pids {
		for _, e := range reg.Entries {
			if e.PID != pid {
				continue
			}
			if e.Metadata.LiveSessionID != "" {
				return e.Metadata.LiveSessionID, "", ""
			}
			if e.SessionID != "" {
				return e.SessionID, "", ""
			}
		}
	}
	return "", "", "pid not in " + path
}

// ── Gemini ───────────────────────────────────────────────────────────────────

// Google Gemini CLI, checked against v0.63.0 (2026-10-06):
// https://github.com/google-gemini/gemini-cli/blob/v0.63.0/package.json#L92-L94 — bin "gemini"
// → bundle/gemini.js, run by node; it relaunches itself as a child node with the same script
// and arguments (packages/cli/src/utils/processUtils.ts#L106-L155), so a pane shows two node
// processes; the nearer one is reported. Sessions: $GEMINI_CLI_HOME (default the home
// directory)/.gemini/tmp/<project-slug>/chats/session-<time>-<id8>.jsonl
// (packages/core/src/services/chatRecordingService.ts#L498-L520), whose first line is
// {"sessionId","projectHash","startTime",…}; later {"$set":{"summary":…}} lines carry the title.
// The cwd → slug map is .gemini/projects.json {"projects":{"<path>":"<slug>"}}
// (packages/core/src/config/projectRegistry.ts#L16-L24). argv: -r/--resume <uuid>,
// --session-id <uuid> (packages/cli/src/config/config.ts#L401-L454). Screen: "(esc to cancel, 12s)"
// (ui/components/LoadingIndicator.tsx#L78-L81); "Allow execution of …?", "Apply this change?",
// "Do you want to proceed?" with "Allow once" (ui/components/messages/ToolConfirmationMessage.tsx).
func geminiDetector() Detector {
	m := matcher{names: []string{"gemini"}, packages: []string{"@google/gemini-cli"}}
	s := stateful{
		argvID:  func(args []string) string { return flagValue(args, []string{"--resume", "-r", "--session-id"}, uuidRE) },
		state:   geminiState,
		working: regexp.MustCompile(`\(esc to cancel, \d`),
		waiting: [][2]*regexp.Regexp{
			{regexp.MustCompile(`Allow execution of [^\n]*\?|Apply this change\?|Do you want to proceed\?|Waiting for user confirmation`), regexp.MustCompile(`Allow once|Allow for this session`)},
		},
	}
	return Detector{ID: "gemini", DisplayName: "Gemini CLI", Executables: []string{"gemini"}, Match: m.match, Enrich: s.enrich}
}

// GeminiDir is $GEMINI_CLI_HOME/.gemini, else ~/.gemini.
func (in *Inspector) GeminiDir() string {
	home := in.env("GEMINI_CLI_HOME")
	if home == "" {
		home = in.Home
	}
	return filepath.Join(home, ".gemini")
}

func geminiState(in *Inspector, p Proc, cwd string) (id, title, note string) {
	dir := in.GeminiDir()
	var reg struct {
		Projects map[string]string `json:"projects"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "projects.json"))
	if err != nil || json.Unmarshal(b, &reg) != nil {
		return "", "", "no " + filepath.Join(dir, "projects.json")
	}
	slug, ok := reg.Projects[cwd]
	if !ok || slug == "" || strings.ContainsAny(slug, `/\`) {
		return "", "", "cwd not in " + filepath.Join(dir, "projects.json")
	}
	if p.StartedAt.IsZero() {
		return "", "", "process start time unknown"
	}
	chats := filepath.Join(dir, "tmp", slug, "chats")
	files, _ := filepath.Glob(filepath.Join(chats, "session-*.jsonl"))
	best := newestAfter(files, p.StartedAt, func(path string) (string, bool) {
		var rec struct {
			SessionID string `json:"sessionId"`
		}
		if !firstLineJSON(path, &rec) || rec.SessionID == "" {
			return "", false
		}
		return rec.SessionID, true
	})
	if best == "" {
		return "", "", "no session in " + chats + " since the process started"
	}
	return best, geminiTitle(files, best), ""
}

// geminiTitle reads the last {"$set":{"summary":…}} of the session's file.
func geminiTitle(files []string, id string) string {
	for _, path := range files {
		var head struct {
			SessionID string `json:"sessionId"`
		}
		if !firstLineJSON(path, &head) || head.SessionID != id {
			continue
		}
		b, err := tail(path, titleTail)
		if err != nil {
			return ""
		}
		var title string
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, `"$set"`) {
				continue
			}
			var rec struct {
				Set struct {
					Summary string `json:"summary"`
				} `json:"$set"`
			}
			if json.Unmarshal([]byte(line), &rec) == nil && rec.Set.Summary != "" {
				title = rec.Set.Summary
			}
		}
		return title
	}
	return ""
}

// ── OpenCode ─────────────────────────────────────────────────────────────────

// OpenCode, checked against v1.19.0 (2026-10-11):
// https://github.com/anomalyco/opencode/blob/v1.19.0/packages/opencode/script/postinstall.mjs#L26-L29
// — the npm package opencode-ai installs a native Bun-compiled binary as bin/opencode.exe (on
// every OS), and ps shows it directly; the legacy wrapper spawns opencode-<os>-<arch>/bin/opencode
// or bin/.opencode (packages/opencode/bin/opencode#L73-L74). Sessions are rows in SQLite
// ($XDG_DATA_HOME/opencode/opencode.db, packages/core/src/database/database.ts#L43-L54), which
// this program does not read, so the id comes from argv only: -s/--session ses_…
// (packages/opencode/src/cli/cmd/tui.ts#L108-L136). Screen: "esc interrupt" / "esc again to
// interrupt" (packages/tui/src/component/prompt/index.tsx#L1587-L1592); "Permission required"
// with "Allow once" (packages/tui/src/routes/session/permission.tsx#L385-L406).
func opencodeDetector() Detector {
	m := matcher{names: []string{"opencode", "opencode.exe", ".opencode"}, packages: []string{"opencode-ai"}}
	s := stateful{
		argvID: func(args []string) string { return flagValue(args, []string{"--session", "-s"}, opencodeIDRE) },
		state: func(in *Inspector, _ Proc, _ string) (string, string, string) {
			return "", "", "sessions are in SQLite (" + in.OpencodeDB() + "), not read"
		},
		working: regexp.MustCompile(`esc (again to )?interrupt`),
		waiting: [][2]*regexp.Regexp{{regexp.MustCompile(`Permission required`), regexp.MustCompile(`Allow once`)}},
	}
	return Detector{ID: "opencode", DisplayName: "OpenCode", Executables: []string{"opencode"}, Match: m.match, Enrich: s.enrich}
}

var opencodeIDRE = regexp.MustCompile(`^ses_[0-9A-Za-z]{10,}$`)

// OpencodeDB is $OPENCODE_DB, else $XDG_DATA_HOME/opencode/opencode.db (default
// ~/.local/share/opencode/opencode.db). Only shown in notes and by doctor.
func (in *Inspector) OpencodeDB() string {
	data := in.env("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(in.Home, ".local", "share")
	}
	if db := in.env("OPENCODE_DB"); db != "" {
		if filepath.IsAbs(db) {
			return db
		}
		return filepath.Join(data, "opencode", db)
	}
	return filepath.Join(data, "opencode", "opencode.db")
}

// StateDirs lists every agent's state location, for doctor.
func (in *Inspector) StateDirs() map[string]string {
	return map[string]string{
		"claude":   in.ClaudeDir(),
		"codex":    in.CodexHome(),
		"hermes":   in.HermesHome(),
		"gemini":   in.GeminiDir(),
		"opencode": in.OpencodeDB(),
	}
}

// ── file helpers ─────────────────────────────────────────────────────────────

// newestAfter returns the id of the newest file modified after since that accept matches.
func newestAfter(files []string, since time.Time, accept func(path string) (id string, ok bool)) string {
	type cand struct {
		path  string
		mtime time.Time
	}
	var cs []cand
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(since) {
			cs = append(cs, cand{f, st.ModTime()})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].mtime.After(cs[j].mtime) })
	for _, c := range cs {
		if id, ok := accept(c.path); ok {
			return id
		}
	}
	return ""
}

// firstLineJSON decodes the first line of a file (up to 1 MiB).
func firstLineJSON(path string, v any) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return false
		}
		line = append(line, chunk...)
		if len(line) > 1<<20 {
			return false
		}
		if !isPrefix {
			break
		}
	}
	return json.Unmarshal(line, v) == nil
}

// tail returns up to n bytes from the end of a file, from the first whole line.
func tail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := st.Size() - n
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil, err
	}
	if off > 0 {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, nil
}
