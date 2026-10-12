# AGENTS.md: tmux-manager-cli

This is the repo's memory file, for anyone (person or agent) changing this code.

`tmux-manager` (alias `tmm`) is one static Go binary. It lists tmux sessions, windows and panes,
and shows which coding agent runs in each pane, with that agent's status, session id, title and
path. It offers a TUI and a CLI. Usage is in [README.md](README.md).

## Layout

| Path                    | Job |
| ----------------------- | --- |
| `cmd/tmux-manager`      | `main`: `os.Exit(cli.Execute(os.Args[1:], cli.OSEnv()))` |
| `internal/tmux`         | **The only package that runs tmux.** `Runner{Socket, Path}` maps to `-L`/`-S`, always passes `-u`, and calls `exec.Command("tmux", args…)`, never a shell. Holds the two `-F` formats and their parsers, and the actions. Actions target `=name` or `%pane`. There is no kill-server path. |
| `internal/agents`       | **The only package that reads agent state files.** `Table()` holds the detector table, one entry per agent. Also the matcher, the process table (`ps` plus `/proc`), Claude's registry and transcript titles, and the per-agent state readers and screen patterns. |
| `internal/snapshot`     | The one model. `Collector.Collect` returns `Snapshot{Taken, Server, Sessions[Windows[Panes[Agent]]]}`. It also does exited tracking (60 s), `Diff` (log events), `Resolve` (TARGET), and pass-through actions. The TUI and the CLI reach tmux only through this package. |
| `internal/tui`          | Bubble Tea model (`model.go`), layout and drawing (`view.go`). `theme.go` holds the **one badge renderer**, plus truncation, age and path helpers that the CLI uses as well. |
| `internal/cli`          | Cobra commands (`cli.go`) and the **one table renderer** (`table.go`). |
| `internal/version`      | `Version`, `Commit`, `Date`, set by `-ldflags -X`. These are the only package-level mutable variables. |
| `scripts/release.sh`    | Cross-compiles five targets into `dist/*.tar.gz` and `dist/checksums.txt`. |
| `install.sh`            | POSIX installer (see README). |
| `.github/workflows/release.yml` | On a `v*` tag: test, `release.sh`, `gh release create` with `GITHUB_TOKEN`. |

How one refresh works:

1. `list-sessions` and `list-panes -a` run in parallel with one `ps -A -o pid=,ppid=,args=`.
2. For each live pane, the detectors walk `pane_pid` and its descendants breadth first, nearest
   first. They run in up to 8 goroutines.
3. Each agent pane gets one `capture-pane -p -J` of its visible screen. Panes without an agent
   are never captured.

With 35 panes and 23 agents on this machine, `agents --json` took a median of 73 ms and at most
97 ms over 20 runs, end to end. `ps` alone took 38 ms of that.

## Versions

| What | Version | Notes |
| ---- | ------- | ----- |
| Go | **1.27.2** | Installed 2026-10-12 into `/usr/local/go` from go.dev/dl. Pinned in `go.mod` and `release.yml`. PATH blocks between `# >>> go >>>` and `# <<< go <<<` in `/root/.zshrc` and `/root/.bashrc`. They are idempotent: re-running the writer and re-sourcing both leave PATH unchanged. |
| Bubble Tea | `charm.land/bubbletea/v2` v2.1.0 | |
| Bubbles | `charm.land/bubbles/v2` v2.2.1 | |
| Lip Gloss | `charm.land/lipgloss/v2` v2.0.6 | |
| cobra | `github.com/spf13/cobra` v1.10.2 | |
| staticcheck | `@master` (devel 2026-10-09) | The newest release, 2026.2.1 (0.8.1), fails on Go 1.27 with "export data version 5 is greater than maximum supported version 4". Install with `go install honnef.co/go/tools/cmd/staticcheck@master` until a tagged release supports 1.27. |
| GitHub Actions | `actions/checkout@v7`, `actions/setup-go@v7` | Upload uses the preinstalled GitHub CLI. |

## Decisions (item 5 of prompt 26)

- **cobra, not the standard flag package.** Chosen as recommended: it gives completion
  (`tmux-manager completion …`) and help for free, at a small size cost (release archives are
  2.5–2.8 MB).
