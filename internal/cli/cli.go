// Package cli is the command line: cobra commands over internal/snapshot. With no command on a
// terminal it opens the TUI; otherwise it prints aligned tables or --json.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/amirhosseinbanaei/perch/internal/agents"
	"github.com/amirhosseinbanaei/perch/internal/config"
	"github.com/amirhosseinbanaei/perch/internal/snapshot"
	"github.com/amirhosseinbanaei/perch/internal/tmux"
	"github.com/amirhosseinbanaei/perch/internal/tui"
	"github.com/amirhosseinbanaei/perch/internal/version"
)

// Exit codes.
const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitNotFound = 3
)

// exitError carries an exit code up to Execute.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func usageErr(format string, a ...any) error {
	return &exitError{ExitUsage, fmt.Errorf(format, a...)}
}

// Env is what the CLI reads from the outside world; tests replace it.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	// IsTTY reports whether stdin and stdout are terminals; Width is stdout's width (0: none).
	IsTTY func() bool
	Width func() int
	Now   func() time.Time
}

// OSEnv is the real process environment.
func OSEnv() Env {
	return Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv, Now: time.Now,
		IsTTY: func() bool { return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) },
		Width: func() int {
			if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
				return w
			}
			if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil {
				return n
			}
			return 0
		},
	}
}

// Execute runs the command line and returns the exit code.
func Execute(args []string, env Env) int {
	root := NewRoot(env)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return ExitOK
	}
	fmt.Fprintln(env.Stderr, "perch:", err)
	var ee *exitError
	switch {
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, snapshot.ErrNotFound):
		return ExitNotFound
	case errors.Is(err, snapshot.ErrAmbiguous):
		return ExitUsage
	case isUsage(err):
		return ExitUsage
	}
	return ExitError
}

// isUsage spots cobra's own argument errors.
func isUsage(err error) bool {
	msg := err.Error()
	for _, s := range []string{"unknown command", "unknown flag", "unknown shorthand", "accepts ", "requires at least", "invalid argument", "flag needs an argument"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// app holds the global flags and the collector built from them.
type app struct {
	env       Env
	socket    string
	path      string
	noColor   bool
	ascii     bool
	verbose   bool
	interval  time.Duration
	themeName string
	noAnim    bool
	bell      bool
	home      string
	collector *snapshot.Collector
}

func (a *app) theme(color bool) tui.Theme {
	return tui.Theme{
		Color: color && !a.noColor && a.env.Getenv("NO_COLOR") == "",
		ASCII: a.ascii || !tui.UTF8Locale(a.env.Getenv),
	}
}

// out is stdout through a colour-profile writer, which downgrades or strips styles to what
// the terminal supports.
func (a *app) out() io.Writer {
	if f, ok := a.env.Stdout.(*os.File); ok {
		return colorprofile.NewWriter(f, os.Environ())
	}
	return a.env.Stdout
}

func (a *app) tableTheme() tui.Theme { return a.theme(a.env.IsTTY()) }

func (a *app) snap(ctx context.Context) (*snapshot.Snapshot, error) {
	s, err := a.collector.Collect(ctx)
	if err != nil && s == nil {
		return nil, err
	}
	if err != nil && a.verbose {
		fmt.Fprintln(a.env.Stderr, "warning:", err)
	}
	return s, nil
}

// NewRoot builds the command tree.
func NewRoot(env Env) *cobra.Command {
	a := &app{env: env}
	a.home, _ = os.UserHomeDir()
	root := &cobra.Command{
		Use:           "perch",
		Short:         "See and manage tmux sessions and the coding agents running in them",
		Long:          "perch lists every tmux session, window and pane, and for panes running Claude Code,\nCodex CLI, Hermes Agent, Gemini CLI or OpenCode shows the agent's status, session id, title\nand path. With no command on a terminal it opens a full-screen TUI.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          noArgs,
		Version:       version.Version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.socket != "" && a.path != "" {
				return usageErr("-L and -S are exclusive")
			}
			a.collector = snapshot.New(tmux.Runner{Socket: a.socket, Path: a.path}, snapshot.Options{})
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.env.IsTTY() {
				return a.runLs(cmd.Context(), false)
			}
			return a.runTUI()
		},
	}
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &exitError{ExitUsage, err} })
	pf := root.PersistentFlags()
	pf.StringVarP(&a.socket, "socket", "L", "", "tmux socket name (tmux -L)")
	pf.StringVarP(&a.path, "socket-path", "S", "", "tmux socket path (tmux -S)")
	pf.BoolVar(&a.noColor, "no-color", false, "no colour (NO_COLOR is honoured too)")
	pf.BoolVar(&a.ascii, "ascii", false, "ASCII symbols only (automatic when the locale is not UTF-8)")
	pf.BoolVar(&a.verbose, "verbose", false, "show where each field came from")
	root.Flags().DurationVar(&a.interval, "interval", 2*time.Second, "TUI refresh interval")
	root.Flags().StringVar(&a.themeName, "theme", "", "TUI theme: coral, teal or violet (PERCH_THEME; t switches and saves it)")
	root.Flags().BoolVar(&a.noAnim, "no-anim", false, "TUI without animation (PERCH_NO_ANIM=1)")
	root.Flags().BoolVar(&a.bell, "bell", false, "TUI rings the terminal bell when an agent starts waiting")

	root.AddCommand(a.lsCmd(), a.agentsCmd(), a.logCmd(), a.newCmd(), a.attachCmd(), a.renameCmd(),
		a.killCmd(), a.sendCmd(), a.peekCmd(), a.doctorCmd(), a.versionCmd())
	return root
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return nil
}

