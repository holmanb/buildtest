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

package buildteststate_test

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"time"

	. "gopkg.in/check.v1"
	"gopkg.in/tomb.v2"

	"github.com/canonical/pebble/internals/overlord/buildteststate"
	"github.com/canonical/pebble/internals/overlord/state"
)

type handlersSuite struct {
	st  *state.State
	mgr *buildteststate.BuildTestManager
}

var _ = Suite(&handlersSuite{})

func (s *handlersSuite) SetUpTest(c *C) {
	s.st = state.New(nil)
	runner := state.NewTaskRunner(s.st)
	pebbleDir := c.MkDir()
	s.mgr = buildteststate.NewManager(pebbleDir, runner)
}

func (s *handlersSuite) TearDownTest(c *C) {
	buildteststate.FakeRunCommand(nil)
	buildteststate.FakeCleanCommand(nil)
}

// createBuildTestChange is a helper that creates a build-test change with
// the build and run tasks, and returns the build and run tasks.
func (s *handlersSuite) createBuildTestChange(c *C, args *buildteststate.BuildTestArgs) (buildTask, runTask *state.Task) {
	s.st.Lock()
	defer s.st.Unlock()

	ts, err := buildteststate.BuildTest(s.st, args)
	c.Assert(err, IsNil)
	buildTask, err = ts.Edge(buildteststate.ExportedBuildEdge)
	c.Assert(err, IsNil)
	runTask, err = ts.Edge(buildteststate.ExportedRunEdge)
	c.Assert(err, IsNil)

	chg := s.st.NewChange("build-test", "Build source and run tests")
	chg.AddAll(ts)
	return buildTask, runTask
}

func (s *handlersSuite) TestDoBuildNoSetup(c *C) {
	s.st.Lock()
	task := s.st.NewTask(buildteststate.BuildTaskKind, "Build source with snapcraft")
	chg := s.st.NewChange("build-test", "Build source and run tests")
	chg.AddTask(task)
	s.st.Unlock()

	// No setup cached — should return nil (retried after restart).
	err := s.mgr.RunHandlerForTest(task)
	c.Check(err, IsNil)
}

func (s *handlersSuite) TestDoBuildSuccess(c *C) {
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		c.Check(name, Equals, "snapcraft")
		c.Check(dir, Not(Equals), "")
		return 0, "build output", "build stderr", nil
	})
	defer restore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
	})

	err := s.mgr.RunHandlerForTest(buildTask)
	c.Check(err, IsNil)

	// Verify api-data was set.
	s.st.Lock()
	var apiData map[string]any
	err = buildTask.Get("api-data", &apiData)
	c.Assert(err, IsNil)
	c.Check(apiData["stdout"], Equals, "build output")
	c.Check(apiData["stderr"], Equals, "build stderr")
	c.Check(apiData["exit-code"], Equals, float64(0))
	s.st.Unlock()
}

func (s *handlersSuite) TestDoBuildFailure(c *C) {
	callCount := 0
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		callCount++
		return 1, "build output", "build stderr", nil
	})
	defer restore()

	cleanRestore := buildteststate.FakeCleanCommand(func(dir string) (string, string, error) {
		return "clean output", "", nil
	})
	defer cleanRestore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
	})

	err := s.mgr.RunHandlerForTest(buildTask)
	c.Check(err, ErrorMatches, "snapcraft failed with exit code 1.*retried after clean.*")

	// Verify api-data was still set (even on failure) with retry info.
	s.st.Lock()
	var apiData map[string]any
	err = buildTask.Get("api-data", &apiData)
	c.Assert(err, IsNil)
	c.Check(apiData["stdout"], Equals, "build output")
	c.Check(apiData["stderr"], Equals, "build stderr")
	c.Check(apiData["exit-code"], Equals, float64(1))
	c.Check(apiData["retried"], Equals, true)
	c.Check(apiData["original-exit-code"], Equals, float64(1))
	s.st.Unlock()

	// Verify snapcraft was called twice (initial + retry).
	c.Check(callCount, Equals, 2)
}

func (s *handlersSuite) TestDoBuildTimeout(c *C) {
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		return -1, "", "", fmt.Errorf("snapcraft timed out after %v", timeout)
	})
	defer restore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
		Timeout:       5 * time.Minute,
	})

	err := s.mgr.RunHandlerForTest(buildTask)
	c.Check(err, ErrorMatches, "cannot run snapcraft: snapcraft timed out.*")
}

