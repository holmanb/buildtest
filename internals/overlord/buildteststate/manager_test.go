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
	"os"
	"path/filepath"

	. "gopkg.in/check.v1"

	"github.com/canonical/pebble/internals/overlord/buildteststate"
	"github.com/canonical/pebble/internals/overlord/state"
)

type managerSuite struct{}

var _ = Suite(&managerSuite{})

func (s *managerSuite) TestNewManager(c *C) {
	st := state.New(nil)
	runner := state.NewTaskRunner(st)
	pebbleDir := c.MkDir()
	_ = buildteststate.NewManager(pebbleDir, runner)
}

func (s *managerSuite) TestBuildTestValidation(c *C) {
	st := state.New(nil)
	runner := state.NewTaskRunner(st)
	pebbleDir := c.MkDir()
	_ = buildteststate.NewManager(pebbleDir, runner)

	st.Lock()
	defer st.Unlock()

	args := &buildteststate.BuildTestArgs{
		SourceTarball: "",
	}
	ts, err := buildteststate.BuildTest(st, args)
	c.Assert(err, ErrorMatches, "must specify source tarball path")
	c.Assert(ts, IsNil)
}

func (s *managerSuite) TestBuildTestCreatesTasks(c *C) {
	st := state.New(nil)
	runner := state.NewTaskRunner(st)
	pebbleDir := c.MkDir()
	_ = buildteststate.NewManager(pebbleDir, runner)

	st.Lock()
	defer st.Unlock()

	args := &buildteststate.BuildTestArgs{
		SourceTarball: "/some/path.tar.gz",
	}
	ts, err := buildteststate.BuildTest(st, args)
	c.Assert(err, IsNil)
	c.Assert(ts, NotNil)

	// Verify the task set has the expected edges.
	buildTask, err := ts.Edge(buildteststate.ExportedBuildEdge)
	c.Assert(err, IsNil)
	c.Assert(buildTask, NotNil)
	c.Check(buildTask.Kind(), Equals, buildteststate.BuildTaskKind)

	runTask, err := ts.Edge(buildteststate.ExportedRunEdge)
	c.Assert(err, IsNil)
	c.Assert(runTask, NotNil)
	c.Check(runTask.Kind(), Equals, buildteststate.RunTaskKind)

	// Verify run task waits for build task.
	c.Check(runTask.WaitTasks(), HasLen, 1)
	c.Check(runTask.WaitTasks()[0].ID(), Equals, buildTask.ID())

	// Verify setup is cached.
	setupObj := st.Cached(buildteststate.NewBuildTestSetupKey(buildTask.ID()))
	c.Assert(setupObj, NotNil)
}

func (s *managerSuite) TestTempDir(c *C) {
	pebbleDir := "/var/lib/pebble"
	st := state.New(nil)
	runner := state.NewTaskRunner(st)
	mgr := buildteststate.NewManager(pebbleDir, runner)

	expected := filepath.Join(pebbleDir, "build-test", "1")
	c.Check(mgr.TempDir("1"), Equals, expected)
}

func (s *managerSuite) TestEnsure(c *C) {
	st := state.New(nil)
	runner := state.NewTaskRunner(st)
	pebbleDir := c.MkDir()
	mgr := buildteststate.NewManager(pebbleDir, runner)

	err := mgr.Ensure()
	c.Assert(err, IsNil)
}

func (s *managerSuite) TestCleanupBuildTask(c *C) {
	st := state.New(nil)
	runner := state.NewTaskRunner(st)
	pebbleDir := c.MkDir()
	mgr := buildteststate.NewManager(pebbleDir, runner)

	st.Lock()
	args := &buildteststate.BuildTestArgs{
		SourceTarball: "/some/path.tar.gz",
	}
	ts, err := buildteststate.BuildTest(st, args)
	c.Assert(err, IsNil)
	buildTask, err := ts.Edge(buildteststate.ExportedBuildEdge)
	c.Assert(err, IsNil)
	chg := st.NewChange("build-test", "Build source and run tests")
	chg.AddAll(ts)
	st.Unlock()

	// Verify setup is cached before cleanup.
	st.Lock()
	setupObj := st.Cached(buildteststate.NewBuildTestSetupKey(buildTask.ID()))
	c.Assert(setupObj, NotNil)
	st.Unlock()

	// Run the build cleanup handler.
	err = mgr.RunCleanupForTest(buildTask)
	c.Assert(err, IsNil)

	// Verify setup cache is cleared after cleanup.
	st.Lock()
	setupObj = st.Cached(buildteststate.NewBuildTestSetupKey(buildTask.ID()))
	c.Assert(setupObj, IsNil)
	st.Unlock()
}

func (s *managerSuite) TestCleanupRunTask(c *C) {
	st := state.New(nil)
	runner := state.NewTaskRunner(st)
	pebbleDir := c.MkDir()
	mgr := buildteststate.NewManager(pebbleDir, runner)

	st.Lock()
	args := &buildteststate.BuildTestArgs{
		SourceTarball: "/some/path.tar.gz",
	}
	ts, err := buildteststate.BuildTest(st, args)
	c.Assert(err, IsNil)
	runTask, err := ts.Edge(buildteststate.ExportedRunEdge)
	c.Assert(err, IsNil)
	chg := st.NewChange("build-test", "Build source and run tests")
	chg.AddAll(ts)
	changeID := chg.ID()
	st.Unlock()

	// Create the temp directory that cleanup should remove.
	tempDir := filepath.Join(pebbleDir, "build-test", changeID)
	err = os.MkdirAll(tempDir, 0o755)
	c.Assert(err, IsNil)

	// Verify temp dir exists.
	_, err = os.Stat(tempDir)
	c.Assert(err, IsNil)

	// Run the run cleanup handler.
	err = mgr.RunCleanupForTest(runTask)
	c.Assert(err, IsNil)

	// Verify temp dir is removed after cleanup.
	_, err = os.Stat(tempDir)
	c.Assert(os.IsNotExist(err), Equals, true)
}
