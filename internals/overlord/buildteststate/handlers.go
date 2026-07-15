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
	"os/exec"
	"time"

	"gopkg.in/tomb.v2"

	"github.com/canonical/pebble/internals/logger"
	"github.com/canonical/pebble/internals/overlord/state"
	"github.com/canonical/pebble/internals/reaper"
)

// doBuild handles the "build-test-build" task kind.
func (m *BuildTestManager) doBuild(task *state.Task, tomb *tomb.Tomb) error {
	st := task.State()
	st.Lock()
	setupObj := st.Cached(buildTestSetupKey{task.ID()})
	change := task.Change()
	changeID := change.ID()
	st.Unlock()

	setup, ok := setupObj.(*buildTestSetup)
	if !ok || setup == nil {
		logger.Debugf("Cannot get build-test setup object for task %q (retried after restart?)", task.ID())
		return nil
	}

	// Create the temporary directory for this build.
	tempDir := m.tempDir(changeID)
	err := os.MkdirAll(tempDir, 0o755)
	if err != nil {
		return fmt.Errorf("cannot create temp directory %q: %w", tempDir, err)
	}

	// Extract the source tarball into the temp directory.
	err = extractTarball(setup.SourceTarball, tempDir)
	if err != nil {
		return fmt.Errorf("cannot extract source tarball: %w", err)
	}

	// Remove the uploaded tarball after extraction.
	err = os.Remove(setup.SourceTarball)
	if err != nil {
		logger.Noticef("Cannot remove uploaded tarball %q: %v", setup.SourceTarball, err)
	}

	// Run snapcraft in the temp directory.
	var timeout time.Duration
	if setup.Timeout > 0 {
		timeout = setup.Timeout
	}
	exitCode, stdout, stderr, err := runCommand("snapcraft", tempDir, timeout, tomb)
	if err != nil {
		return fmt.Errorf("cannot run snapcraft: %w", err)
	}

	// Store the results in the task's api-data.
	st.Lock()
	task.Set("api-data", map[string]any{
		"stdout":    stdout,
		"stderr":    stderr,
		"exit-code": exitCode,
	})
	st.Unlock()

	// If the build failed, return an error so the change fails and the
	// dependent run task does not execute.
	if exitCode != 0 {
		return fmt.Errorf("snapcraft failed with exit code %d", exitCode)
	}

	return nil
}

// doRun handles the "build-test-run" task kind.
func (m *BuildTestManager) doRun(task *state.Task, tomb *tomb.Tomb) error {
	st := task.State()
	st.Lock()
	change := task.Change()
	changeID := change.ID()
	st.Unlock()

	tempDir := m.tempDir(changeID)

	// Determine timeout from the build task's setup.
	var timeout time.Duration
	st.Lock()
	for _, t := range change.Tasks() {
		if t.Kind() == buildTaskKind {
			setupObj := st.Cached(buildTestSetupKey{t.ID()})
			if setup, ok := setupObj.(*buildTestSetup); ok && setup != nil {
				timeout = setup.Timeout
			}
			break
		}
	}
	st.Unlock()

	// Run spread in the temp directory.
	exitCode, stdout, stderr, err := runCommand("spread", tempDir, timeout, tomb)
	if err != nil {
		return fmt.Errorf("cannot run spread: %w", err)
	}

	// Store the results in the task's api-data.
	st.Lock()
	task.Set("api-data", map[string]any{
		"stdout":    stdout,
		"stderr":    stderr,
		"exit-code": exitCode,
	})
	st.Unlock()

	return nil
}

// fakeRunCommand is used for testing to mock out command execution.
var fakeRunCommand func(name string, dir string, timeout time.Duration, tomb *tomb.Tomb) (exitCode int, stdout string, stderr string, err error)

// runCommand runs a command in the given directory, capturing stdout and stderr.
// It returns the exit code, stdout, stderr, and any error starting the command.
func runCommand(name string, dir string, timeout time.Duration, tomb *tomb.Tomb) (exitCode int, stdout string, stderr string, err error) {
	if fakeRunCommand != nil {
		return fakeRunCommand(name, dir, timeout, tomb)
	}

	ctx := tomb.Context(context.Background())
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second // same as cmdstate

	var stdoutBuf, stderrBuf limitWriter
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = reaper.StartCommand(cmd)
	if err != nil {
		return -1, "", "", fmt.Errorf("cannot start %s: %w", name, err)
	}

	exitCode, waitErr := reaper.WaitCommand(cmd)
	if waitErr != nil {
		logger.Noticef("%s wait error: %v", name, waitErr)
	}

	if ctx.Err() == context.DeadlineExceeded {
		return exitCode, stdoutBuf.String(), stderrBuf.String(), fmt.Errorf("%s timed out after %v", name, timeout)
	}
	if ctx.Err() == context.Canceled {
		return exitCode, stdoutBuf.String(), stderrBuf.String(), fmt.Errorf("%s interrupted", name)
	}

	return exitCode, stdoutBuf.String(), stderrBuf.String(), nil
}

// maxOutputSize is the maximum number of bytes to capture from stdout/stderr.
const maxOutputSize = 10 * 1024 * 1024 // 10MB

// limitWriter is an io.Writer that limits the total bytes written.
type limitWriter struct {
	buf []byte
}

func (w *limitWriter) Write(p []byte) (n int, err error) {
	remaining := maxOutputSize - len(w.buf)
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf = append(w.buf, p[:remaining]...)
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *limitWriter) String() string {
	return string(w.buf)
}
