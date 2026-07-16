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

package buildteststate

import (
	"fmt"
	"time"

	"github.com/canonical/pebble/internals/overlord/state"
)

const (
	buildTaskKind = "build-test-build"
	runTaskKind   = "build-test-run"

	// TaskSet edges for identifying build and run tasks.
	BuildEdge state.TaskSetEdge = "build"
	RunEdge   state.TaskSetEdge = "run"

	// maxBuildRetries is the maximum number of times a failed snapcraft
	// build will be retried after running "snapcraft clean".
	maxBuildRetries = 1
)

// BuildTestArgs holds the arguments for a build-test request.
type BuildTestArgs struct {
	// SourceTarball is the path to the uploaded source tarball on disk.
	SourceTarball string
	// Timeout is the optional overall timeout for the operation.
	Timeout time.Duration
	// Interactive enables interactive mode, allowing stdin to be forwarded
	// to the build/run processes via websockets.
	Interactive bool
	// Terminal allocates a pseudo-terminal for the build/run processes.
	// Typically set to true when Interactive is true.
	Terminal bool
}

// buildTestSetup is stored in the state cache to specify the args for a build-test.
type buildTestSetup struct {
	SourceTarball string
	Timeout       time.Duration
	RetryCount    int
	Interactive   bool
	Terminal      bool
}

// buildTestSetupKey is used as a cache key for the build-test setup.
type buildTestSetupKey struct {
	taskID string
}

// BuildTest creates tasks that will build the source with snapcraft and run
// spread tests. It returns a TaskSet containing the build and run tasks.
func BuildTest(st *state.State, args *BuildTestArgs) (*state.TaskSet, error) {
	if args.SourceTarball == "" {
		return nil, fmt.Errorf("must specify source tarball path")
	}

	// Create the build task.
	buildTask := st.NewTask(buildTaskKind, "Build source with snapcraft")
	setup := &buildTestSetup{
		SourceTarball: args.SourceTarball,
		Timeout:       args.Timeout,
		Interactive:   args.Interactive,
		Terminal:      args.Terminal,
	}
	st.Cache(buildTestSetupKey{buildTask.ID()}, setup)

	// Create the run task (depends on build task succeeding).
	runTask := st.NewTask(runTaskKind, "Run spread tests")

	// Build task must complete before run task starts.
	runTask.WaitFor(buildTask)

	ts := state.NewTaskSet(buildTask, runTask)
	ts.MarkEdge(buildTask, BuildEdge)
	ts.MarkEdge(runTask, RunEdge)

	return ts, nil
}
