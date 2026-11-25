# Hacking on Buildtest

- [Running the daemon](#running-the-daemon)
- [Using the CLI client](#using-the-cli-client)
- [Using Curl to hit the API](#using-curl-to-hit-the-api)
- [Code style](#code-style)
- [Running the tests](#running-the-tests)

Hacking on Buildtest is easy. It's written in Go, so install or [download](https://golang.org/dl/) a copy of the latest version of Go. Buildtest uses [Go modules](https://golang.org/ref/mod) for managing dependencies, so all of the standard Go tooling just works.

To compile and run Buildtest, use the `go run` command on the `cmd/buildtest` directory. The first time you run it, it will download dependencies and build packages, so will take a few seconds (but after that be very fast):

```
$ go run ./cmd/buildtest
Buildtest lets you build source and run tests on the system that is
running the daemon.

Usage: buildtest <command> [<options>...]
...
```

If you want to build and install the executable to your `~/go/bin` directory (which you may want to add to your path), use `go install`:

```
go install ./cmd/buildtest
```

However, during development it's easiest just to use `go run`, as that will automatically recompile if you've made any changes.


## Running the daemon

To run the Buildtest daemon, set the `$BUILDTEST` environment variable and use the `buildtest run` sub-command, something like this:

```
$ mkdir ~/buildtest
$ export BUILDTEST=~/buildtest
$ go run ./cmd/buildtest run --http=:8000
2021-09-15T01:37:23.962Z [buildtest] Started daemon.
...
```


## Using the CLI client

Then use the Buildtest command line client, run one of the other Buildtest sub-commands, such as `buildtest changes` (if the server is running in one terminal, do this in another):

```
$ export BUILDTEST=~/buildtest
$ go run ./cmd/buildtest changes
ID   Status  Spawn                     Ready                     Summary
...
```

To run a build-test job against a remote daemon, set `BUILDTEST_BASEURL`:

```
$ BUILDTEST_BASEURL=http://buildhost:8000 go run ./cmd/buildtest build-test ~/src/myproject --stream
```


## Using Curl to hit the API

For debugging, you can also use [curl](https://curl.se/) in unix socket mode to hit the Buildtest API:

```
$ curl --unix-socket ~/buildtest/.buildtest.socket 'http://localhost/v1/changes' | jq
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100   120  100   120    0     0    117k      0 --:--:-- --:--:-- --:--:--  117k
{
  "type": "sync",
  "status-code": 200,
  "status": "OK",
  "result": [
    ...
  ]
}
```


## Code style

### Commits

Please format your commits following the [conventional commit](https://www.conventionalcommits.org/en/v1.0.0/#summary) style.

Optionally, use the brackets to scope to a particular component where applicable.

See below for some examples of commit headings:

```
feat: checks inherit context from services
test: increase unit test stability
feat(daemon): foo the bar correctly in the baz
test(daemon): ensure the foo bars correctly in the baz
ci(snap): upload the snap artefacts to Github
chore(deps): update go.mod dependencies
```

Recommended prefixes are: `fix:`, `feat:`, `build:`, `chore:`, `ci:`, `docs:`, `style:`, `refactor:`,`perf:` and `test:`

### Imports

Buildtest imports should be arranged in three groups:

- standard library imports
- third-party / non-Buildtest imports
- Buildtest imports (i.e. those prefixed with `github.com/holmanb/buildtest`)

Imports should be sorted alphabetically within each group.

We use the [`gopkg.in/check.v1`](https://pkg.go.dev/gopkg.in/check.v1) package for testing. Inside a test file, import this as follows:

```go
. "gopkg.in/check.v1"
```

so that identifiers from that package will be added to the local namespace.


Here is an example of correctly arranged imports:

```go
import (
	"fmt"
	"net"
	"os"

	"github.com/gorilla/mux"
	. "gopkg.in/check.v1"

	"github.com/holmanb/buildtest/internals/systemd"
	"github.com/holmanb/buildtest/internals/testutil"
)
```

### Log and error messages

**Log messages** (that is, those passed to `logger.Noticef` or `logger.Debugf`) should begin with a capital letter, and use "Cannot X" rather than "Error Xing":

```go
logger.Noticef("Cannot marshal logs to JSON: %v", err)
```

**Error messages** should be lowercase, and again use "cannot ..." instead of "error ...":

```go
fmt.Errorf("cannot create log client: %w", err)
```


## Running the tests

Buildtest has a suite of Go unit tests, which you can run using the regular `go test` command. To test all packages in the Buildtest repository:

```
$ go test ./...
ok      github.com/holmanb/buildtest/client  (cached)
?       github.com/holmanb/buildtest/cmd [no test files]
ok      github.com/holmanb/buildtest/cmd/buildtest  0.095s
...
```

To test a single package, simply pass the package path to `go test`:

```
$ go test ./internals/cli
ok      github.com/holmanb/buildtest/internals/cli  0.115s
```

To run a single suite or a single test, pass the suite or test name to the [gocheck](https://labix.org/gocheck) test runner:

```
$ go test ./internals/cli -v -check.v -check.f PebbleSuite
=== RUN   Test
PASS: cmd_changes_test.go:38: PebbleSuite.TestChanges   0.002s
PASS: format_test.go:52: PebbleSuite.TestCanUnicode 0.000s
...
OK: 20 passed
--- PASS: Test (0.02s)
PASS
ok      github.com/holmanb/buildtest/internals/cli  0.022s
```

Note that during CI we run the tests with `-race` to catch data races:

```
$ go test -race ./...
ok      github.com/holmanb/buildtest/client  (cached)
...
```

Buildtest also has a suite of integration tests for testing things like `buildtest run`. To run them, use the "integration" build constraint:

```
$ go test -count=1 -tags=integration ./tests/
ok  	github.com/holmanb/buildtest/tests	4.774s
```