- **charmbracelet v2 (Bubble Tea v2.1.0, Bubbles v2.2.1, Lip Gloss v2.0.6).** v2 is the newest
  major with stable tagged releases on the build day (`go list -m -versions` lists v2.0.x and
  v2.1.0 for Bubble Tea). It uses the `charm.land/...` import paths. The v1 line ends at
  bubbletea v1.3.10.
- **Waiting text for the four uninstalled agents: per-agent regexes from each agent's source.**
  Each regex has a comment pointing to the source file and lines. A dialog counts only when both
  halves of a pair are on the visible screen: a title and its choices. This keeps an old prompt
  in the scrollback from counting.
  **Every one of these patterns is unverified on a live session.** Only Claude Code's trust
  dialog was checked live.
- **The five agents are the ones the user chose.** Where the sources differ from the prompt's
  assumptions, this code follows the source (see per-agent facts):
  - Hermes and OpenCode keep sessions in SQLite, not one file per session.
  - Gemini always runs as two node processes.
  - Codex's native binary is named plain `codex`.
- **Hosting:** GitHub at `amirhosseinbanaei/tmux-manager-cli`. The repo does not exist yet.
  `install.sh` was tested only against a local server through `TMM_BASE_URL`.
- **"All devices":** 40×12 and up, local or SSH, on Linux, macOS and Termux, with or without
  colour and Unicode. No Windows.
- **Name:** the binary is `tmux-manager` and `tmm` is a symlink. The installer skips the alias
  when another `tmm` is on PATH, or when a non-symlink `tmm` is in the target directory.

Smaller decisions made while building:

- **No SQLite reader.** Reading Hermes' `state.db` or OpenCode's `opencode.db` would need a large
  pure-Go SQLite dependency, and would mean opening another program's live database.
  - Hermes gets its id from its live registry, `runtime/active_sessions.json`. Its title falls
    back to `pane_title`.
  - OpenCode gets its id from argv (`-s ses_…`) only. Otherwise it shows `—`, and Source says
    the sessions are in SQLite.
- **`s` (send) in the TUI.** `tab` toggles "and press Enter" (default on), and `enter` sends. The
  prompt says "Enter toggling", but Enter must also submit the line, so the toggle is on Tab.
- **Source keys.** Values come from the prompt's list (`registry`, `argv`, `state-dir`, `screen`,
  `pane-title`, `activity`), plus:
  - `proc-cwd` and `pane-path` for the path;
  - `agent=argv` to say how the process was matched;
  - for a missing id, `sessionId=none (<why>)`.
  The transcript title counts as `state-dir`.
- **`tmux -u` on every call.** Without it, tmux replaces tabs and non-ASCII with `_` when the
  locale is not UTF-8. That breaks the separator and Unicode names (seen live with `LANG=C`).
- **tmux renames sessions.** It turns `.` and `:` in session names into `_`. `new` passes
  `-P -F '#{session_name}'` and reports the real name. tmux prints a tab in a name as `\t`, so a
  raw tab in `-F` output is always a separator.
- **Single-argument commands.** tmux runs a single-argument pane command through `/bin/sh`.
  `new --agent` passes the resolved executable path when it has only safe characters, else
  `env <path>`, so no shell interprets it.
- **Attach.** Inside a client of the same server, attach is `switch-client`; the server is
  compared by `$TMUX` against `#{socket_path}`. Otherwise it is `attach-session` with `$TMUX`
  removed, which also works from inside another server.
- **Colour.** The TUI asks the terminal for its background colour and uses `LightDark` hex
  colours. The CLI uses the 16 ANSI colours, which each terminal theme tunes for its own
  background. Colour is off with `--no-color`, with `NO_COLOR`, or when not on a terminal.
  ASCII mode is on with `--ascii`, or when LC_ALL/LC_CTYPE/LANG (the first one set) is not
  UTF-8.
- **AGE in `agents`** is the agent process's uptime. The TUI list shows the age of the pane's
  last activity.
- **Exit code for an ambiguous TARGET** is 2 (bad usage). Not found is 3.

## Per-agent facts

All were checked on 2026-10-12. Each detector's comment in `internal/agents` repeats the exact
source lines.