func (s *handlersSuite) TestDoRunSuccess(c *C) {
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		if name == "snapcraft" {
			return 0, "build output", "build stderr", nil
		}
		c.Check(name, Equals, "spread")
		return 0, "test output", "test stderr", nil
	})
	defer restore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, runTask := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
	})

	// Run the build task first.
	err := s.mgr.RunHandlerForTest(buildTask)
	c.Assert(err, IsNil)

	// Mark build task as done so run task can proceed.
	s.st.Lock()
	buildTask.SetStatus(state.DoneStatus)
	s.st.Unlock()

	// Run the run task.
	err = s.mgr.RunHandlerForTest(runTask)
	c.Check(err, IsNil)

	// Verify api-data was set for the run task.
	s.st.Lock()
	var apiData map[string]any
	err = runTask.Get("api-data", &apiData)
	c.Assert(err, IsNil)
	c.Check(apiData["stdout"], Equals, "test output")
	c.Check(apiData["stderr"], Equals, "test stderr")
	c.Check(apiData["exit-code"], Equals, float64(0))
	s.st.Unlock()
}

func (s *handlersSuite) TestDoRunFailure(c *C) {
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		if name == "snapcraft" {
			return 0, "build output", "build stderr", nil
		}
		return 2, "test output", "test stderr", nil
	})
	defer restore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, runTask := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
	})

	// Run the build task first.
	err := s.mgr.RunHandlerForTest(buildTask)
	c.Assert(err, IsNil)

	s.st.Lock()
	buildTask.SetStatus(state.DoneStatus)
	s.st.Unlock()

	// Run the run task — non-zero exit code does NOT return an error.
	err = s.mgr.RunHandlerForTest(runTask)
	c.Check(err, IsNil)

	// Verify api-data was set.
	s.st.Lock()
	var apiData map[string]any
	err = runTask.Get("api-data", &apiData)
	c.Assert(err, IsNil)
	c.Check(apiData["stdout"], Equals, "test output")
	c.Check(apiData["stderr"], Equals, "test stderr")
	c.Check(apiData["exit-code"], Equals, float64(2))
	s.st.Unlock()
}

func (s *handlersSuite) TestDoBuildRetrySuccess(c *C) {
	// First snapcraft call fails, second succeeds after clean.
	callCount := 0
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		callCount++
		if callCount == 1 {
			return 1, "first build output", "first build stderr", nil
		}
		return 0, "second build output", "second build stderr", nil
	})
	defer restore()

	cleanRestore := buildteststate.FakeCleanCommand(func(dir string) (string, string, error) {
		return "clean output", "clean stderr", nil
	})
	defer cleanRestore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
	})

	err := s.mgr.RunHandlerForTest(buildTask)
	c.Check(err, IsNil)

	// Verify api-data includes retry info.
	s.st.Lock()
	var apiData map[string]any
	err = buildTask.Get("api-data", &apiData)
	c.Assert(err, IsNil)
	c.Check(apiData["stdout"], Equals, "second build output")
	c.Check(apiData["stderr"], Equals, "second build stderr")
	c.Check(apiData["exit-code"], Equals, float64(0))
	c.Check(apiData["retried"], Equals, true)
	c.Check(apiData["original-exit-code"], Equals, float64(1))
	c.Check(apiData["original-stderr"], Equals, "first build stderr")
	s.st.Unlock()
}

func (s *handlersSuite) TestDoBuildRetryExhausted(c *C) {
	// Both snapcraft calls fail — max retries reached.
	callCount := 0
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		callCount++
		if callCount == 1 {
			return 1, "first build output", "first build stderr", nil
		}
		return 2, "second build output", "second build stderr", nil
	})
	defer restore()

	cleanRestore := buildteststate.FakeCleanCommand(func(dir string) (string, string, error) {
		return "clean output", "clean stderr", nil
	})
	defer cleanRestore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
	})

	err := s.mgr.RunHandlerForTest(buildTask)
	c.Check(err, ErrorMatches, "snapcraft failed with exit code 2.*")

	// Verify api-data includes retry info even on final failure.
	s.st.Lock()
	var apiData map[string]any
	err = buildTask.Get("api-data", &apiData)
	c.Assert(err, IsNil)
	c.Check(apiData["stdout"], Equals, "second build output")
	c.Check(apiData["stderr"], Equals, "second build stderr")
	c.Check(apiData["exit-code"], Equals, float64(2))
	c.Check(apiData["retried"], Equals, true)
	c.Check(apiData["original-exit-code"], Equals, float64(1))
	s.st.Unlock()
}

func (s *handlersSuite) TestDoBuildCleanCommandFailure(c *C) {
	// Clean command fails, but retry still attempted and succeeds.
	callCount := 0
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		callCount++
		if callCount == 1 {
			return 1, "first build output", "first build stderr", nil
		}
		return 0, "second build output", "second build stderr", nil
	})
	defer restore()

	cleanRestore := buildteststate.FakeCleanCommand(func(dir string) (string, string, error) {
		return "", "clean error", fmt.Errorf("clean failed")
	})
	defer cleanRestore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
	})

	err := s.mgr.RunHandlerForTest(buildTask)
	c.Check(err, IsNil)

	// Verify the retry succeeded despite clean failure.
	s.st.Lock()
	var apiData map[string]any
	err = buildTask.Get("api-data", &apiData)
	c.Assert(err, IsNil)
	c.Check(apiData["retried"], Equals, true)
	c.Check(apiData["exit-code"], Equals, float64(0))
	s.st.Unlock()
}

