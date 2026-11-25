# Preface

Notes on running the Buildtest daemon as a systemd user service, and on the
`systemd/install.sh` installer. Read this before touching `systemd/user/` or
the install script.

IMPORTANT: Read the `.kb/agents.md` file.


# Concepts

## Unit design (`systemd/user/buildtest.service`)

- `Type=notify` works because the daemon calls `sd_notify("READY=1")` from the
  main process (`internals/daemon/daemon.go`, `Start`), so the default
  `NotifyAccess=main` suffices. Do not add `NotifyAccess=all`.
- `Environment=BUILDTEST=%h/...` is valid: systemd expands `%h` in
  `Environment=` (via `unit_env_printf`).
- Restart exit codes: the daemon exits 10 (service failure), 11 (check
  failure), or 42 (wait for socket activation) to ask systemd to restart it
  (`internals/cli/cmd_run.go`, `run`). These must NOT be listed in
  `SuccessExitStatus=` — that would make `Restart=on-failure` treat them as
  success and refuse to restart. Use `RestartForceExitStatus=10 11 42`
  instead.
- The unit has an `[Install]` section (`WantedBy=default.target`); without it
  `systemctl --user enable` fails.

## Why there is no socket unit

The daemon does NOT support systemd socket activation: `Daemon.Init` builds
an empty listener map and never consumes `SD_LISTEN_FDS`; it always calls
`net.Listen` itself (for both the Unix socket and `--http`). A
`buildtest.socket` unit would hold port 8000 while the daemon's own listen
fails. Socket activation would require wiring `sd_listen_fds` into
`getListener` (see `internals/daemon/daemon.go`).

## Install script (`systemd/install.sh`)

- POSIX sh, shellcheck-clean. Builds `./cmd/buildtest` if needed, installs to
  `~/bin/buildtest` (unit expects `%h/bin/buildtest`), copies the unit to
  `~/.config/systemd/user/`, then `daemon-reload` + `enable --now`.
- `--dest-dir <dir>` (or `DEST_DIR`) stages `bin/` + `.config/systemd/user/`
  under a directory for packaging, without touching systemd.
- `HTTP_ADDR` (default `:8000`) is substituted into the unit's `ExecStart`.
- `BUILDTEST_BIN` installs a prebuilt binary instead of building.
- `systemctl --user` needs a user session bus; over plain ssh or in a
  container it fails, and the script dies with a hint.

## Changelog

- **2026-11-25** - Rewrote `buildtest.service` (added `[Install]`,
  `RestartForceExitStatus`, dropped `NotifyAccess=all` and
  `SuccessExitStatus`); deleted `buildtest.socket` (no socket-activation
  support) and stale `pebble.*` units; added `systemd/install.sh`; documented
  the service in the README.