func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usageErr("%s takes %d argument(s), got %d (see --help)", cmd.Name(), n, len(args))
		}
		return nil
	}
}

func (a *app) runTUI() error {
	opts := []tea.ProgramOption{}
	if a.noColor || a.env.Getenv("NO_COLOR") != "" {
		opts = append(opts, tea.WithColorProfile(colorprofile.Ascii))
	}
	th := a.theme(true)
	cfgPath, err := config.Path(a.env.Getenv, a.home)
	var file config.File
	if err == nil {
		file, err = config.Load(cfgPath)
	}
	var warn string
	th.Name, warn = config.ResolveTheme(a.themeName, a.env.Getenv("PERCH_THEME"), file, err)
	var save func(string) error
	if cfgPath != "" {
		save = func(name string) error { return config.SaveTheme(cfgPath, name) }
	}
	m := tui.New(tui.Options{
		Collector: a.collector, Interval: a.interval, Theme: th, Home: a.home,
		SaveTheme: save, Warning: warn, Verbose: a.verbose, Bell: a.bell,
		NoAnim: a.noAnim || a.env.Getenv("PERCH_NO_ANIM") == "1",
	})
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

func ctxOf(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

// ── ls ───────────────────────────────────────────────────────────────────────

func (a *app) lsCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "ls",
		Short: "List sessions: name, windows, attached, created, last activity and the agents inside",
		Args:  exactArgs(0),
		RunE:  func(cmd *cobra.Command, _ []string) error { return a.runLs(ctxOf(cmd), asJSON) },
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the snapshot as JSON")
	return c
}

func (a *app) runLs(ctx context.Context, asJSON bool) error {
	s, err := a.snap(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(a.env.Stdout, s)
	}
	th := a.tableTheme()
	if len(s.Sessions) == 0 {
		fmt.Fprintf(a.env.Stdout, "no tmux sessions (server %s)\n", s.Server.Socket)
		return nil
	}
	t := newTable([]string{"NAME", "WINDOWS", "ATTACHED", "CREATED", "ACTIVITY", "AGENTS"}, 5)
	for _, ss := range s.Sessions {
		var ag []string
		for _, p := range ss.SessionAgents() {
			ag = append(ag, p.Agent.Agent+" "+th.Badge(p.Agent.Status))
		}
		agentsCell := strings.Join(ag, ", ")
		if agentsCell == "" {
			agentsCell = th.Dash()
		}
		att := "no"
		if ss.Attached > 0 {
			att = "yes"
		}
		t.add(ss.Name, strconv.Itoa(len(ss.Windows)), att, tui.AgeSince(s.Taken, ss.Created)+" ago",
			tui.AgeSince(s.Taken, ss.Activity)+" ago", agentsCell)
	}
	return t.render(a.out(), th, a.env.Width())
}

// ── agents ───────────────────────────────────────────────────────────────────

// agentRow is one agent pane in `agents --json`.
type agentRow struct {
	Target  string `json:"target"`
	Pane    string `json:"pane"`
	Session string `json:"session"`
	agents.Info
}

func (a *app) agentsCmd() *cobra.Command {
	var asJSON bool
	var agentID, status string
	c := &cobra.Command{
		Use:   "agents",
		Short: "One row per agent pane: target, agent, status, session id, title, path, age",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validFilter(a.collector.Table(), agentID, status); err != nil {
				return err
			}
			s, err := a.snap(ctxOf(cmd))
			if err != nil {
				return err
			}
			rows := filterAgents(s, agentID, status)
			if asJSON {
				return writeJSON(a.env.Stdout, rows)
			}
			return a.agentsTable(s.Taken, rows)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	c.Flags().StringVar(&agentID, "agent", "", "only this agent (claude, codex, hermes, gemini, opencode)")
	c.Flags().StringVar(&status, "status", "", "only this status (starting, working, waiting, idle, exited, unknown)")
	return c
}

func validFilter(table []agents.Detector, agentID, status string) error {
	if agentID != "" {
		if _, ok := agents.Lookup(table, agentID); !ok {
			return usageErr("unknown agent %q", agentID)
		}
	}
	if status != "" {
		for _, s := range agents.Statuses() {
			if string(s) == status {
				return nil
			}
		}
		return usageErr("unknown status %q", status)
	}
	return nil
}

func filterAgents(s *snapshot.Snapshot, agentID, status string) []agentRow {
	rows := []agentRow{}
	for _, ap := range s.Agents() {
		in := *ap.Pane.Agent
		if (agentID != "" && in.Agent != agentID) || (status != "" && string(in.Status) != status) {
			continue
		}
		rows = append(rows, agentRow{Target: ap.Pane.Target, Pane: ap.Pane.ID, Session: ap.Session.Name, Info: in})
	}
	return rows
}

func (a *app) agentsTable(now time.Time, rows []agentRow) error {
	th := a.tableTheme()
	if len(rows) == 0 {
		_, err := fmt.Fprintln(a.env.Stdout, "no agents found")
		return err
	}
	headers := []string{"TARGET", "AGENT", "STATUS", "SESSION ID", "TITLE", "PATH", "AGE"}
	if a.verbose {
		headers = append(headers, "PID", "CONF", "SOURCE", "NOTE")
	}
	t := newTable(headers, 4, 5)
	for _, r := range rows {
		id := r.SessionID
		if id == "" {
			id = th.Dash()
		}
		cells := []string{r.Target, r.Agent, th.Badge(r.Status), id, r.Title, tui.ShortPath(r.Path, a.home), tui.AgeSince(now, r.StartedAt)}
		if a.verbose {
			cells = append(cells, strconv.Itoa(r.PID), r.Confidence, r.SourceString(), r.Note)
		}
		t.add(cells...)
	}
	return t.render(a.out(), th, a.env.Width())
}

// ── log ──────────────────────────────────────────────────────────────────────

func (a *app) logCmd() *cobra.Command {
	var follow, asJSON bool
	var interval time.Duration
	var file string
	c := &cobra.Command{
		Use:   "log",
		Short: "Print a status report; --follow streams one line per change",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interval < 200*time.Millisecond {
				return usageErr("--interval must be at least 200ms")
			}
			var sink io.Writer = a.out()
			var fileOut io.Writer
			if file != "" {
				f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
				if err != nil {
					return fmt.Errorf("open log file: %w", err)
				}
				defer f.Close()
				fileOut = f
			}
			if follow {
				return a.follow(ctxOf(cmd), sink, fileOut, interval, asJSON)
			}
			return a.report(ctxOf(cmd), sink, fileOut, asJSON)
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "stream status changes")
	c.Flags().DurationVar(&interval, "interval", 2*time.Second, "how often --follow looks")
	c.Flags().BoolVar(&asJSON, "json", false, "JSON (JSON Lines with --follow)")
	c.Flags().StringVar(&file, "file", "", "also append the output to this file")
	return c
}

type report struct {
	Taken    time.Time       `json:"taken"`
	Server   snapshot.Server `json:"server"`
	Sessions int             `json:"sessions"`
	Counts   map[string]int  `json:"counts"`
	Agents   []agentRow      `json:"agents"`
}

func (a *app) report(ctx context.Context, out, file io.Writer, asJSON bool) error {
	s, err := a.snap(ctx)
	if err != nil {
		return err
	}
	rows := filterAgents(s, "", "")
	r := report{Taken: s.Taken, Server: s.Server, Sessions: len(s.Sessions), Counts: map[string]int{}, Agents: rows}
	for _, row := range rows {
		r.Counts[string(row.Status)]++
	}
	if asJSON {
		if file != nil {
			if err := writeJSON(file, r); err != nil {
				return err
			}
		}
		return writeJSON(a.env.Stdout, r)
	}
	var counts []string
	for _, st := range agents.Statuses() {
		if n := r.Counts[string(st)]; n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, st))
		}
	}
	summary := fmt.Sprintf("%s  server %s  %d sessions  %d agents", s.Taken.Format("2006-01-02 15:04:05"), s.Server.Socket, len(s.Sessions), len(rows))
	if len(counts) > 0 {
		summary += " (" + strings.Join(counts, ", ") + ")"
	}
	fmt.Fprintln(out, summary)
	if file != nil {
		fmt.Fprintln(file, summary)
		plain := *a
		plain.env.Stdout = file
		plain.env.IsTTY = func() bool { return false }
		plain.env.Width = func() int { return 0 }
		if len(rows) > 0 {
			if err := plain.agentsTable(s.Taken, rows); err != nil {
				return err
			}
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return a.agentsTable(s.Taken, rows)
}

func (a *app) follow(ctx context.Context, out, file io.Writer, interval time.Duration, asJSON bool) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	th := a.tableTheme()
	plain := tui.Theme{ASCII: th.ASCII}
	var prev *snapshot.Snapshot
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		s, err := a.collector.Collect(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil && s == nil:
			fmt.Fprintln(a.env.Stderr, "refresh failed:", err)
		default:
			for _, e := range snapshot.Diff(prev, s) {
				if asJSON {
					b, _ := json.Marshal(e)
					fmt.Fprintln(out, string(b))
					if file != nil {
						fmt.Fprintln(file, string(b))
					}
					continue
				}
				fmt.Fprintln(out, tui.EventLine(th, e, a.home))
				if file != nil {
					fmt.Fprintln(file, e.Time.Format("2006-01-02 ")+tui.EventLine(plain, e, a.home))
				}
			}
			prev = s
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// ── actions ──────────────────────────────────────────────────────────────────

func (a *app) newCmd() *cobra.Command {
	var dir, agentID string
	c := &cobra.Command{
		Use:   "new NAME [-c DIR] [--agent ID | -- CMD...]",
		Short: "Create a detached session, optionally running an agent or a command",
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			names, command := args, []string(nil)
			if dash >= 0 {
				names, command = args[:dash], args[dash:]
			}
			if len(names) != 1 || names[0] == "" {
				return usageErr("new takes one NAME")
			}
			if agentID != "" && len(command) > 0 {
				return usageErr("--agent and -- CMD are exclusive")
			}
			if agentID != "" {
				d, ok := agents.Lookup(a.collector.Table(), agentID)
				if !ok {
					return usageErr("unknown agent %q", agentID)
				}
				p, err := tui.LookAgent(d, exec.LookPath)
				if err != nil {
					return err
				}
				command = tui.AgentCommand(p)
			}
			if dir != "" {
				if st, err := os.Stat(dir); err != nil || !st.IsDir() {
					return usageErr("-c %s: not a directory", dir)
				}
			}
			got, err := a.collector.NewSession(ctxOf(cmd), names[0], dir, command)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.env.Stdout, "created", got)
			return nil
		},
	}
	c.Flags().StringVarP(&dir, "dir", "c", "", "start directory")
	c.Flags().StringVar(&agentID, "agent", "", "start this agent (it must be on PATH)")
	return c
}

