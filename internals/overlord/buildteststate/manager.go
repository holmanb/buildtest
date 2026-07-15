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
	"os"
	"path/filepath"

	"gopkg.in/tomb.v2"

	"github.com/canonical/pebble/internals/logger"
	"github.com/canonical/pebble/internals/overlord/state"
)

// BuildTestManager manages build-test tasks.
type BuildTestManager struct {
	pebbleDir string
}

// NewManager creates a new BuildTestManager.
func NewManager(pebbleDir string, runner *state.TaskRunner) *BuildTestManager {
	manager := &BuildTestManager{
		pebbleDir: pebbleDir,
	}
	runner.AddHandler(buildTaskKind, manager.doBuild, nil)
	runner.AddHandler(runTaskKind, manager.doRun, nil)

	// Clean up temp directory and cached setup after build task completes.
	runner.AddCleanup(buildTaskKind, func(task *state.Task, tomb *tomb.Tomb) error {
		st := task.State()
		st.Lock()
		defer st.Unlock()
		st.Cache(buildTestSetupKey{task.ID()}, nil)
		return nil
	})

	runner.AddCleanup(runTaskKind, func(task *state.Task, tomb *tomb.Tomb) error {
		// Remove the temporary directory for this task's change.
		st := task.State()
		st.Lock()
		change := task.Change()
		st.Unlock()

		tempDir := filepath.Join(manager.pebbleDir, "build-test", change.ID())
		err := os.RemoveAll(tempDir)
		if err != nil {
			logger.Noticef("Cannot remove build-test temp dir %q: %v", tempDir, err)
		}
		return nil
	})

	return manager
}

// Ensure is part of the overlord.StateManager interface.
func (m *BuildTestManager) Ensure() error {
	return nil
}

// tempDir returns the temporary directory path for a given change ID.
func (m *BuildTestManager) tempDir(changeID string) string {
	return filepath.Join(m.pebbleDir, "build-test", changeID)
}
