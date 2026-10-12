// Package tmux is the only package that runs tmux. Every call goes through Runner, which holds
// the socket choice (-L name or -S path) and builds an argument list for exec.Command: nothing
// here is ever handed to a shell, so a session name is never interpreted.
//
// Nothing in this package kills by pattern, and there is no kill-server path: sessions are
// closed one at a time by their exact name ("=" + name).
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner runs tmux against one server. The zero value talks to the default server.
type Runner struct {
	Socket string // tmux -L <name>
	Path   string // tmux -S <path>; wins over Socket when both are set
}

// ErrNoServer reports that no tmux server is listening on the runner's socket. Listing treats
// it as an empty server; actions return it.
var ErrNoServer = errors.New("no tmux server running")

// Error is a failed tmux call: the arguments, the exit error and what tmux printed.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("tmux %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// noServer matches what tmux 3.x prints when nothing is listening on the socket.
func noServer(stderr string) bool {
	for _, s := range []string{"no server running", "error connecting to", "server exited unexpectedly", "no sessions"} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// Args returns the full argument list (without the "tmux" executable) for a command. -u is
// always passed: without it, tmux replaces tabs and non-ASCII bytes with "_" whenever the locale
// is not UTF-8, which would break both the field separator and Unicode names.
func (r Runner) Args(args ...string) []string {
	out := []string{"-u"}
	switch {
	case r.Path != "":
		out = append(out, "-S", r.Path)
	case r.Socket != "":
		out = append(out, "-L", r.Socket)
	}
	return append(out, args...)
}

// Command builds the exec.Cmd for a tmux command, with no shell in between.
func (r Runner) Command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "tmux", r.Args(args...)...)
}

// Run runs one tmux command and returns its stdout. A missing server is ErrNoServer.
func (r Runner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := r.Command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if noServer(stderr.String()) {
			return "", ErrNoServer
		}
		return "", &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

// Session is one row of list-sessions.
type Session struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	Attached int       `json:"attached"`
	Windows  int       `json:"windows"`
	Activity time.Time `json:"activity"`
}

