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

package client_test

import (
	"os"
	"path/filepath"
	"time"

	. "gopkg.in/check.v1"

	"github.com/canonical/pebble/client"
)

func (cs *clientSuite) TestBuildTestRequestFormat(c *C) {
	// Create a source directory with a file.
	srcDir := c.MkDir()
	err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0o644)
	c.Assert(err, IsNil)

	// First response: async POST returns change ID.
	// Second response: WaitChange returns completed change with task data.
	cs.rsps = []string{
		`{"type": "async", "status-code": 202, "change": "42"}`,
		`{"type": "sync", "result": {
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
					"data": {"api-data": {"stdout": "build output", "stderr": "", "exit-code": 0}}
				},
				{
					"id": "2",
					"kind": "build-test-run",
					"summary": "Run tests",
					"status": "Done",
					"progress": {"done": 1, "total": 1},
					"spawn-time": "2026-01-01T00:00:30Z",
					"ready-time": "2026-01-01T00:01:00Z",
					"data": {"api-data": {"stdout": "test output", "stderr": "test stderr", "exit-code": 0}}
				}
			]
		}}`,
	}

	result, err := cs.cli.BuildTest(&client.BuildTestOptions{
		SourcePath: srcDir,
	})
	c.Assert(err, IsNil)

	// Verify the first request was a POST to /v1/build-test.
	c.Check(cs.reqs[0].Method, Equals, "POST")
	c.Check(cs.reqs[0].URL.Path, Equals, "/v1/build-test")

	// Verify the Content-Type header is multipart/form-data.
	contentType := cs.reqs[0].Header.Get("Content-Type")
	c.Check(contentType, Matches, "multipart/form-data.*")

	// Verify the second request was a GET to /v1/changes/42/wait.
	c.Check(cs.reqs[1].Method, Equals, "GET")
	c.Check(cs.reqs[1].URL.Path, Equals, "/v1/changes/42/wait")

	// Verify the result.
	c.Check(result.Build.Stdout, Equals, "build output")
	c.Check(result.Build.Stderr, Equals, "")
	c.Check(result.Build.ExitCode, Equals, 0)
	c.Check(result.Run, NotNil)
	c.Check(result.Run.Stdout, Equals, "test output")
	c.Check(result.Run.Stderr, Equals, "test stderr")
	c.Check(result.Run.ExitCode, Equals, 0)
}

func (cs *clientSuite) TestBuildTestWithTimeout(c *C) {
	srcDir := c.MkDir()
	err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0o644)
	c.Assert(err, IsNil)

	cs.rsps = []string{
		`{"type": "async", "status-code": 202, "change": "42"}`,
		`{"type": "sync", "result": {
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
					"data": {"api-data": {"stdout": "", "stderr": "", "exit-code": 0}}
				},
				{
					"id": "2",
					"kind": "build-test-run",
					"summary": "Run tests",
					"status": "Done",
					"progress": {"done": 1, "total": 1},
					"spawn-time": "2026-01-01T00:00:30Z",
					"ready-time": "2026-01-01T00:01:00Z",
					"data": {"api-data": {"stdout": "", "stderr": "", "exit-code": 0}}
				}
			]
		}}`,
	}

	result, err := cs.cli.BuildTest(&client.BuildTestOptions{
		SourcePath: srcDir,
		Timeout:    5 * time.Minute,
	})
	c.Assert(err, IsNil)
	c.Check(result, NotNil)

	// Verify the request was made with multipart content type.
	contentType := cs.reqs[0].Header.Get("Content-Type")
	c.Check(contentType, Matches, "multipart/form-data.*")
	c.Check(cs.reqs[0].Method, Equals, "POST")
}

func (cs *clientSuite) TestBuildTestBuildFailure(c *C) {
	srcDir := c.MkDir()
	err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0o644)
	c.Assert(err, IsNil)

	cs.rsps = []string{
		`{"type": "async", "status-code": 202, "change": "42"}`,
		`{"type": "sync", "result": {
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
					"data": {"api-data": {"stdout": "building...", "stderr": "error: build failed", "exit-code": 1}}
				}
			]
		}}`,
	}

	result, err := cs.cli.BuildTest(&client.BuildTestOptions{
		SourcePath: srcDir,
	})
	c.Assert(err, IsNil)

	// Build failed but the change itself completed (no change.Err).
	c.Check(result.Build.ExitCode, Equals, 1)
	c.Check(result.Build.Stderr, Equals, "error: build failed")
	c.Check(result.Run, IsNil) // No run task since build failed
}

func (cs *clientSuite) TestBuildTestChangeError(c *C) {
	srcDir := c.MkDir()
	err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0o644)
	c.Assert(err, IsNil)

	cs.rsps = []string{
		`{"type": "async", "status-code": 202, "change": "42"}`,
		`{"type": "sync", "result": {
			"id": "42",
			"kind": "build-test",
			"summary": "Build source and run tests",
			"status": "Error",
			"ready": true,
			"err": "build-test change failed: something went wrong",
			"spawn-time": "2026-01-01T00:00:00Z",
			"ready-time": "2026-01-01T00:00:30Z",
			"tasks": []
		}}`,
	}

	_, err = cs.cli.BuildTest(&client.BuildTestOptions{
		SourcePath: srcDir,
	})
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, ".*something went wrong.*")
}

func (cs *clientSuite) TestBuildTestMissingSourcePath(c *C) {
	_, err := cs.cli.BuildTest(&client.BuildTestOptions{
		SourcePath: "/nonexistent/path/that/does/not/exist",
	})
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, "cannot create source tarball.*")
}

func (cs *clientSuite) TestBuildTestAsyncError(c *C) {
	srcDir := c.MkDir()
	err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0o644)
	c.Assert(err, IsNil)

	cs.status = 500
	cs.rsp = `{"type": "error", "status-code": 500, "result": {"message": "internal error"}}`

	_, err = cs.cli.BuildTest(&client.BuildTestOptions{
		SourcePath: srcDir,
	})
	c.Assert(err, NotNil)
}

func (cs *clientSuite) TestBuildTestMetadataEncoding(c *C) {
	// Test that the metadata part is properly encoded with timeout.
	srcDir := c.MkDir()
	err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0o644)
	c.Assert(err, IsNil)

	cs.rsps = []string{
		`{"type": "async", "status-code": 202, "change": "42"}`,
		`{"type": "sync", "result": {
			"id": "42",
			"kind": "build-test",
			"summary": "Build source and run tests",
			"status": "Done",
			"ready": true,
			"spawn-time": "2026-01-01T00:00:00Z",
			"ready-time": "2026-01-01T00:01:00Z",
			"tasks": []
		}}`,
	}

	_, err = cs.cli.BuildTest(&client.BuildTestOptions{
		SourcePath: srcDir,
		Timeout:    10 * time.Minute,
	})
	c.Assert(err, IsNil)

	// Verify the request was made with multipart content type.
	contentType := cs.reqs[0].Header.Get("Content-Type")
	c.Check(contentType, Matches, "multipart/form-data.*")
}
