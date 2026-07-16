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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/tomb.v2"

	"github.com/canonical/pebble/internals/overlord/state"
)

// Export for testing.
var (
	BuildTaskKind = buildTaskKind
	RunTaskKind   = runTaskKind
)

// Export for testing.
const (
	ExportedBuildEdge    = BuildEdge
	ExportedRunEdge      = RunEdge
	MaxOutputSize        = maxOutputSize
	MaxBuildRetries      = maxBuildRetries
	WsBuildStdoutExport  = WsBuildStdout
	WsBuildStderrExport  = WsBuildStderr
	WsBuildStdioExport   = WsBuildStdio
	WsBuildControlExport = WsBuildControl
	WsRunStdoutExport    = WsRunStdout
	WsRunStderrExport    = WsRunStderr
	WsRunStdioExport     = WsRunStdio
	WsRunControlExport   = WsRunControl
	ConnectTimeoutExport = connectTimeout
)

// NewBuildTestSetupKey creates a buildTestSetupKey for testing.
func NewBuildTestSetupKey(taskID string) any {
	return buildTestSetupKey{taskID: taskID}
}

// TempDir exports the tempDir method for testing.
func (m *BuildTestManager) TempDir(changeID string) string {
	return m.tempDir(changeID)
}

// FakeRunCommand sets a mock for the runCommand function and returns a
// restore function to reset it. This allows tests to mock command
// execution without running real snapcraft/spread binaries.
func FakeRunCommand(f func(name string, dir string, timeout time.Duration, tomb *tomb.Tomb) (exitCode int, stdout string, stderr string, err error)) (restore func()) {
	old := fakeRunCommand
	fakeRunCommand = f
	return func() { fakeRunCommand = old }
}

// FakeCleanCommand sets a mock for the clean command function and returns a
// restore function to reset it.
func FakeCleanCommand(f func(dir string) (stdout string, stderr string, err error)) (restore func()) {
	old := fakeCleanCommand
	fakeCleanCommand = f
	return func() { fakeCleanCommand = old }
}

// ExtractTarball exports the extractTarball function for testing.
var ExtractTarball = extractTarball

// NewLimitWriter creates a new limitWriter for testing.
func NewLimitWriter() *limitWriter {
	return &limitWriter{}
}

// Bytes returns the buffer contents as a byte slice.
func (w *limitWriter) Bytes() []byte {
	return w.buf
}

// RunHandlerForTest runs the appropriate handler for the given task.
// This is for testing only — it looks up the handler registered for the
// task's kind and invokes it with a fresh tomb.
func (m *BuildTestManager) RunHandlerForTest(task *state.Task) error {
	t := &tomb.Tomb{}
	switch task.Kind() {
	case buildTaskKind:
		return m.doBuild(task, t)
	case runTaskKind:
		return m.doRun(task, t)
	default:
		return fmt.Errorf("unknown task kind: %s", task.Kind())
	}
}

// RunCleanupForTest runs the cleanup handler for the given task.
func (m *BuildTestManager) RunCleanupForTest(task *state.Task) error {
	switch task.Kind() {
	case buildTaskKind:
		// Build cleanup: clear cache entry.
		st := task.State()
		st.Lock()
		defer st.Unlock()
		st.Cache(buildTestSetupKey{task.ID()}, nil)
		return nil
	case runTaskKind:
		// Run cleanup: remove temp directory.
		st := task.State()
		st.Lock()
		change := task.Change()
		st.Unlock()
		tempDir := filepath.Join(m.pebbleDir, "build-test", change.ID())
		err := os.RemoveAll(tempDir)
		if err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown task kind: %s", task.Kind())
	}
}

// RegisterExecutionForTest creates and registers a buildTestExecution for
// testing, returning the execution object.
func (m *BuildTestManager) RegisterExecutionForTest(taskID, taskKind string, wsIDs []string, interactive, terminal bool) *buildTestExecution {
	return m.registerExecution(taskID, taskKind, wsIDs, interactive, terminal)
}

// UnregisterExecutionForTest removes the execution for the given task ID.
func (m *BuildTestManager) UnregisterExecutionForTest(taskID string) {
	m.unregisterExecution(taskID)
}

// GetExecutionForTest returns the execution for the given task ID, or nil.
func (m *BuildTestManager) GetExecutionForTest(taskID string) *buildTestExecution {
	m.executionsCond.L.Lock()
	defer m.executionsCond.L.Unlock()
	return m.executions[taskID]
}

// WaitIOConnectedForTest waits for all I/O websockets to connect.
func (e *buildTestExecution) WaitIOConnectedForTest(ctx context.Context, taskID string) error {
	return e.waitIOConnected(ctx, taskID)
}

// GetWebsocketForTest returns the websocket connection for the given ID.
func (e *buildTestExecution) GetWebsocketForTest(key string) *websocket.Conn {
	return e.getWebsocket(key)
}

// IOConnectedForTest returns the ioConnected channel for testing.
func (e *buildTestExecution) IOConnectedForTest() chan struct{} {
	return e.ioConnected
}

// BuildTestSetupForTest exports the buildTestSetup type for testing.
type BuildTestSetupForTest = buildTestSetup
