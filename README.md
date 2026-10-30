# pfqueue

Realtime queue scanner and manager for Postfix (2.x and later) and Exim
(3, 4) — a Go port of pfqueue 0.6.0 by Stefano Rivoir.

The terminal interface, key bindings, command line options, configuration
files and the `spfqueue` socket protocol are kept compatible with the C
version.

## Build

```sh
make            # builds bin/pfqueue
make test       # unit tests (with race detector)
make lint       # golangci-lint
make help       # all targets
```

Requires Go 1.27 or newer. Without make: `go build -o pfqueue ./cmd/pfqueue`.

## Usage

```
pfqueue [-ehvn] [-q queue_num] [-m max_msg]
        [-b postfix2|exim|socket] [-B backends_path]
        [-f ncurses|socket] [-F frontends_path]
        [-s autorefresh_seconds] [-l limit_seconds] [-d scan_delay]
        [-p executables_path] [-c config_path] [-r remote_host]
```

pfqueue must be run as root.

| Option          | Description                                                                                     |
|-----------------|-------------------------------------------------------------------------------------------------|
| `-b backend`    | `autodetect` (default, tries exim, then postfix2), `postfix2` (alias `postfix`), `exim` or `socket` |
| `-f frontend`   | `ncurses` (default, terminal UI) or `socket` (serve the queue over TCP)                         |
| `-q queue`      | Start with queue 1 deferred, 2 active, 3 incoming, 4 hold                                       |
| `-m max`        | Maximum number of messages shown in a queue (default 200, at least 5)                           |
| `-s seconds`    | Screen refresh interval (default 1, 1–300)                                                      |
| `-d seconds`    | Delay between queue scans (default 1)                                                           |
| `-l seconds`    | Time limit for a single queue scan (1–300, default unlimited); results may be incomplete        |
| `-e`            | Read From/To from the envelope instead of the headers (postfix only)                            |
| `-n`            | Disable colors                                                                                  |
| `-p path`       | Directory containing the MTA executables (`postcat`, `postsuper`, `postconf`, `exim`, …)        |
| `-c config`     | Custom MTA configuration: a directory for postfix, a file for exim                              |
| `-r host[:port]`| Remote host for the socket backend                                                              |
| `-B`, `-F`      | Accepted for compatibility, ignored (backends and frontends are built in)                       |
| `-v`, `-h`      | Show version / usage                                                                            |

Exit codes: 0 ok, 253 (-3) not root, 252 (-4) backend cannot be started,
254 (-2) frontend cannot be started.

## Screen

```
 Queue: 'deferred', 30 messages, 2 tagged, unsorted          ATCSB   <- header
 ID                 From                      To
 4AB001C01          Sender <s@example.org>    Empfänger <r@example.com>
 ...                                                                  <- message list
┌─Message (from headers)─────────────────────────────────────────────┐
│Message: 4AB001C01                                                  │
│From..: / To....: / Subj..: / Status:                               │  <- current message
└────────────────────────────────────────────────────────────────────┘
┌─Body of …──────────────────────────────────────────────────────────┐
│  (optional body window, toggled with b)                            │
└────────────────────────────────────────────────────────────────────┘
```

The flags at the right of the header are shown reversed when active:
**A** auto-work-on-tagged, **T** work-on-tagged, **C** ask for confirmation,
**S** queue scanning, **B** automatic body display. Tagged messages are shown
in bold (yellow with colors).

## Key bindings

Press `?` inside the program for the built-in help.

### Navigation

| Key                    | Action                          |
|------------------------|---------------------------------|
| `↑` / `↓`              | Previous / next message         |
| `PgUp` / `PgDn`        | Page up / down                  |
| `g` / `Home`           | First message                   |
| `G` / `End`            | Last message                    |
| `1` `2` `3` `4` `5`    | Switch queue: deferred, active, incoming, hold, corrupt (postfix 2.x); exim has a single queue |

### Message actions

| Key            | Action                                  |
|----------------|-----------------------------------------|
| `d` / `Del`    | Delete message                          |
| `h`            | Hold message                            |
| `l`            | Release message from the hold queue     |
| `r`            | Requeue message                         |
| `c`            | Toggle confirmation request for actions |

Actions work on the current message, or on all tagged messages when
work-on-tagged (`;`) or auto-work-on-tagged (`:`) is active.

### Tagging

| Key          | Action                                                              |
|--------------|---------------------------------------------------------------------|
| `t` / `Space`| Tag/untag message and move down; with a mark set, tag the whole block |
| `m`          | Mark message as start of a tag block                                |
| `a`          | Tag all messages                                                    |
| `u`          | Untag all messages                                                  |
| `;`          | Toggle work-on-tagged: actions apply to all tagged messages         |
| `:`          | Toggle auto-work-on-tagged: actions apply to tagged messages if any |

### Searching

| Key | Action                                     |
|-----|--------------------------------------------|
| `/` | Search for a regular expression            |
| `n` | Search next                                |
| `p` | Search previous                            |
| `T` | Search and tag all matching messages       |

