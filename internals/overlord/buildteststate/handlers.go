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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"
	"gopkg.in/tomb.v2"

	"github.com/canonical/pebble/internals/logger"
	"github.com/canonical/pebble/internals/overlord/state"
	"github.com/canonical/pebble/internals/ptyutil"
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
	var wsIDs []string
	if setup.Interactive {
		wsIDs = []string{WsBuildStdio, WsBuildStderr, WsBuildControl}
	} else {
		wsIDs = []string{WsBuildStdout, WsBuildStderr}
	}
	e := m.registerExecution(task.ID(), buildTaskKind, wsIDs, setup.Interactive, setup.Terminal)
	defer m.unregisterExecution(task.ID())

	// Run snapcraft in the temp directory.
	var timeout time.Duration
	if setup.Timeout > 0 {
		timeout = setup.Timeout
	}

	var exitCode int
	var stdout, stderr string
	var buildErr error

	if setup.Interactive {
		ctx := tomb.Context(context.Background())
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		exitCode, stdout, stderr, buildErr = m.runCommandInteractive("snapcraft", tempDir, ctx, e, WsBuildStdio, WsBuildStderr, WsBuildControl)
	} else {
		exitCode, stdout, stderr, buildErr = m.runCommandStreaming("snapcraft", tempDir, timeout, tomb, e, WsBuildStdout, WsBuildStderr)
	}
	if buildErr != nil {
		return fmt.Errorf("cannot run snapcraft: %w", buildErr)
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
		e2 := m.registerExecution(task.ID(), buildTaskKind, wsIDs, setup.Interactive, setup.Terminal)
		defer m.unregisterExecution(task.ID())

		// Retry the build.
		if setup.Interactive {
			ctx := tomb.Context(context.Background())
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			exitCode, stdout, stderr, buildErr = m.runCommandInteractive("snapcraft", tempDir, ctx, e2, WsBuildStdio, WsBuildStderr, WsBuildControl)
		} else {
			exitCode, stdout, stderr, buildErr = m.runCommandStreaming("snapcraft", tempDir, timeout, tomb, e2, WsBuildStdout, WsBuildStderr)
		}
		if buildErr != nil {
			return fmt.Errorf("cannot run snapcraft: %w", buildErr)
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

	// Determine timeout and interactive settings from the build task's setup.
	var timeout time.Duration
	var interactive, terminal bool
	st.Lock()
	for _, t := range change.Tasks() {
		if t.Kind() == buildTaskKind {
			setupObj := st.Cached(buildTestSetupKey{t.ID()})
			if setup, ok := setupObj.(*buildTestSetup); ok && setup != nil {
				timeout = setup.Timeout
				interactive = setup.Interactive
				terminal = setup.Terminal
			}
			break
		}
	}
	st.Unlock()

	// Register execution for websocket streaming.
	var wsIDs []string
	if interactive {
		wsIDs = []string{WsRunStdio, WsRunStderr, WsRunControl}
	} else {
		wsIDs = []string{WsRunStdout, WsRunStderr}
	}
	e := m.registerExecution(task.ID(), runTaskKind, wsIDs, interactive, terminal)
	defer m.unregisterExecution(task.ID())

	// Run spread in the temp directory.
	var exitCode int
	var stdout, stderr string
	var runErr error

	if interactive {
		ctx := tomb.Context(context.Background())
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		exitCode, stdout, stderr, runErr = m.runCommandInteractive("spread", tempDir, ctx, e, WsRunStdio, WsRunStderr, WsRunControl)
	} else {
		exitCode, stdout, stderr, runErr = m.runCommandStreaming("spread", tempDir, timeout, tomb, e, WsRunStdout, WsRunStderr)
	}
	if runErr != nil {
		return fmt.Errorf("cannot run spread: %w", runErr)
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

// buildTestCommand represents a command sent over the control websocket.
type buildTestCommand struct {
	Command string                `json:"command"`
	Signal  *buildTestSignalArgs  `json:"signal,omitempty"`
	Resize  *buildTestResizeArgs  `json:"resize,omitempty"`
}

type buildTestSignalArgs struct {
	Name string `json:"name"`
}

type buildTestResizeArgs struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// controlLoop reads commands from the control websocket and handles them
// (signal forwarding, terminal resize). It's modeled after cmdstate's
// execution.controlLoop.
func (e *buildTestExecution) controlLoop(taskID string, pidCh <-chan int, stop <-chan struct{}, ptyFd int) {
	logger.Debugf("Build-test %s: control handler waiting", taskID)
	defer logger.Debugf("Build-test %s: control handler finished", taskID)

	// Wait till we receive the process's PID (command started).
	var pid int
	select {
	case pid = <-pidCh:
		break
	case <-stop:
		return
	}

	// Wait till the control websocket is connected.
	select {
	case <-e.controlConnected:
		break
	case <-stop:
		return
	}

	logger.Debugf("Build-test %s: control handler started for PID %d", taskID, pid)
	for {
		controlConn := e.getWebsocket(WsBuildControl)
		if controlConn == nil {
			controlConn = e.getWebsocket(WsRunControl)
		}
		if controlConn == nil {
			logger.Debugf("Build-test %s: no control websocket", taskID)
			break
		}

		mt, r, err := controlConn.NextReader()
		if mt == websocket.CloseMessage {
			break
		}

		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway) {
				logger.Debugf("Build-test %s: cannot get next websocket reader for PID %d: %v", taskID, pid, err)
			}

			if websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
				err := unix.Kill(pid, unix.SIGKILL)
				if err != nil {
					logger.Noticef("Build-test %s: cannot send SIGKILL to pid %d: %v", taskID, pid, err)
				} else {
					logger.Debugf("Build-test %s: sent SIGKILL to pid %d", taskID, pid)
				}
			}

			break
		}

		var command buildTestCommand
		err = json.NewDecoder(r).Decode(&command)
		if err != nil {
			logger.Noticef("Build-test %s: cannot decode control websocket command: %v", taskID, err)
			continue
		}

		switch {
		case command.Command == "resize" && e.terminal:
			if command.Resize == nil {
				logger.Noticef(`Build-test %s: control command "resize" requires terminal width and height`, taskID)
				continue
			}
			w, h := command.Resize.Width, command.Resize.Height
			err = ptyutil.SetSize(ptyFd, w, h)
			if err != nil {
				logger.Noticef(`Build-test %s: control command "resize" cannot set terminal size to %dx%d: %v`, taskID, w, h, err)
				continue
			}
			logger.Debugf(`Build-test %s: PID %d terminal resized to %dx%d`, taskID, pid, w, h)
		case command.Command == "signal":
			if command.Signal == nil {
				logger.Noticef(`Build-test %s: control command "signal" requires signal name`, taskID)
				continue
			}
			name := command.Signal.Name
			sig := unix.SignalNum(name)
			if sig == 0 {
				logger.Noticef("Build-test %s: invalid signal name %q", taskID, name)
				continue
			}
			err := unix.Kill(pid, sig)
			if err != nil {
				logger.Noticef("Build-test %s: cannot send %s to PID %d: %v", taskID, name, pid, err)
				continue
			}
			logger.Debugf("Build-test %s: sent %s to PID %d", taskID, name, pid)
		default:
			logger.Noticef("Build-test %s: invalid control command %q", taskID, command.Command)
		}
	}
}

