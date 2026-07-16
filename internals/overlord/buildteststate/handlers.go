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
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"gopkg.in/tomb.v2"

	"github.com/canonical/pebble/internals/logger"
	"github.com/canonical/pebble/internals/overlord/state"
	"github.com/canonical/pebble/internals/reaper"
	"github.com/canonical/pebble/internals/wsutil"
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

	// Register execution for websocket streaming.
	wsIDs := []string{WsBuildStdout, WsBuildStderr}
	e := m.registerExecution(task.ID(), buildTaskKind, wsIDs)
	defer m.unregisterExecution(task.ID())

	// Run snapcraft in the temp directory.
	var timeout time.Duration
	if setup.Timeout > 0 {
		timeout = setup.Timeout
	}
	exitCode, stdout, stderr, err := m.runCommandStreaming("snapcraft", tempDir, timeout, tomb, e, WsBuildStdout, WsBuildStderr)
	if err != nil {
		return fmt.Errorf("cannot run snapcraft: %w", err)
	}

	// If the build failed, try cleaning and retrying once.
	if exitCode != 0 && setup.RetryCount < maxBuildRetries {
		logger.Noticef("Snapcraft build failed (exit code %d), retrying with clean (attempt %d/%d)",
			exitCode, setup.RetryCount+1, maxBuildRetries)

		originalExitCode := exitCode
		originalStderr := stderr

		// Run snapcraft clean before retrying.
		cleanStdout, cleanStderr, cleanErr := m.runCleanCommand(tempDir, timeout)
		if cleanErr != nil {
			logger.Noticef("Snapcraft clean failed: %v (stdout: %s, stderr: %s), proceeding with retry anyway",
				cleanErr, cleanStdout, cleanStderr)
		}

		// Update retry count in state cache.
		st.Lock()
		setup.RetryCount++
		st.Cache(buildTestSetupKey{task.ID()}, setup)
		st.Unlock()

		// Re-register execution for websocket streaming on retry.
		e2 := m.registerExecution(task.ID(), buildTaskKind, wsIDs)
		defer m.unregisterExecution(task.ID())

		// Retry the build.
		exitCode, stdout, stderr, err = m.runCommandStreaming("snapcraft", tempDir, timeout, tomb, e2, WsBuildStdout, WsBuildStderr)
		if err != nil {
			return fmt.Errorf("cannot run snapcraft: %w", err)
		}

		// Store the results in the task's api-data, including retry info.
		st.Lock()
		task.Set("api-data", map[string]any{
			"stdout":             stdout,
			"stderr":             stderr,
			"exit-code":          exitCode,
			"retried":            true,
			"original-exit-code": originalExitCode,
			"original-stderr":    truncateString(originalStderr, 1024),
		})
		st.Unlock()

		if exitCode != 0 {
			return fmt.Errorf("snapcraft failed with exit code %d (retried after clean, original exit code %d)",
				exitCode, originalExitCode)
		}

		return nil
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

	// Register execution for websocket streaming.
	wsIDs := []string{WsRunStdout, WsRunStderr}
	e := m.registerExecution(task.ID(), runTaskKind, wsIDs)
	defer m.unregisterExecution(task.ID())

	// Run spread in the temp directory.
	exitCode, stdout, stderr, err := m.runCommandStreaming("spread", tempDir, timeout, tomb, e, WsRunStdout, WsRunStderr)
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

// runCommandStreaming runs a command in the given directory, streaming stdout
// and stderr to websockets if connected, and capturing output for api-data.
// If no websocket connects within the connect timeout, it falls back to
// buffered capture (the original non-streaming behavior).
func (m *BuildTestManager) runCommandStreaming(name string, dir string, timeout time.Duration, tomb *tomb.Tomb, e *buildTestExecution, stdoutWsID, stderrWsID string) (exitCode int, stdout string, stderr string, err error) {
	if fakeRunCommand != nil {
		return fakeRunCommand(name, dir, timeout, tomb)
	}

	ctx := tomb.Context(context.Background())
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Wait for websocket connections (with timeout).
	// If no connections arrive, fall back to buffered capture.
	streaming := false
	waitErr := e.waitIOConnected(ctx, name)
	if waitErr == nil {
		streaming = true
	} else {
		logger.Debugf("Build-test %s: no websocket connections, falling back to buffered capture: %v", name, waitErr)
	}

	if streaming {
		return m.runCommandPiped(name, dir, ctx, e, stdoutWsID, stderrWsID)
	}
	return runCommandBuffered(name, dir, ctx)
}

// runCommandPiped runs a command with pipes, streaming output to websockets
// and also capturing to limitWriter for api-data storage.
func (m *BuildTestManager) runCommandPiped(name string, dir string, ctx context.Context, e *buildTestExecution, stdoutWsID, stderrWsID string) (exitCode int, stdout string, stderr string, err error) {
	var beforeClosers []io.Closer
	var afterClosers []io.Closer
	var wgOutputSent sync.WaitGroup

	var stdoutLimit, stderrLimit limitWriter

	// Stdout: pipe -> tee(limitWriter + websocket)
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return -1, "", "", fmt.Errorf("cannot create stdout pipe: %w", err)
	}
	beforeClosers = append(beforeClosers, stdoutWriter)

	stdoutConn := e.getWebsocket(stdoutWsID)
	wgOutputSent.Go(func() {
		// Tee the stdout pipe output to both the limitWriter and the websocket.
		teeReader := io.TeeReader(stdoutReader, &stdoutLimit)
		<-wsutil.WebsocketSendStream(stdoutConn, teeReader, -1)
		stdoutReader.Close()
	})

	// Stderr: pipe -> tee(limitWriter + websocket)
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		return -1, "", "", fmt.Errorf("cannot create stderr pipe: %w", err)
	}
	beforeClosers = append(beforeClosers, stderrWriter)

	stderrConn := e.getWebsocket(stderrWsID)
	wgOutputSent.Go(func() {
		// Tee the stderr pipe output to both the limitWriter and the websocket.
		teeReader := io.TeeReader(stderrReader, &stderrLimit)
		<-wsutil.WebsocketSendStream(stderrConn, teeReader, -1)
		stderrReader.Close()
	})

	cmd := exec.CommandContext(ctx, name)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	err = reaper.StartCommand(cmd)
	if err != nil {
		// Close pipes on error.
		for _, closer := range beforeClosers {
			_ = closer.Close()
		}
		for _, closer := range afterClosers {
			_ = closer.Close()
		}
		return -1, "", "", fmt.Errorf("cannot start %s: %w", name, err)
	}

	exitCode, waitErr := reaper.WaitCommand(cmd)
	if waitErr != nil {
		logger.Noticef("%s wait error: %v", name, waitErr)
	}

	// Close the write end of the pipes so the streaming goroutines get EOF.
	for _, closer := range beforeClosers {
		_ = closer.Close()
	}

	// Wait for all output to be sent over websockets.
	wgOutputSent.Wait()

	for _, closer := range afterClosers {
		_ = closer.Close()
	}

	if ctx.Err() == context.DeadlineExceeded {
		return exitCode, stdoutLimit.String(), stderrLimit.String(), fmt.Errorf("%s timed out after %v", name, ctx.Err())
	}
	if ctx.Err() == context.Canceled {
		return exitCode, stdoutLimit.String(), stderrLimit.String(), fmt.Errorf("%s interrupted", name)
	}

	return exitCode, stdoutLimit.String(), stderrLimit.String(), nil
}

