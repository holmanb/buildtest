# Buildtest

_Remotely build and test, over HTTP._

**Buildtest** is a minimal daemon for remote build-and-test jobs. Upload a
source tree, and the daemon builds it with [snapcraft] and runs your [spread]
tests, streaming output back to the client in real time. When a build fails,
attach an interactive terminal and debug it in place.

Buildtest is a stripped-down fork of Canonical's [Pebble] service manager. It
keeps the daemon core — the HTTP API, changes, and tasks — and drops the
service-management surface: no services, no plans or layers, no identities,
notices, checks, or log forwarding. Just `run`, `changes`, `tasks`, and
`build-test`.

## Commands

| Command | Purpose |
|---|---|
| `run` | Start the daemon and its HTTP API |
| `build-test` | Upload a source directory, build it, and run tests |
| `changes` | List changes (each build-test run is a change) |
| `tasks` | Show the tasks (build, run) within a change |

`build-test` options:

- `--stream` — stream build and test output live
- `--debug` — interactive mode: allocate a PTY on the server, forward stdin
  and signals (e.g. Ctrl-C), and handle terminal resizing, so you can debug a
  failing build as it happens
- `--timeout` — overall timeout for the build and test run

## Quick start

On the build machine, start the server:

```bash
$ PEBBLE=~/.local/share/pebble/default ./pebble run --http=:8000 -v --create-dirs
```

Then, from a machine with the source tree, run the client:

```bash
$ PEBBLE_BASEURL=http://arc.local:8000 $HOME/git/buildtest/pebble build-test $HOME/git/tako --stream
```

The client tars up the source directory and uploads it; the daemon then runs:

1. **Build** — `snapcraft` in the uploaded tree. If the build fails, it is
   retried once after `snapcraft clean`.
2. **Run** — spread tests against the built snap. Skipped if the build failed.

The client reports the exit code of each step.

## Configuration

| Variable | Used by | Meaning |
|---|---|---|
| `PEBBLE` | daemon | Data directory (state is kept here) |
| `PEBBLE_HTTP` | daemon | Address for the HTTP API, e.g. `:8000` (same as `--http`) |
| `PEBBLE_BASEURL` | client | Base URL of the remote daemon |

## Hacking and development

See [HACKING.md](HACKING.md) for how to run and hack on the codebase. In
short, use `go run ./cmd/pebble`.

## Contributing

We welcome quality external contributions. We have good unit tests for much
of the code, and a thorough code review process. For anything beyond a trivial
fix, please open an issue to discuss it before submitting a pull request.

## License

Buildtest is licensed under the [GPLv3](COPYING). It is based on
[Pebble](https://github.com/canonical/pebble), Copyright (c) Canonical Ltd.

[snapcraft]: https://snapcraft.io
[spread]: https://github.com/snapcore/spread
[Pebble]: https://github.com/canonical/pebble