| Agent | Checked | Process / argv | State | Session id | Screen |
| ----- | ------- | -------------- | ----- | ---------- | ------ |
| Claude Code | 2.1.296, live on this machine | Native ELF `claude` (`~/.local/bin/claude`, VS Code `…/native-binary/claude`); old npm: `node …/@anthropic-ai/claude-code/cli.js` | `$CLAUDE_CONFIG_DIR` or `~/.claude`. `sessions/<pid>.json` holds `sessionId cwd name status procStart …`, with status `busy`→working, `idle`→idle. `procStart` is checked against `/proc/<pid>/stat` field 22, so a reused pid is caught. Transcript: glob `projects/*/<id>.jsonl`; title is the last `custom-title`, else the last `ai-title`, read from a 64 KiB tail that doubles until a title is found, cached by mtime and size. | Registry; else `--resume`/`-r`/`--session-id` | Dialog regexes ported from tmux-claude.py:337-375 (trust, outside-read, permission, auto-mode). A dialog overrides the registry. No registry within 5 s → starting. |
| Codex CLI | rust-v0.162.1, https://github.com/openai/codex | `node …/@openai/codex/bin/codex.js` spawns `…/vendor/<triple>/bin/codex`. Also matches a platform-suffixed `codex-<triple>` (release tarball names). | `$CODEX_HOME` or `~/.codex`. `sessions/YYYY/MM/DD/rollout-*-<uuid>.jsonl`, whose first line is `session_meta` with `payload.id` and `payload.cwd`. Titles: `session_index.jsonl` `{id, thread_name}`, last wins. `.jsonl.zst` (compressed) rollouts are not read. | `codex resume <uuid>` / `codex fork <uuid>`; else the newest rollout with the same cwd and mtime after the process started | Working: `to interrupt)`. Waiting: "Would you like to run the following command?" (and the edits, permissions, network and MCP variants) together with "Yes, proceed" or "Press enter to confirm or esc to cancel" |
| Hermes Agent | v0.21.7, https://github.com/NousResearch/hermes-agent | `hermes`, `hermes-agent`, `python …/bin/hermes`, `python -m hermes_cli…`. A source install runs `python -I -c "…runpy.run_module('hermes_cli.main'…)"`, which is matched only by that `run_module` call. In TUI mode the processes are python → node → `python -m tui_gateway.entry`. | `$HERMES_HOME` or `~/.hermes`. Sessions are in SQLite `state.db` (**not read**). Live registry: `runtime/active_sessions.json` `{entries:[{session_id, pid, metadata.live_session_id}]}`. | Registry entry for the pid or a descendant; argv `-r/--resume` only when it is an id (`YYYYMMDD_HHMMSS_hex6`), never `latest` or a title | Working: `msg=interrupt`, `Ctrl+C to interrupt`, `running…`. Waiting: "Dangerous Command" or "approval required" with "Allow once" / `Choice [o/s/a/D]` |
| Gemini CLI | v0.63.0, https://github.com/google-gemini/gemini-cli | `node …/bin/gemini` or `…/@google/gemini-cli/bundle/gemini.js`. **It always relaunches itself as a child node with the same args**, and the nearer (parent) process is reported. | `$GEMINI_CLI_HOME` or home, then `.gemini/`. `projects.json` `{projects:{cwd:slug}}` → `tmp/<slug>/chats/session-*.jsonl`, whose first line has `sessionId`. Title: last `{"$set":{"summary":…}}` in a 64 KiB tail. | `-r/--resume <uuid>`, `--session-id <uuid>`; else the newest chat file for the cwd's slug with mtime after the process started | Working: `(esc to cancel, 12s)`. Waiting: "Allow execution of …?", "Apply this change?", "Do you want to proceed?" or "Waiting for user confirmation" with "Allow once" / "Allow for this session" |
| OpenCode | v1.19.0, https://github.com/anomalyco/opencode (sst/opencode redirects) | The native binary runs directly: `bin/opencode.exe` (npm `opencode-ai`, same name on every OS), `opencode`, legacy `.opencode`; or node or bun on `…/opencode-ai/…` | SQLite `$OPENCODE_DB` or `$XDG_DATA_HOME/opencode/opencode.db` (**not read**) | argv `-s/--session ses_…` only | Working: `esc interrupt` / `esc again to interrupt`. Waiting: "Permission required" with "Allow once" |