// runCommandBuffered runs a command with buffered capture (no streaming).
// This is the fallback when no websocket connections are available.
// If extraArgs are provided, they are appended to the command arguments.
func runCommandBuffered(name string, dir string, ctx context.Context, extraArgs ...string) (exitCode int, stdout string, stderr string, err error) {
	cmd := exec.CommandContext(ctx, name, extraArgs...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second

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
		return exitCode, stdoutBuf.String(), stderrBuf.String(), fmt.Errorf("%s timed out after %v", name, ctx.Err())
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

// runCleanCommand runs "snapcraft clean" in the given directory. It uses
// buffered capture (no streaming) since clean is typically fast. The timeout
// is capped at 30 seconds or 10% of the build timeout, whichever is smaller,
// to avoid hanging on the clean step.
func (m *BuildTestManager) runCleanCommand(dir string, buildTimeout time.Duration) (stdout string, stderr string, err error) {
	if fakeCleanCommand != nil {
		return fakeCleanCommand(dir)
	}

	cleanTimeout := 30 * time.Second
	if buildTimeout > 0 {
		tenth := buildTimeout / 10
		if tenth < cleanTimeout {
			cleanTimeout = tenth
		}
		// Ensure at least 5 seconds for clean.
		if cleanTimeout < 5*time.Second {
			cleanTimeout = 5 * time.Second
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cleanTimeout)
	defer cancel()

	exitCode, stdout, stderr, err := runCommandBuffered("snapcraft", dir, ctx, "clean")
	if err != nil {
		return stdout, stderr, err
	}
	if exitCode != 0 {
		return stdout, stderr, fmt.Errorf("snapcraft clean failed with exit code %d", exitCode)
	}
	return stdout, stderr, nil
}

// fakeCleanCommand is used for testing to mock out the clean command execution.
var fakeCleanCommand func(dir string) (stdout string, stderr string, err error)

// truncateString truncates s to at most maxLen bytes, appending "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
