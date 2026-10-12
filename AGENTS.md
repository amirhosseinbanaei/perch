# AGENTS.md: perch

This is the repo's memory file, for anyone (person or agent) changing this code.

`perch` is one static Go binary. It lists tmux sessions, windows and panes,
and shows which coding agent runs in each pane, with that agent's status, session id, title and
path. It offers a TUI and a CLI. Usage is in [README.md](README.md).

## Layout

| Path                    | Job |
| ----------------------- | --- |
| `cmd/perch`      | `main`: `os.Exit(cli.Execute(os.Args[1:], cli.OSEnv()))` |
| `internal/tmux`         | **The only package that runs tmux.** `Runner{Socket, Path}` maps to `-L`/`-S`, always passes `-u`, and calls `exec.Command("tmux", args…)`, never a shell. Holds the two `-F` formats and their parsers, and the actions. Actions target `=name` or `%pane`. There is no kill-server path. |
| `internal/agents`       | **The only package that reads agent state files.** `Table()` holds the detector table, one entry per agent. Also the matcher, the process table (`ps` plus `/proc`), Claude's registry and transcript titles, and the per-agent state readers and screen patterns. |
| `internal/snapshot`     | The one model. `Collector.Collect` returns `Snapshot{Taken, Server, Sessions[Windows[Panes[Agent]]]}`. It also does exited tracking (60 s), `Diff` (log events), `Resolve` (TARGET), and pass-through actions. The TUI and the CLI reach tmux only through this package. |
| `internal/tui`          | Bubble Tea model (`model.go`), layout, panels and drawing (`view.go`), the command bar (`cmdbar.go`), modals (`modal.go`), spinner, pulse and toasts (`motion.go`), the logo (`logo.go`). `theme.go` holds the **one badge renderer**, plus truncation, age and path helpers that the CLI uses as well. `palette.go` is **the only file with hex colours**; `styles.go` compiles them into styles once per theme change. |
| `internal/config`       | Reads and writes `$XDG_CONFIG_HOME/perch/config.json` (else `~/.config/perch/config.json`): `{"theme": …}`. Keeps unknown keys; writes atomically (temp file, rename). `ResolveTheme` is the flag → `PERCH_THEME` → file → coral order. |
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
  (`perch completion …`) and help for free, at a small size cost (release archives are
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
- **Hosting:** GitHub at `amirhosseinbanaei/perch` (first published as `tmux-manager-cli`).
  `install.sh` was tested only against a local server through `PERCH_BASE_URL`.
- **"All devices":** 40×12 and up, local or SSH, on Linux, macOS and Termux, with or without
  colour and Unicode. No Windows.
- **Name:** `perch`. Version 0.1.0 was built and released as `tmux-manager`, with a `tmm`
  alias. It was renamed on 2026-10-12, and there is no alias any more: the name is already short.

Smaller decisions made while building:

- **No SQLite reader.** Reading Hermes' `state.db` or OpenCode's `opencode.db` would need a large
  pure-Go SQLite dependency, and would mean opening another program's live database.
  - Hermes gets its id from its live registry, `runtime/active_sessions.json`. Its title falls
    back to `pane_title`.
  - OpenCode gets its id from argv (`-s ses_…`) only. Otherwise it shows `—`, and Source says
    the sessions are in SQLite.
- **`s` (send) in the TUI.** `tab` toggles "and press Enter" (default on), and `enter` sends. The
  prompt says "Enter toggling", but Enter must also submit the line, so the toggle is on Tab.
  `/send <text>` always presses Enter.
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

## TUI design system (prompt 28)

The TUI looks and moves like Claude Code's CLI: rounded panels, one accent colour, bold titles,
normal values, dim labels, small motion, and an input bar that drives everything. The data,
refresh, actions and every CLI output are unchanged.

**Palette roles** (`palette.go`, one `Palette` per theme and variant): Accent (focused borders and
titles, the selection bar, the logo), AccentSoft (the gradient's end), Text, Muted (labels, ages,
hints), Faint (unfocused borders), Surface (modal and selected-row background), Working, Waiting,
Idle, Exited, Danger, Success, and one colour per agent. Status and agent colours are shared by
the three themes, so a status reads the same in each.

| Theme | Variant | Accent | AccentSoft | Text | Muted | Faint | Surface |
| ----- | ------- | ------ | ---------- | ---- | ----- | ----- | ------- |
| coral (default) | dark | `#D97757` | `#F4B183` | `#E8E4DF` | `#9C958E` | `#736C66` | `#2D2A27` |
| coral | light | `#C15F3C` | `#C9772E` | `#2B2622` | `#6B635C` | `#8E8780` | `#F3EFEA` |
| teal | dark | `#2EC4B6` | `#7BDFF2` | `#E2E8F0` | `#94A3B8` | `#64748B` | `#243040` |
| teal | light | `#0F8B8D` | `#2B6CB0` | `#1E293B` | `#526173` | `#7F8FA3` | `#EEF3F6` |
| violet | dark | `#A78BFA` | `#F472B6` | `#E4E4E7` | `#A1A1AA` | `#71717A` | `#2A2633` |
| violet | light | `#7C3AED` | `#DB2777` | `#18181B` | `#52525B` | `#8B8B94` | `#F4F0FF` |

Shared, dark / light: working `#4EC27A` / `#1A7F37`, waiting `#E5B84B` / `#9A6700`, idle
`#A39E99` / `#6E7781`, exited and danger `#FF7B72` / `#CF222E`; agents claude `#E0896A` /
`#B85A3A`, codex `#6CB6FF` / `#0B66C3`, gemini `#A9A1FF` / `#6E56CF`, hermes `#F28FAD` /
`#B4366A`, opencode `#56C8D8` / `#0B7A87`. The prompt's starting accents were kept; the greys
and the gradient ends were chosen against `TestContrast`, which requires 4.5:1 for Text, Muted
and Danger and 3:1 for Accent, Faint, AccentSoft, status and agent colours, against `#1E1E1E`
(dark) and `#FFFFFF` (light), and the same on Surface for everything drawn on it. The first
Faint picks (`#6E6862`-ish) were under 3:1 and were lightened.

- The variant follows `BackgroundColorMsg`; until it arrives the TUI uses the dark one.
- Colour goes through lipgloss and the terminal's colour profile, so 256- and 16-colour
  terminals get the nearest colours (checked inside tmux at 256 colours). With colour off,
  nothing but bold, faint and reverse is emitted, and status comes from the symbols.
- Styles are compiled once per theme, background or colour change (`Theme.Compile`) into
  escape-sequence pairs (`paint`). Nothing builds a lipgloss style per frame. `Render` of
  35 panes at 140×40 takes about 1.2–1.35 ms (`BenchmarkRender`).
- The CLI never compiles a theme (`Theme.Name` empty), so its tables keep the 16 ANSI colours.

**Panels and focus.** Rounded borders (`╭╮╰╯`), or `+ - |` in ASCII mode. A title goes in the
top edge and a position (`3/12`) in the bottom edge when a panel scrolls. Tab and Shift-Tab move
focus between Sessions and Detail; the focused panel's border and title are Accent, the other's
border is Faint and its title Muted (Faint text is only 3:1, too low for a label). A click
focuses the panel it lands in. Arrows act on the focused panel; in Detail they scroll the
live preview. Every rendered line fits the width (`TestEveryWidth`: 40–200 columns).

**Motion.** A spinner (`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`, or `| / - \`) on working agents at 100 ms; a 3 s pulse
(300 ms steps) between Waiting and the agent colour on a row that turns waiting, with a 4 s toast
"◆ codex in api-server needs you" and, with `--bell`, one `\a`. The animation tick runs only
while a working agent or a pulse exists; a toast has its own one-shot 4 s timer. The 2 s
`tickCmd` is still the only refresh. `--no-anim` or `PERCH_NO_ANIM=1` stops the spinner and the
pulse (toasts still show); no colour also means no animation. Text inputs do not blink, so an
open input runs no timer. Measured with 8 agents on a private server: 2.9% CPU with a spinner
visible, 1.4% for the same TUI with `--no-anim` (that is the 2 s refresh), 0.9% for a TUI with
no agents.

**Toasts** sit at the right end of the footer on narrow screens, and as a rounded mini-panel
above the command bar from 100 columns with the full chrome. One at a time; newer replaces
older.

**The command bar replaces "/" as the filter key.** `/` or `:` opens it with a popup of
commands (prefix matches, then subsequence matches; Tab completes, Up/Down choose, Enter runs).
Any printable key that is not a shortcut (`jkGanrxspglRy?tq`) opens it as a live filter. A
`/word` that names no command filters by `word`, so the old `/text` habit still works. `/kill`
only ever opens the confirm modal. Errors stay in the bar and show under it, in Danger.

**Modals** (new, rename, send, kill, help) are centred panels, min(60, w−4) wide, or w−2 under
60 columns. The keys of each form are as before, except that new's agent is now a picker
(←→) of "shell" and the agents found on PATH, instead of a text field.

**Config file.** `internal/config`; see the layout table. A save failure shows as a toast and
keeps the theme for the run.

**Open decisions, as chosen:**

- **Logo:** two candidates, block elements (A) and rounded box lines (B), are in
  `docs/screenshots/logo-candidates.png`. A was kept: at 24 columns B reads as a box, not a
  bird. ASCII mode uses a separate ASCII bird. The logo is dropped under 12 rows.
- **Surface backgrounds** (modals, the selected row, the popup's selection) are drawn only once
  `BackgroundColorMsg` has reported the background. Before that, and in terminals that never
  answer (tmux without an attached client, for one), only borders and the `▌` bar mark them,
  so a guessed background never clashes. The Surface fills a modal's inside, not its border
  cells.

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
  server**, `tmux -L perch-test-<random>`. When it finishes, it removes only that server, with
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
- TUI golden renders for each theme at 40×12, 80×24 and 140×40; `--ascii` and `--no-color` at
  80×24; the empty state at 80×24 (logo) and 40×10 (none); the command popup and every modal at
  80×24 and 40×12; toasts; a narrow ASCII detail view. The spinner frame and the clock are
  pinned. All by a direct `Render` call; `go test ./internal/tui -update` rewrites them.
- Every width from 40 to 200 in several modes, for line width.
- Mouse handling, focus, typing to filter, the forms, the command bar (matching, parsing,
  errors, `/kill` only confirming), theme cycling and saving, the waiting pulse and toast, that
  no animation tick is scheduled with no working agent, and the palette contrast ratios.
- `internal/config`: bad JSON, an unknown theme, unknown keys kept, atomic writes, the
  resolution order.
- `BenchmarkRender`: 35 panes at 140×40.
- CLI tables, exit codes, and kill refusing to run without `--yes` off a terminal.
- Integration tests on a private `-L perch-test-<random>` server: create, rename, send, capture,
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
  through the redirect, for a pinned `PERCH_VERSION`, and for a re-run (upgrade). It refused both
  a tampered checksum and a tampered archive.

Manual checks for the panels and themes (prompt 28), 2026-10-12, on private servers only
(`perch-test-<n>` with the sessions, and `perch-test-<n>-ui` running the TUIs so they did not
list themselves):

- Three fake agents (codex working via a tick loop, gemini and opencode idle), a plain shell
  and `ünïcode-проект`. Each theme at 40×12, 80×24 and 140×40, captured with
  `capture-pane -e`. freeze 0.2 rendered, but its built-in font lacks `⠋ ✦ ↻`, and embedding
  DejaVu Sans Mono gave blank text, so the PNGs in `docs/screenshots/` come from a small
  throwaway Go renderer (x/image, DejaVu Sans Mono with fallbacks, box lines drawn as lines).
  It was not committed.
- Tab focus; `t` wrote `config.json` (`XDG_CONFIG_HOME` pointed at a scratch dir) and a
  restart came up in the saved theme. `/new`, typing to filter and Enter/Esc, `/send` (the text
  arrived in the shell), `/kill` → No, `x` → Yes on a session made for it, `/theme pink`
  showing its error under the bar.
- A real `claude` in an untrusted `mktemp -d` folder: the row turned waiting, the toast
  appeared and cleared after 4 s, and the row's colour alternated between waiting and Claude's
  colour for 3 s. The trust dialog was not answered.

Not verified:

- The light variants on a real light terminal: tmux without a client does not report a
  background, so only the contrast test covers them. For the same reason the Surface
  background was seen only in the golden renders.
- `--bell` on a real terminal (the test only checks that it is sent once).

- The four uninstalled agents (Codex, Hermes, Gemini, OpenCode) were not checked on a live
  session. Their argv shapes, state layouts and screen strings come from source only.
- The darwin and armv7 binaries were built but not run.
- Termux was not run. Its install path (`$PREFIX/bin`) is chosen from `TERMUX_VERSION` or
  `PREFIX`.
- The GitHub workflow was not run.
- The latest-version redirect was tested only against a local stand-in for GitHub's
  `/releases/latest`.
- OSC 52 copy needs a terminal that accepts it. Inside tmux, it also needs `set-clipboard on`.