When neither the screen nor the state decides, the four non-Claude agents use pane activity:
`window_activity` within 3 s means working, otherwise idle. Confidence is then `low`. Path is
`/proc/<pid>/cwd` on Linux, else `pane_current_path`; for Claude it is the registry's cwd. No
id is ever guessed: a missing id shows as `—`, and Source says why.

**Adding an agent** takes one entry in `Table()`: a `matcher{names, patterns, packages, modules}`
and an `Enrich`. Usually that Enrich is `stateful{argvID, state, working, waiting}.enrich`. Add
matcher rows, including negatives, to `TestMatcher`, and a fixture under
`internal/agents/testdata/`.

## Hard rules

They held while this was built, and they hold for any change.

- **Never kill by pattern.** No `pkill`, no `killall`, and no `kill-server` on the default
  server. Kill a test process by the pid you started it with. The code has no kill-server path,
  and `kill` closes one session by its exact name (`=name`).
- **Every integration test and manual check that creates or kills sessions uses a private
  server**, `tmux -L tmm-test-<random>`. When it finishes, it removes only that server, with
  `kill-server` on that `-L` socket.
- **Against the default server, run only read-only commands:** `ls`, `agents`, `log`, `peek`,
  `doctor`, and the TUI without actions.
- **Never send keys to, rename or kill a session you did not create.**
- No cgo. No shell strings: every external command is `exec.Command` with an argument list.
- Errors are wrapped with context. No panic in a path a user can reach.
- No global mutable state except `internal/version`.

## Verification gate

From the repo root. All five must pass:

```sh
gofmt -l . (prints nothing) && go vet ./... && staticcheck ./... && go test -race ./... && go build ./...
```

What the tests cover:

- The matcher table, with negatives such as `grep claude`, `nvim claude.md` and
  `python3 tmux-claude.py`.
- The `-F` parsers, with spaces, dots, colons, Unicode and tabs.
- Claude's registry and title, using fixtures and a `CLAUDE_CONFIG_DIR` override. This includes
  the tail extension and the cache invalidation.
- Fixture state dirs for Codex, Hermes and Gemini, and argv for OpenCode.
- Screen patterns for each agent.
- Snapshot assembly, `Diff`, `Resolve` (including an ambiguous target) and exited tracking.
- TUI golden renders at 40×12, 80×24 and 140×40, plus a narrow ASCII detail view, the kill
  dialog and the new-session form, all by a direct `View`/`Render` call. Run
  `go test ./internal/tui -update` to rewrite them.
- Mouse handling, the filter and the forms.
- CLI tables, exit codes, and kill refusing to run without `--yes` off a terminal.
- Integration tests on a private `-L tmm-test-<random>` server: create, rename, send, capture,
  kill, and "no server running". They are skipped when tmux is not on PATH.

Manual checks done on 2026-10-12, all on private sockets:

- Four fake agents (`bash -c 'exec -a codex sleep 600'` and the like) were listed with
  `agent=argv` and id `—`.
- A real Claude Code in an untrusted scratch folder showed `waiting`, with `status=screen` and
  the note "trust: the workspace trust dialog is open". The dialog was not answered.
- The TUI ran at 40×12, 80×24 and 160×45 with nothing wrapping. `n`, `r`, `s`, `x` (default No,
  then `y`), `/` and `g` were driven with send-keys.
- Attach from a TUI running on a second private server suspended the TUI. Detaching resumed it.
- 35 panes: 73 ms median for `agents --json`.
- `install.sh` was tested against a local `python3 -m http.server`-style server, under dash,
  bash, zsh and busybox sh, with curl and with wget only. It worked for the latest version
  through the redirect, for a pinned `TMM_VERSION`, and for a re-run (upgrade). It refused both
  a tampered checksum and a tampered archive. It skipped the `tmm` alias on a collision and
  honoured `TMM_NO_ALIAS`.

Not verified:

- The four uninstalled agents (Codex, Hermes, Gemini, OpenCode) were not checked on a live
  session. Their argv shapes, state layouts and screen strings come from source only.
- The darwin and armv7 binaries were built but not run.
- Termux was not run. Its install path (`$PREFIX/bin`) is chosen from `TERMUX_VERSION` or
  `PREFIX`.
- The GitHub workflow was not run.
- The latest-version redirect was tested only against a local stand-in for GitHub's
  `/releases/latest`.
- OSC 52 copy needs a terminal that accepts it. Inside tmux, it also needs `set-clipboard on`.
