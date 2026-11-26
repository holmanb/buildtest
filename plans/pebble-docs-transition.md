# Transition plan: Pebble → Buildtest in the docs

Review date: 2026-11-26. Scope: every `Pebble`/`pebble` mention in docs
(`*.md`) and doc-adjacent Go comments, with actions for each.

## Rules

1. **Keep attribution.** Buildtest is a GPLv3 fork of Pebble; the README's
   "fork of Pebble" statements and Canonical copyright lines stay forever.
2. **Fix docs that lie first.** Docs naming flags, methods, or paths that do
   not exist are bugs, not branding leftovers.
3. **Never break doc↔code symmetry.** Where docs quote real identifiers
   (`PebbleSuite`, `o.pebbleDir`, `pebbleDaemon`), rename code and doc in the
   same commit — or leave both. Never update only one side.
4. **Don't rewrite history.** Changelog entries mentioning Pebble record what
   happened; they stay.

## Inventory

### A. Keep — attribution and history (no action)

| Location | Text | Why |
| -------- | ---- | --- |
| `README.md:10` | "stripped-down fork of Canonical's [Pebble]" | Honest fork description |
| `README.md:101` | "based on [Pebble] … Copyright (c) Canonical Ltd." | GPLv3 attribution |
| `README.md:105` | `[pebble]:` link definition | Supports the above |
| `systemd/.kb/systemd-user-service.md:54` | changelog: "deleted … stale `pebble.*` units" | Historical record |
| Go copyright headers | `Copyright (c) … Canonical Ltd` | License attribution |

### B. Fix now — factual errors (docs name things that don't exist)

| Location | Problem | Fix |
| -------- | ------- | --- |
| `tests/README.md:13,16` | Documents flag `-pebblebin`; actual flag is `-buildtestbin` (`tests/main_test.go:34`) | Rename flag and example path in doc |
| `client/doc.go:15-19` | Says "Pebble's HTTP-over-unix-socket API"; links `[Client.Start]` and `[Client.ListFiles]` — neither method exists | Describe the real API (Unix socket or TCP via `BaseURL`); link real methods, e.g. `[Client.BuildTest]`, `[Client.Changes]`, `[Client.WaitChange]` |
| `internals/daemon/daemon.go:119` | "Defaults to /var/lib/pebble/default"; actual default is `/var/lib/buildtest/default` (`cmd/personality.go`) | Correct the path |

### C. Fix now — prose rebrand (no code coupling)

| Location | Fix |
| -------- | --- |
| `STYLE.md:1` | `# Pebble Go style guide` → `# Buildtest Go style guide` |
| `STYLE.md:3` | "for the Pebble project" → "for the Buildtest project" (see decision D1 on the Charm Tech sentence) |
| `tests/README.md:1` | `# Pebble Integration Tests` → `# Buildtest integration tests` |
| `tests/README.md:3` | "things like pebble run" → "things like buildtest run" |
| `tests/README.md:11` | "will build Pebble first" → "will build Buildtest first" |

### D. Fix with code — docs quoting live identifiers

These doc passages are accurate *only because the code still uses Pebble
names*. Rename in matched pairs, one identifier per commit:

| Identifier (code) | Code location | Paired doc | Suggested name |
| ----------------- | ------------ | ---------- | -------------- |
| `PebbleSuite`, `BasePebbleSuite` | `internals/cli/cli_test.go:26,129` | `HACKING.md:175-178` | `BuildtestSuite`, `BaseBuildtestSuite` |
| `Options.PebbleDir` (+ `GlobalOptions.PebbleDir`, `ClientConfig.PebbleDir`) | `internals/overlord/overlord.go:73`, `internals/cli/cli.go:55,151,266` | `STYLE.md:408-416` | `BuildTestDir` (or `Dir`, matching `daemon.Options.Dir`) |
| `pebbleDaemon` helper | `tests/main_test.go:77` | `STYLE.md:555-560` | `buildtestDaemon` |
| `pebbleDir` locals | `tests/*.go`, `STYLE.md` examples | `STYLE.md:559-560` | `buildtestDir` |
| `"pebble.socket"` test path | `client/client_test.go:76` | — | `"buildtest.socket"` |
| `"pebble-source-*.tar.gz"` temp prefix | `client/buildtest.go:827` | — | `"buildtest-source-*.tar.gz"` |
| `"pebbletron"` / `"pebblepebble…"` test payloads | `internals/servicelog/ringbuffer*_test.go` | — | any string; cosmetic |

