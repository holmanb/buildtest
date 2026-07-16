// Copyright (c) 2026 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cli_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	. "gopkg.in/check.v1"

	"github.com/canonical/pebble/internals/cli"
)

func (s *PebbleSuite) TestBuildTestExtraArgs(c *C) {
	rest, err := cli.ParserForTest().ParseArgs([]string{"build-test", "/some/path", "extra"})
	c.Assert(err, Equals, cli.ErrExtraArgs)
	c.Check(rest, HasLen, 1)
}

func (s *PebbleSuite) TestBuildTestMissingPath(c *C) {
	_, err := cli.ParserForTest().ParseArgs([]string{"build-test"})
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, `.*required argument.*`)
}

func (s *PebbleSuite) TestBuildTestDebugFlag(c *C) {
	// Test that the --debug flag is recognized by the parser.
	// We verify this by parsing with --debug and a valid source path,
	// but with extra args to avoid actual execution.
	_, err := cli.ParserForTest().ParseArgs([]string{"build-test", "--debug", "/some/path", "extra"})
	// Should fail with ErrExtraArgs, which means --debug was accepted.
	c.Assert(err, Equals, cli.ErrExtraArgs)
}

func (s *PebbleSuite) TestBuildTestSuccess(c *C) {
	srcDir := c.MkDir()
	err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0o644)
	c.Assert(err, IsNil)

	requestCount := 0
	s.RedirectClientToTestServer(func(w http.ResponseWriter, r *http.Request) {
		switch requestCount {
		case 0:
			// First request: POST /v1/build-test
			c.Check(r.Method, Equals, "POST")
			c.Check(r.URL.Path, Equals, "/v1/build-test")
			c.Check(r.Header.Get("Content-Type"), Matches, "multipart/form-data.*")
			fmt.Fprintf(w, `{"type": "async", "status-code": 202, "change": "42"}`)
		case 1:
			// Second request: GET /v1/changes/42/wait
			c.Check(r.Method, Equals, "GET")
			c.Check(r.URL.Path, Equals, "/v1/changes/42/wait")
			fmt.Fprintf(w, `{"type": "sync", "result": {
				"id": "42",
				"kind": "build-test",
				"summary": "Build source and run tests",
				"status": "Done",
				"ready": true,
				"spawn-time": "2026-01-01T00:00:00Z",
				"ready-time": "2026-01-01T00:01:00Z",
				"tasks": [
					{
						"id": "1",
						"kind": "build-test-build",
						"summary": "Build source",
						"status": "Done",
						"progress": {"done": 1, "total": 1},
						"spawn-time": "2026-01-01T00:00:00Z",
						"ready-time": "2026-01-01T00:00:30Z",
						"data": {"stdout": "build output", "stderr": "", "exit-code": 0}
					},
					{
						"id": "2",
						"kind": "build-test-run",
						"summary": "Run tests",
						"status": "Done",
						"progress": {"done": 1, "total": 1},
						"spawn-time": "2026-01-01T00:00:30Z",
						"ready-time": "2026-01-01T00:01:00Z",
						"data": {"stdout": "test output", "stderr": "test stderr", "exit-code": 0}
					}
				]
			}}`)
		default:
			c.Fatalf("unexpected request %d: %s %s", requestCount, r.Method, r.URL.Path)
		}
		requestCount++
	})

	restore := fakeArgs("pebble", "build-test", srcDir)
	defer restore()

	exitCode := cli.PebbleMain()
	c.Check(exitCode, Equals, 0)

	output := s.Stdout()
	c.Check(output, Matches, `(?s)BUILD.*Exit code: 0.*build output.*RUN.*Exit code: 0.*test output.*test stderr.*`)
}

func (s *PebbleSuite) TestBuildTestBuildFailure(c *C) {
	srcDir := c.MkDir()
	err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0o644)
	c.Assert(err, IsNil)

	requestCount := 0
	s.RedirectClientToTestServer(func(w http.ResponseWriter, r *http.Request) {
		switch requestCount {
		case 0:
			fmt.Fprintf(w, `{"type": "async", "status-code": 202, "change": "42"}`)
		case 1:
			fmt.Fprintf(w, `{"type": "sync", "result": {
				"id": "42",
				"kind": "build-test",
				"summary": "Build source and run tests",
				"status": "Done",
				"ready": true,
				"spawn-time": "2026-01-01T00:00:00Z",
				"ready-time": "2026-01-01T00:00:30Z",
				"tasks": [
					{
						"id": "1",
						"kind": "build-test-build",
						"summary": "Build source",
						"status": "Error",
						"progress": {"done": 1, "total": 1},
						"spawn-time": "2026-01-01T00:00:00Z",
						"ready-time": "2026-01-01T00:00:30Z",
						"data": {"stdout": "building...", "stderr": "error: build failed", "exit-code": 1}
					}
				]
			}}`)
		default:
			c.Fatalf("unexpected request %d", requestCount)
		}
		requestCount++
	})

	restore := fakeArgs("pebble", "build-test", srcDir)
	defer restore()

	exitCode := cli.PebbleMain()
	c.Check(exitCode, Equals, 1)

	output := s.Stdout()
	c.Check(output, Matches, `(?s)BUILD.*Exit code: 1.*build failed.*`)
	c.Check(output, Not(Matches), `(?s)RUN.*`)
}

func (s *PebbleSuite) TestBuildTestNonexistentPath(c *C) {
	restore := fakeArgs("pebble", "build-test", "/nonexistent/path")
	defer restore()

	exitCode := cli.PebbleMain()
	c.Check(exitCode, Equals, 1)
	c.Check(s.Stderr(), Matches, `(?s).*cannot create source tarball.*`)
}