The expression may be prefixed with `f:` (From), `t:` (To), `e:` (From or
To) or `s:` (Subject); without prefix all three fields are searched.
Expressions use Go/RE2 syntax. `Esc` cancels the input.

### Message body

| Key       | Action                                                        |
|-----------|---------------------------------------------------------------|
| `s`       | Show the body in a full screen window                         |
| `Enter`   | Show/refresh the body (like `s` when the body window is hidden) |
| `b`       | Show/hide the body window                                     |
| `B`       | Toggle automatic body display in the body window              |
| `>` / `<` | Increase / decrease body window height                        |
| `.` / `,` | Scroll body window down / up                                  |

In the full screen body window: `↑`/`↓`/`Space` scroll, `PgUp`/`PgDn`
page, `g`/`Home` and `G`/`End` jump to start/end, `q`/`Esc` close.

### Display and misc

| Key      | Action                                                       |
|----------|--------------------------------------------------------------|
| `S`      | Sort: 0 unsorted, 1/2/3 by from/to/subject, 4/5/6 descending |
| `e`      | Toggle From/To from envelope or headers (postfix only)       |
| `-`      | Suspend/resume queue scanning (freeze the list)              |
| `+`      | Toggle colors                                                |
| `Ctrl-L` | Redraw the screen                                            |
| `?`      | Help (any key: next page, `q`: close)                        |
| `q`      | Quit (also `Ctrl-C`)                                         |

Sorting needs the headers of every message in the queue and can be slow on
large queues.

## Configuration

At startup `/etc/pfqueue.conf` and then `~/.pfqueue` are read; the latter
overrides the former, command line options override both. Lines have the
form `option=value` (whitespace is ignored), lines starting with `#` are
comments.

| Option           | Same as | Description                                   |
|------------------|---------|-----------------------------------------------|
| `backend_name`   | `-b`    | Backend to use                                |
| `frontend_name`  | `-f`    | Frontend to use                               |
| `mta_config`     | `-c`    | MTA configuration directory or file           |
| `mta_bin`        | `-p`    | Path to the MTA executables                   |
| `max_messages`   | `-m`    | Maximum number of messages                    |
| `default_queue`  | `-q`    | Queue at startup (1–4, see `-q`)              |
| `scan_delay`     | `-d`    | Seconds between queue scans                   |
| `scan_limit`     | `-l`    | Time limit for a single scan                  |
| `autorefresh`    | `-s`    | Seconds between screen refreshes              |
| `use_envelope`   | `-e`    | `yes` / `no`                                  |
| `use_colors`     | `-n`    | `yes` / `no`                                  |
| `remote_host`    | `-r`    | Host for the socket backend                   |
| `remote_port`    |         | TCP port of the socket protocol (default 20000) |
| `backends_path`, `frontends_path` | | Accepted, ignored                  |

Example:

```
# /etc/pfqueue.conf
backend_name=postfix2
max_messages=500
use_colors=yes
```

## Remote mode

Run `pfqueue -f socket` on the mail server (listens on TCP port 20000,
`remote_port` in the config file) and `pfqueue -b socket -r host` (or
`-r host:port`) on the client. The protocol has no authentication; only
expose it on trusted networks.

## Layout

| Path               | Content                                         |
|--------------------|-------------------------------------------------|
| `cmd/pfqueue`      | Main program, option parsing                    |
| `internal/config`  | Configuration files                             |
| `internal/pfq`     | Queue library: scanning, tagging, search, sort  |
| `internal/backend` | MTA backends: postfix, exim, socket             |
| `internal/tui`     | Terminal frontend (tcell)                       |
| `internal/server`  | Socket frontend                                 |
| `internal/proto`   | Socket protocol constants                       |

## Differences to the C version

- Postfix 1.x is no longer supported; the `postfix1` backend was removed.
- Backends and frontends are compiled in instead of loaded with `dlopen()`.
  `-B`, `-F`, `backends_path` and `frontends_path` are accepted but ignored.
- MTA tools (`postcat`, `postsuper`, `exim`, …) are executed directly, not
  through `/bin/sh`, so message ids cannot be interpreted by a shell.
- Search uses Go regular expressions (RE2 syntax) instead of POSIX basic
  regular expressions; plain strings and `.`/`*`/`^`/`$` behave the same.
- From/To/Subject are decoded from RFC 2047 (`=?UTF-8?...?=`) for display
  and search.
- Tags stay attached to their message when the queue is rescanned (the C
  version tracked them by list position).
- Fixed: `-d` (scan delay) and `-s`/`autorefresh` (screen refresh) now work,
  `p` (search previous) moves the cursor, the config source is shown
  correctly in the help, `default_queue` uses the same numbering as `-q`,
  the envelope title updates immediately, BODY transfer in the socket
  protocol handles multi-line bodies, the exim backend only scans
  `<spool>/input`.
- The socket frontend serves several clients concurrently.