// runCommandInteractive runs a command with interactive support, using a PTY
// when terminal mode is enabled, or pipes otherwise. It streams output to
// websockets and captures to limitWriter for api-data storage.
func (m *BuildTestManager) runCommandInteractive(name string, dir string, ctx context.Context, e *buildTestExecution, stdioWsID, stderrWsID, controlWsID string) (exitCode int, stdout string, stderr string, err error) {
	// Wait for websocket connections (with timeout).
	waitErr := e.waitIOConnected(ctx, name)
	if waitErr != nil {
		return -1, "", "", fmt.Errorf("build-test %s: cannot start interactive mode, websocket connections not established: %w", name, waitErr)
	}

	var beforeClosers []io.Closer
	var afterClosers []io.Closer
	var wgOutputSent sync.WaitGroup

	// Closed to make the controlLoop stop early.
	stopControl := make(chan struct{})
	defer close(stopControl)

	pidCh := make(chan int)
	childDead := make(chan struct{})

	var stdoutLimit, stderrLimit limitWriter

	if e.terminal {
		// Allocate a pseudo-terminal.
		uid, gid := os.Getuid(), os.Getgid()
		master, slave, ptyErr := ptyutil.OpenPty(int64(uid), int64(gid))
		if ptyErr != nil {
			return -1, "", "", fmt.Errorf("cannot allocate PTY: %w", ptyErr)
		}
		afterClosers = append(afterClosers, master)
		beforeClosers = append(beforeClosers, slave)

		// Start the control loop for signal/resize handling.
		go e.controlLoop(name, pidCh, stopControl, int(master.Fd()))

		// Mirror PTY output to the stdio websocket and capture to limitWriter.
		stdioConn := e.getWebsocket(stdioWsID)
		wgOutputSent.Go(func() {
			// Use a pipe to capture output from MirrorToWebsocket.
			// MirrorToWebsocket writes to the websocket directly, so we
			// need a different approach: read from master via a tee.
			// We'll use ExecReaderToChannel + manual websocket writing
			// with a TeeReader for capture.
			in := wsutil.ExecReaderToChannel(master, -1, childDead, int(master.Fd()))
			for {
				buf, ok := <-in
				if !ok {
					_ = master.Close()
					// Capture any remaining output.
					stdoutLimit.Write(buf)
					// Send write barrier.
					stdioConn.WriteMessage(websocket.TextMessage, endCommandJSON)
					return
				}
				// Capture output to limitWriter.
				stdoutLimit.Write(buf)
				// Send to websocket.
				err := stdioConn.WriteMessage(websocket.BinaryMessage, buf)
				if err != nil {
					logger.Debugf("Build-test %s: error writing to stdio websocket: %v", name, err)
					break
				}
			}
			closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			stdioConn.WriteMessage(websocket.CloseMessage, closeMsg)
			master.Close()
		})

		if e.interactive {
			// Interactive: receive stdin from stdio websocket and write to PTY.
			go func() {
				<-wsutil.WebsocketRecvStream(master, stdioConn)
				// Send Ctrl-D to indicate end of input.
				master.Write([]byte{byte(unix.VEOF)})
			}()
		} else {
			// Non-interactive with PTY: receive stdin from stdio websocket
			// and write to the PTY's stdin pipe.
			stdinReader, stdinWriter, pipeErr := os.Pipe()
			if pipeErr != nil {
				return -1, "", "", fmt.Errorf("cannot create stdin pipe: %w", pipeErr)
			}
			afterClosers = append(afterClosers, stdinReader)
			go func() {
				<-wsutil.WebsocketRecvStream(stdinWriter, stdioConn)
				stdinWriter.Close()
			}()
		}

		// Handle stderr separately if a stderr websocket is connected.
		if stderrConn := e.getWebsocket(stderrWsID); stderrConn != nil {
			// In PTY mode, stderr goes to the same PTY, so we don't have
			// a separate stderr stream. Just skip stderr websocket in this case.
			logger.Debugf("Build-test %s: stderr websocket not used in PTY terminal mode", name)
		}

		// Build the command.
		cmd := exec.CommandContext(ctx, name)
		cmd.Dir = dir
		cmd.Stdin = slave
		cmd.Stdout = slave
		cmd.Stderr = slave
		cmd.WaitDelay = time.Second
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setsid:  true,
			Setctty: true,
		}

		err = reaper.StartCommand(cmd)
		if err != nil {
			for _, closer := range beforeClosers {
				_ = closer.Close()
			}
			for _, closer := range afterClosers {
				_ = closer.Close()
			}
			return -1, "", "", fmt.Errorf("cannot start %s: %w", name, err)
		}

		// Send PID to control loop.
		pidCh <- cmd.Process.Pid

		exitCode, waitErr = reaper.WaitCommand(cmd)
		if waitErr != nil {
			logger.Noticef("%s wait error: %v", name, waitErr)
		}

		// Signal that the child is dead for ExecReaderToChannel.
		close(childDead)

		// Close the write end of pipes so streaming goroutines get EOF.
		for _, closer := range beforeClosers {
			_ = closer.Close()
		}

		// Close the control websocket.
		controlConn := e.getWebsocket(controlWsID)
		if controlConn != nil {
			_ = controlConn.Close()
		}

		// Wait for all output to be sent.
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

	// Non-PTY interactive mode: use pipes for stdin/stdout/stderr.
	go e.controlLoop(name, pidCh, stopControl, -1)

	// Stdin: receive from stdio websocket and write to cmd.Stdin pipe.
	stdioConn := e.getWebsocket(stdioWsID)
	stdinReader, stdinWriter, pipeErr := os.Pipe()
	if pipeErr != nil {
		return -1, "", "", fmt.Errorf("cannot create stdin pipe: %w", pipeErr)
	}
	afterClosers = append(afterClosers, stdinReader)
	go func() {
		<-wsutil.WebsocketRecvStream(stdinWriter, stdioConn)
		stdinWriter.Close()
	}()

	// Stdout: pipe -> tee(limitWriter + websocket)
	stdoutReader, stdoutWriter, pipeErr := os.Pipe()
	if pipeErr != nil {
		return -1, "", "", fmt.Errorf("cannot create stdout pipe: %w", pipeErr)
	}
	beforeClosers = append(beforeClosers, stdoutWriter)

	wgOutputSent.Go(func() {
		teeReader := io.TeeReader(stdoutReader, &stdoutLimit)
		<-wsutil.WebsocketSendStream(stdioConn, teeReader, -1)
		stdoutReader.Close()
	})

	// Stderr: pipe -> tee(limitWriter + websocket) if stderr websocket exists.
	stderrConn := e.getWebsocket(stderrWsID)
	var stderrWriter *os.File
	if stderrConn != nil {
		stderrReader, stderrPipeWriter, pipeErr := os.Pipe()
		if pipeErr != nil {
			return -1, "", "", fmt.Errorf("cannot create stderr pipe: %w", pipeErr)
		}
		beforeClosers = append(beforeClosers, stderrPipeWriter)
		stderrWriter = stderrPipeWriter

		wgOutputSent.Go(func() {
			teeReader := io.TeeReader(stderrReader, &stderrLimit)
			<-wsutil.WebsocketSendStream(stderrConn, teeReader, -1)
			stderrReader.Close()
		})
	}

	cmd := exec.CommandContext(ctx, name)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	cmd.Stdin = stdinReader
	cmd.Stdout = stdoutWriter
	if stderrWriter != nil {
		cmd.Stderr = stderrWriter
	} else {
		cmd.Stderr = stdoutWriter // combine stderr into stdout
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	err = reaper.StartCommand(cmd)
	if err != nil {
		for _, closer := range beforeClosers {
			_ = closer.Close()
		}
		for _, closer := range afterClosers {
			_ = closer.Close()
		}
		return -1, "", "", fmt.Errorf("cannot start %s: %w", name, err)
	}

	// Send PID to control loop.
	pidCh <- cmd.Process.Pid

	exitCode, waitErr = reaper.WaitCommand(cmd)
	if waitErr != nil {
		logger.Noticef("%s wait error: %v", name, waitErr)
	}

	// Close the write end of the pipes so the streaming goroutines get EOF.
	for _, closer := range beforeClosers {
		_ = closer.Close()
	}

	// Close the control websocket.
	controlConn := e.getWebsocket(controlWsID)
	if controlConn != nil {
		_ = controlConn.Close()
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

var endCommandJSON = []byte(`{"command":"end"}`)
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
