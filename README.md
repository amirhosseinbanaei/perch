# perch

`perch` lists every tmux session, window and pane. For each pane running
one of five coding agents, it shows the agent's **status, session id, title and working path**,
refreshed live:

- Claude Code
- OpenAI Codex CLI
- Hermes Agent
- Google Gemini CLI
- OpenCode

You can use it as a full-screen TUI or as plain commands that print tables or JSON.

- It fits any terminal from a 40×12 phone screen (Termux, Blink, SSH) to 200+ columns.
- It works with or without colour and Unicode, and needs no Nerd Font.
- Mouse and touch scrolling work where the terminal sends them.
- It is one static binary for Linux and macOS (amd64, arm64) and Linux armv7.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/amirhosseinbanaei/perch/main/install.sh | sh
```

The script picks the build for your OS and CPU and checks it against `checksums.txt`. It
installs `perch`. Running it again upgrades in place, and it never calls `sudo` itself.

| Variable            | Effect                                                                                                   |
| ------------------- | -------------------------------------------------------------------------------------------------------- |
| `PERCH_VERSION`     | Install this version (`0.2.0` or `v0.2.0`) instead of the latest release                                 |
| `PERCH_INSTALL_DIR` | Install here. Default: `$PREFIX/bin` on Termux, else `/usr/local/bin` when writable, else `~/.local/bin` |
| `PERCH_BASE_URL`    | Download from this base instead of GitHub Releases (files under `download/v<version>/`)                  |

To install system-wide when `/usr/local/bin` is not writable:
`curl -fsSL …/install.sh | sudo sh`.

tmux itself is needed:

| System        | Command               |
| ------------- | --------------------- |
| Debian/Ubuntu | `apt install tmux`    |
| Fedora        | `dnf install tmux`    |
| Arch          | `pacman -S tmux`      |
| Alpine        | `apk add tmux`        |
| macOS         | `brew install tmux`   |
| Termux        | `pkg install tmux`    |

## The TUI

Run `perch` with no arguments in a terminal. The layout follows the terminal's
width:

| Width        | Layout                                                                     |
| ------------ | -------------------------------------------------------------------------- |
| under 60     | One list of sessions. Enter opens a full-screen detail view, Esc goes back |
| 60–99        | The list, with the detail below it                                         |
| 100 and wider | The list on the left, the detail on the right                             |

Under 16 rows, the preview is hidden.

Each session shows its name, the window count, and `att` when a client is attached. Under it
are its agent panes: the agent, a status badge, the title and the age of the last activity.

| Status   | Badge | ASCII |
| -------- | ----- | ----- |
| working  | `●`   | `*`   |
| waiting  | `◆`   | `!`   |
| idle     | `○`   | `o`   |
| exited   | `✕`   | `x`   |
| starting | `…`   | `~`   |

`waiting` means the agent needs a person: a permission or trust dialog, or a question. An agent
that ends while its pane stays open is shown as `exited` for 60 s.

The detail pane shows the full session id, title, path, pid, uptime, and where each field came
from (Source and Confidence). Below them is a live preview of the pane.

| Key            | Action                                                                     |
| -------------- | -------------------------------------------------------------------------- |
| `↑` `↓` / `j` `k` | Move (also PgUp/PgDn, Home, End/`G`)                                     |
| `enter`        | Attach to the session; in the narrow layout, open the detail view          |
| `a`            | Attach                                                                     |
| `n`            | New session: name, directory, optional agent                               |
| `r`            | Rename the session                                                         |
| `x`            | Kill the session. It asks first, and the default is No                     |
| `s`            | Send text to the pane. `tab` toggles "and press Enter"                     |
| `p`            | Toggle the preview                                                         |
| `/`            | Filter by session, agent, status, title or path. Esc clears it             |
| `g`            | Show only sessions with agents                                             |
| `l`            | The log of changes seen in this run                                        |
| `y`            | Copy the session id with OSC 52, which also works over SSH                 |
| `R`            | Refresh now                                                                |
| `?`            | Help                                                                       |
| `q` / `ctrl+c` | Quit                                                                       |

No key needs Alt or a function key. The mouse wheel scrolls, a click selects, and a click on the
selected row opens it.

Attaching outside tmux suspends the TUI. Detaching (`ctrl-b d`) brings it back. Inside tmux, it
switches your client to the session and keeps the TUI running.

## Commands

```text
perch                     the TUI on a terminal; otherwise the same as `ls`
perch ls [--json]         sessions: name, windows, attached, created, activity, agents
perch agents [--json] [--agent ID] [--status S]
                                 one row per agent pane: TARGET AGENT STATUS SESSION-ID TITLE PATH AGE
perch log [--follow] [--interval 2s] [--json] [--file PATH]
                                 a status report; --follow prints one line per change
                                 (JSON Lines with --json); --file appends to PATH as well
perch new NAME [-c DIR] [--agent ID | -- CMD...]
perch attach TARGET
perch rename OLD NEW
perch kill NAME [--yes]   asks on a terminal; refuses without --yes otherwise
perch send TARGET TEXT [--enter]
perch peek TARGET [--lines N]
perch doctor              tmux, socket, agents on PATH, state dirs, UTF-8, colour
perch version
perch completion bash|zsh|fish|powershell
```

Global flags:

| Flag          | Effect                                         |
| ------------- | ---------------------------------------------- |
| `-L SOCKET`   | Same as tmux's `-L`                            |
| `-S PATH`     | Same as tmux's `-S`                            |
| `--no-color`  | No colour. `NO_COLOR` is honoured too           |
| `--ascii`     | ASCII only. Automatic when the locale is not UTF-8 |
| `--verbose`   | Adds PID, CONF, SOURCE and NOTE columns        |

A TARGET is a session name, `session:window.pane`, or a pane id `%N`. A target that matches more
than one thing is rejected.

| Exit code | Meaning          |
| --------- | ---------------- |
| 0         | ok               |
| 1         | error            |
| 2         | bad usage        |
| 3         | target not found |

The `--json` output of `ls` is the same snapshot the TUI draws.

## How agents are found

Each refresh reads tmux twice (`list-sessions` and `list-panes -a`) and runs `ps` once. For each
pane it walks the processes under the pane, nearest first. A process counts only by the start of
its arguments: the executable's name, or the script an interpreter runs. A `grep claude` or an
`nvim claude.md` never counts. Per-agent details and their sources are in [AGENTS.md](AGENTS.md).

## Publishing a release

To publish a new version:

1. Merge your branch into `main` and `git push origin main`.
2. `git tag v<version> && git push origin v<version>`, for example `v0.2.0`.

The `release` workflow then runs `scripts/release.sh` and attaches the archives and
`checksums.txt` to the GitHub Release. From then on, the one-line installer works.

To build the archives locally: `scripts/release.sh 0.2.0` writes them to `dist/`.

## License

MIT, © amir hossein banaei.