// Pane is one row of list-panes -a.
type Pane struct {
	Session        string    `json:"session"`
	WindowIndex    int       `json:"windowIndex"`
	WindowName     string    `json:"windowName"`
	Index          int       `json:"index"`
	ID             string    `json:"id"`
	PID            int       `json:"pid"`
	Command        string    `json:"command"`
	Path           string    `json:"path"`
	Title          string    `json:"title"`
	Active         bool      `json:"active"`
	Dead           bool      `json:"dead"`
	WindowActivity time.Time `json:"windowActivity"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
}

// Tab separates fields: tmux prints a tab inside a name as the two characters `\t`, so a raw
// tab in the output is always a separator.
const sep = "\t"

// SessionFormat and PaneFormat are the two -F formats a snapshot uses.
var (
	SessionFormat = strings.Join([]string{"#{session_id}", "#{session_name}", "#{session_created}",
		"#{session_attached}", "#{session_windows}", "#{session_activity}"}, sep)
	PaneFormat = strings.Join([]string{"#{session_name}", "#{window_index}", "#{window_name}",
		"#{pane_index}", "#{pane_id}", "#{pane_pid}", "#{pane_current_command}", "#{pane_current_path}",
		"#{pane_title}", "#{pane_active}", "#{pane_dead}", "#{window_activity}", "#{pane_width}", "#{pane_height}"}, sep)
)

const (
	sessionFields = 6
	paneFields    = 14
)

// ListSessions runs list-sessions. No server is an empty list, not an error.
func (r Runner) ListSessions(ctx context.Context) ([]Session, error) {
	out, err := r.Run(ctx, "list-sessions", "-F", SessionFormat)
	if errors.Is(err, ErrNoServer) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseSessions(out)
}

// ListPanes runs list-panes -a. No server is an empty list, not an error.
func (r Runner) ListPanes(ctx context.Context) ([]Pane, error) {
	out, err := r.Run(ctx, "list-panes", "-a", "-F", PaneFormat)
	if errors.Is(err, ErrNoServer) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParsePanes(out)
}

// ParseSessions parses list-sessions output in SessionFormat.
func ParseSessions(out string) ([]Session, error) {
	var res []Session
	for _, line := range lines(out) {
		f := strings.Split(line, sep)
		if len(f) < sessionFields {
			return nil, fmt.Errorf("parse list-sessions line %q: want %d fields, got %d", line, sessionFields, len(f))
		}
		// A name can only hold an escaped tab, but be safe: extra fields belong to the name.
		name := strings.Join(f[1:len(f)-4], sep)
		tail := f[len(f)-4:]
		res = append(res, Session{
			ID:       f[0],
			Name:     name,
			Created:  unix(tail[0]),
			Attached: atoi(tail[1]),
			Windows:  atoi(tail[2]),
			Activity: unix(tail[3]),
		})
	}
	return res, nil
}

// ParsePanes parses list-panes output in PaneFormat. The title is the only free-text field a
// program controls (through an escape sequence), so any extra fields are joined back into it.
func ParsePanes(out string) ([]Pane, error) {
	var res []Pane
	for _, line := range lines(out) {
		f := strings.Split(line, sep)
		if len(f) < paneFields {
			return nil, fmt.Errorf("parse list-panes line %q: want %d fields, got %d", line, paneFields, len(f))
		}
		extra := len(f) - paneFields
		title := strings.Join(f[8:9+extra], sep)
		t := f[9+extra:]
		res = append(res, Pane{
			Session:        f[0],
			WindowIndex:    atoi(f[1]),
			WindowName:     f[2],
			Index:          atoi(f[3]),
			ID:             f[4],
			PID:            atoi(f[5]),
			Command:        f[6],
			Path:           f[7],
			Title:          title,
			Active:         t[0] == "1",
			Dead:           t[1] == "1",
			WindowActivity: unix(t[2]),
			Width:          atoi(t[3]),
			Height:         atoi(t[4]),
		})
	}
	return res, nil
}

func lines(out string) []string {
	var res []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			res = append(res, l)
		}
	}
	return res
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func unix(s string) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// Exact returns the exact-name target for a session: tmux never treats it as a prefix or a
// pattern.
func Exact(name string) string { return "=" + name }

// NewSession creates a detached session and returns the name tmux gave it (tmux replaces "."
// and ":" with "_"). With cmd, the pane runs cmd: several arguments are executed directly by
// tmux, a single one goes through the default shell, as with tmux itself.
func (r Runner) NewSession(ctx context.Context, name, dir string, cmd []string) (string, error) {
	args := []string{"new-session", "-d", "-P", "-F", "#{session_name}", "-s", name}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	if len(cmd) > 0 {
		args = append(args, "--")
		args = append(args, cmd...)
	}
	out, err := r.Run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\r\n"), nil
}

// RenameSession renames exactly one session.
func (r Runner) RenameSession(ctx context.Context, oldName, newName string) error {
	_, err := r.Run(ctx, "rename-session", "-t", Exact(oldName), newName)
	return err
}

// KillSession closes exactly one session, by its exact name.
func (r Runner) KillSession(ctx context.Context, name string) error {
	_, err := r.Run(ctx, "kill-session", "-t", Exact(name))
	return err
}

// SendText types text literally into a pane (%N), then presses Enter in a separate call when
// enter is set.
func (r Runner) SendText(ctx context.Context, paneID, text string, enter bool) error {
	if text != "" {
		if _, err := r.Run(ctx, "send-keys", "-t", paneID, "-l", text); err != nil {
			return err
		}
	}
	if enter {
		if _, err := r.Run(ctx, "send-keys", "-t", paneID, "Enter"); err != nil {
			return err
		}
	}
	return nil
}

// Capture returns a pane's screen, joined (-J). history > 0 also includes that many lines of
// scrollback.
func (r Runner) Capture(ctx context.Context, paneID string, history int) (string, error) {
	args := []string{"capture-pane", "-p", "-J", "-t", paneID}
	if history > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(history))
	}
	return r.Run(ctx, args...)
}

// ActivePane returns the pane id of a session's current window's active pane.
func (r Runner) ActivePane(ctx context.Context, session string) (string, error) {
	out, err := r.Run(ctx, "display-message", "-p", "-t", Exact(session)+":", "#{pane_id}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SocketPath asks the running server for its socket path.
func (r Runner) SocketPath(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "display-message", "-p", "#{socket_path}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Version returns `tmux -V`.
func (r Runner) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", "-V").Output()
	if err != nil {
		return "", fmt.Errorf("tmux -V: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// InsideSameServer reports whether this process runs inside a tmux client of this runner's
// server ($TMUX names the socket first), in which case switch-client works.
func (r Runner) InsideSameServer(ctx context.Context) bool {
	env := os.Getenv("TMUX")
	if env == "" {
		return false
	}
	sock, _, _ := strings.Cut(env, ",")
	own, err := r.SocketPath(ctx)
	return err == nil && own == sock
}

// AttachCommand returns the command that puts the user's terminal on a session. Inside a
// client of the same server it is switch-client, which returns at once (inPlace). Otherwise it
// is attach-session, which takes over the terminal until the user detaches; $TMUX is removed
// so a client of another server can nest one.
func (r Runner) AttachCommand(ctx context.Context, name string) (cmd *exec.Cmd, inPlace bool) {
	if r.InsideSameServer(ctx) {
		return exec.Command("tmux", r.Args("switch-client", "-t", Exact(name))...), true
	}
	cmd = exec.Command("tmux", r.Args("attach-session", "-t", Exact(name))...)
	cmd.Env = withoutEnv(os.Environ(), "TMUX")
	return cmd, false
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}