func (s *handlersSuite) TestDoBuildNoRetryOnRunCommandError(c *C) {
	// If runCommandStreaming itself returns an error (not just non-zero exit),
	// we don't retry — it's an infrastructure error.
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		return -1, "", "", fmt.Errorf("cannot start snapcraft: executable not found")
	})
	defer restore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
	})

	err := s.mgr.RunHandlerForTest(buildTask)
	c.Check(err, ErrorMatches, "cannot run snapcraft:.*")
}

func (s *handlersSuite) TestMaxBuildRetries(c *C) {
	c.Check(buildteststate.MaxBuildRetries, Equals, 1)
}

func (s *handlersSuite) TestInteractiveWebsocketIDs(c *C) {
	// Verify the interactive websocket ID constants.
	c.Check(buildteststate.WsBuildStdioExport, Equals, "build-stdio")
	c.Check(buildteststate.WsBuildControlExport, Equals, "build-control")
	c.Check(buildteststate.WsRunStdioExport, Equals, "run-stdio")
	c.Check(buildteststate.WsRunControlExport, Equals, "run-control")
}

func (s *handlersSuite) TestDoBuildInteractiveUsesStdioWebsockets(c *C) {
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		c.Check(name, Equals, "snapcraft")
		return 0, "build output", "build stderr", nil
	})
	defer restore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	// Create a build-test with interactive mode enabled.
	// Note: We can't actually run the handler in interactive mode
	// without websocket connections, so we just verify the setup
	// is created correctly with the interactive/terminal flags.
	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
		Interactive:   true,
		Terminal:      true,
	})

	// Verify the setup has interactive and terminal flags.
	s.st.Lock()
	setupObj := s.st.Cached(buildteststate.NewBuildTestSetupKey(buildTask.ID()))
	c.Assert(setupObj, NotNil)
	setup, ok := setupObj.(*buildteststate.BuildTestSetupForTest)
	c.Assert(ok, Equals, true)
	c.Check(setup.Interactive, Equals, true)
	c.Check(setup.Terminal, Equals, true)
	s.st.Unlock()
}

func (s *handlersSuite) TestDoRunInteractiveUsesStdioWebsockets(c *C) {
	restore := buildteststate.FakeRunCommand(func(name, dir string, timeout time.Duration, tomb *tomb.Tomb) (int, string, string, error) {
		if name == "snapcraft" {
			return 0, "build output", "build stderr", nil
		}
		c.Check(name, Equals, "spread")
		return 0, "test output", "test stderr", nil
	})
	defer restore()

	pebbleDir := c.MkDir()
	tarballPath := createMinimalTarball(c, pebbleDir)

	// Create a build-test with interactive mode enabled but no terminal.
	buildTask, _ := s.createBuildTestChange(c, &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
		Interactive:   true,
		Terminal:      false,
	})

	// Verify the setup has interactive=true and terminal=false.
	s.st.Lock()
	setupObj := s.st.Cached(buildteststate.NewBuildTestSetupKey(buildTask.ID()))
	c.Assert(setupObj, NotNil)
	setup, ok := setupObj.(*buildteststate.BuildTestSetupForTest)
	c.Assert(ok, Equals, true)
	c.Check(setup.Interactive, Equals, true)
	c.Check(setup.Terminal, Equals, false)
	s.st.Unlock()
}

func (s *handlersSuite) TestLimitWriterWithinLimit(c *C) {
	w := buildteststate.NewLimitWriter()
	data := []byte("hello")
	n, err := w.Write(data)
	c.Check(n, Equals, len(data))
	c.Check(err, IsNil)
	c.Check(w.String(), Equals, "hello")
}

func (s *handlersSuite) TestLimitWriterExceedsLimit(c *C) {
	w := buildteststate.NewLimitWriter()
	// Write exactly the max size.
	bigData := make([]byte, buildteststate.MaxOutputSize)
	n, err := w.Write(bigData)
	c.Check(n, Equals, buildteststate.MaxOutputSize)
	c.Check(err, IsNil)

	// Write more data — should be silently truncated.
	extraData := []byte("extra")
	n, err = w.Write(extraData)
	c.Check(n, Equals, len(extraData))
	c.Check(err, IsNil)
	c.Check(len(w.Bytes()), Equals, buildteststate.MaxOutputSize)
}

// createMinimalTarball creates a minimal .tar.gz file for testing.
func createMinimalTarball(c *C, dir string) string {
	tarballPath := dir + "/source.tar.gz"
	err := createTarball(tarballPath, map[string]string{"hello.txt": "hello world"})
	c.Assert(err, IsNil)
	return tarballPath
}

// createTarball creates a gzipped tar archive containing the given files.
func createTarball(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gzw := gzip.NewWriter(f)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return err
		}
	}
	return nil
}