func (a *app) attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach TARGET",
		Short: "Attach to a session (switch-client when already inside tmux)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.resolve(ctxOf(cmd), args[0])
			if err != nil {
				return err
			}
			c, _ := a.collector.AttachCommand(ctxOf(cmd), t.Session.Name)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("attach %s: %w", t.Session.Name, err)
			}
			return nil
		},
	}
}

func (a *app) renameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename OLD NEW",
		Short: "Rename a session (OLD is an exact session name)",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := a.session(ctxOf(cmd), args[0]); err != nil {
				return err
			}
			if args[1] == "" {
				return usageErr("NEW is empty")
			}
			if err := a.collector.RenameSession(ctxOf(cmd), args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(a.env.Stdout, "renamed %s → %s\n", args[0], args[1])
			return nil
		},
	}
}

func (a *app) killCmd() *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "kill NAME [--yes]",
		Short: "Kill one session by its exact name (asks first unless --yes)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.session(ctxOf(cmd), args[0])
			if err != nil {
				return err
			}
			if !yes {
				if !a.env.IsTTY() {
					return usageErr("refusing to kill %q without --yes when not on a terminal", s.Name)
				}
				fmt.Fprintf(a.env.Stdout, "Kill session %q (%d windows, %d agents)? [y/N] ", s.Name, len(s.Windows), len(s.SessionAgents()))
				line, _ := bufio.NewReader(a.env.Stdin).ReadString('\n')
				if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
					fmt.Fprintln(a.env.Stdout, "not killed")
					return nil
				}
			}
			if err := a.collector.KillSession(ctxOf(cmd), s.Name); err != nil {
				return err
			}
			fmt.Fprintln(a.env.Stdout, "killed", s.Name)
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask")
	return c
}