### E. Sweep — Pebble prose in Go doc comments

Update wording only; no identifier coupling. Do as one pass or ride along
with phase 3 commits:

- `client/client.go:148,207,211` — "the Pebble daemon" → "the Buildtest daemon"
- `internals/cli/cli.go` — 11 prose mentions ("full Pebble help manual", "Pebble man page", "regular Pebble commands", "Pebble's main", …)
- `internals/daemon/daemon.go:124,572,634,771,776` — "pebble directory", restart log messages ("Pebble was restarted…")
- `internals/overlord/overlord.go:44,47` — "in the context of Pebble" (snapd comparison; keep the comparison, fix the name)
- `internals/overlord/state/change.go:310` — "pebble change <id>" → `buildtest tasks <id>` (there is no `change` command; `tasks <id>` is the real one)
- `internals/overlord/state/notices.go:193` — "pebble notify" (command no longer exists)
- `internals/reaper/reaper.go:89` — "started by a Pebble service"
- `internals/servicelog/lastlines.go:23` — "Pebble log prefix"
- `internals/testutil/base.go:21` — "many of the pebble tests"

### F. Decision points

1. **Charm Tech sentence (`STYLE.md:3`).** "…style we're converging on for
   other Go projects maintained by the Charm Tech team." Buildtest lives at
   `github.com/holmanb`, not Charm. Keep (inherited style, still true for the
   author's other projects) or drop. Recommend: drop or reword to "other
   projects maintained by the author".
2. **Layer-label example (`STYLE.md:393-396`).** Uses the reserved
   `pebble-` label prefix from the plan/layers feature. README declares
   plans/layers dropped, and `internals/plan` is imported only by its own
   tests (dead code). Recommend: delete `internals/plan` and replace the
   STYLE.md example with a buildtest-relevant one. If `internals/plan`
   stays, the example matches `plan.go:848` and may remain.
3. **`STYLE.md:401` example** — "Cannot build pebble binary" → "Cannot build
   buildtest binary" (trivial, fold into phase 2).
4. **`STYLE.md:510` example** — `pebble-service-name` as a "meaningful
   variable name" → any buildtest-flavored name (fold into phase 2).
5. **Notices comment (`notices.go:193`).** If notices code is also dead
   (README says notices were dropped from the surface), deleting it
   subsumes the comment fix.

## Phases

1. **Fix docs that lie (B).** Two small commits:
   - `docs: fix incorrect -pebblebin flag in tests/README.md`
   - `docs: fix client package doc and daemon dir default`
2. **Prose rebrand (C + F3, F4).** One commit:
   `docs: rebrand remaining Pebble references in STYLE.md and tests/README.md`.
3. **Identifier renames (D).** One commit per rename, doc updated in the same
   commit (gopls rename or `gofmt -r`):
   - `refactor: rename PebbleSuite to BuildtestSuite` (+ `HACKING.md`)
   - `refactor: rename PebbleDir to BuildTestDir` (+ `STYLE.md` examples)
   - `refactor: rename pebbleDaemon to buildtestDaemon` (+ `STYLE.md` example)
   - `chore: rename pebble temp-file prefix and test fixtures`
4. **Comment sweep (E).** One commit: `docs: replace Pebble mentions in Go doc comments`.
   Can ride along with phase 3 commits where files overlap.
5. **Decisions (F1, F2, F5), then act.** Delete `internals/plan` (and notices
   code if dead), replace the STYLE.md layer example, resolve the Charm Tech
   sentence.

## Verification

After each phase:

- `grep -ri pebble --include='*.md' .` → only category A remains
  (README attribution + `.kb` changelog).
- `grep -ri pebble --include='*.go' .` → only intentional remains (or zero).
- `go build ./... && go test ./...`
- `go test -count=1 -tags=integration ./tests/`

## Non-goals

- `COPYING` and Canonical copyright headers — never touched.
- README fork attribution.
- Historical `.kb` changelog entries.