func (a *app) sendCmd() *cobra.Command {
	var enter bool
	c := &cobra.Command{
		Use:   "send TARGET TEXT [--enter]",
		Short: "Type TEXT into a pane literally; --enter presses Enter after it",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pane, err := a.pane(ctxOf(cmd), args[0])
			if err != nil {
				return err
			}
			return a.collector.SendText(ctxOf(cmd), pane, args[1], enter)
		},
	}
	c.Flags().BoolVar(&enter, "enter", false, "press Enter after the text")
	return c
}

func (a *app) peekCmd() *cobra.Command {
	var lines int
	c := &cobra.Command{
		Use:   "peek TARGET [--lines N]",
		Short: "Print a pane's screen (the last N lines, with scrollback when N is larger)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if lines < 0 {
				return usageErr("--lines must be positive")
			}
			pane, err := a.pane(ctxOf(cmd), args[0])
			if err != nil {
				return err
			}
			text, err := a.collector.Capture(ctxOf(cmd), pane, lines)
			if err != nil {
				return err
			}
			ls := strings.Split(strings.TrimRight(text, "\n "), "\n")
			if lines > 0 && len(ls) > lines {
				ls = ls[len(ls)-lines:]
			}
			_, err = fmt.Fprintln(a.env.Stdout, strings.Join(ls, "\n"))
			return err
		},
	}
	c.Flags().IntVarP(&lines, "lines", "n", 0, "lines to print (0: the visible screen)")
	return c
}

func (a *app) resolve(ctx context.Context, target string) (snapshot.Target, error) {
	s, err := a.snap(ctx)
	if err != nil {
		return snapshot.Target{}, err
	}
	return s.Resolve(target)
}

func (a *app) session(ctx context.Context, name string) (*snapshot.Session, error) {
	s, err := a.snap(ctx)
	if err != nil {
		return nil, err
	}
	if ss := s.Session(name); ss != nil {
		return ss, nil
	}
	return nil, fmt.Errorf("session %q: %w", name, snapshot.ErrNotFound)
}

// pane resolves TARGET to a pane id: a session means its current pane.
func (a *app) pane(ctx context.Context, target string) (string, error) {
	t, err := a.resolve(ctx, target)
	if err != nil {
		return "", err
	}
	if t.Pane != nil {
		return t.Pane.ID, nil
	}
	return a.collector.ActivePane(ctx, t.Session.Name)
}

// ── doctor, version ──────────────────────────────────────────────────────────

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check tmux, the socket, the agents on PATH, their state directories, UTF-8 and colour",
		Args:  exactArgs(0),
		RunE:  func(cmd *cobra.Command, _ []string) error { return a.doctor(ctxOf(cmd)) },
	}
}

func (a *app) doctor(ctx context.Context) error {
	w := a.env.Stdout
	th := a.tableTheme()
	ok, bad := "ok", "--"
	if !th.ASCII {
		ok, bad = "✓", "✗"
	}
	line := func(good bool, k, v string) {
		mark := bad
		if good {
			mark = ok
		}
		fmt.Fprintf(w, "%s %-12s %s\n", mark, k, v)
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		line(false, "tmux", "not on PATH: "+installHint())
	} else {
		v, _ := a.collector.TmuxVersion(ctx)
		line(true, "tmux", tmuxPath+" ("+v+")")
	}
	sock, err := a.collector.SocketPath(ctx)
	if err != nil {
		line(false, "server", a.collector.SocketLabel()+": not running")
	} else {
		s, _ := a.snap(ctx)
		n := 0
		if s != nil {
			n = len(s.Sessions)
		}
		line(true, "server", fmt.Sprintf("%s (%s), %d sessions", a.collector.SocketLabel(), sock, n))
	}
	dirs := a.collector.Inspector().StateDirs()
	for _, d := range a.collector.Table() {
		p, err := tui.LookAgent(d, exec.LookPath)
		if err != nil {
			line(false, d.ID, "not on PATH")
		} else {
			line(true, d.ID, p+" "+agentVersion(ctx, p))
		}
		_, serr := os.Stat(dirs[d.ID])
		line(serr == nil, "  state", dirs[d.ID])
	}
	utf := tui.UTF8Locale(a.env.Getenv)
	line(utf, "utf-8", fmt.Sprintf("LC_ALL=%q LC_CTYPE=%q LANG=%q", a.env.Getenv("LC_ALL"), a.env.Getenv("LC_CTYPE"), a.env.Getenv("LANG")))
	prof := colorprofile.Detect(os.Stdout, os.Environ())
	line(prof > colorprofile.NoTTY && !a.noColor && a.env.Getenv("NO_COLOR") == "", "colour", fmt.Sprintf("%s (TERM=%s, COLORTERM=%s)", prof, a.env.Getenv("TERM"), a.env.Getenv("COLORTERM")))
	line(true, "platform", runtime.GOOS+"/"+runtime.GOARCH)
	return nil
}

// agentVersion runs `<agent> --version` with a short timeout.
func agentVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "(version unknown)"
	}
	v, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return "(" + v + ")"
}

func installHint() string {
	hints := map[string]string{"apt-get": "apt install tmux", "dnf": "dnf install tmux", "pacman": "pacman -S tmux",
		"apk": "apk add tmux", "brew": "brew install tmux", "pkg": "pkg install tmux"}
	keys := make([]string, 0, len(hints))
	for k := range hints {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := exec.LookPath(k); err == nil {
			return hints[k]
		}
	}
	return "install tmux with your package manager"
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and build date",
		Args:  exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprintf(a.env.Stdout, "perch %s (commit %s, built %s, %s/%s)\n", version.Version, version.Commit, version.Date, runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}
