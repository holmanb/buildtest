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

package client

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/canonical/pebble/internals/logger"
	"github.com/canonical/pebble/internals/wsutil"
	"github.com/gorilla/websocket"
)

// BuildTestOptions holds the options for a build-test request.
type BuildTestOptions struct {
	// SourcePath is the local path to the source directory to be built and tested.
	// The directory will be tar'd and uploaded to the server.
	SourcePath string

	// Timeout is the optional overall timeout for the operation.
	Timeout time.Duration

	// Stdout is an optional writer for streaming build/test stdout output.
	// If set, output is streamed in real-time via websockets. If nil, output
	// is captured and returned in the result.
	Stdout io.Writer

	// Stderr is an optional writer for streaming build/test stderr output.
	// If set, output is streamed in real-time via websockets. If nil, output
	// is captured and returned in the result.
	Stderr io.Writer

	// Stdin is an optional reader for sending input to the build/run processes.
	// Only used when Interactive is true.
	Stdin io.Reader

	// Interactive enables interactive mode, allowing stdin to be forwarded
	// to the build/run processes via websockets. When true, Terminal should
	// also be set to true for proper PTY support.
	Interactive bool

	// Terminal allocates a pseudo-terminal for the build/run processes.
	// Typically set to true when Interactive is true.
	Terminal bool

	// Width is the initial terminal width (columns) for the PTY.
	// Only used when Interactive and Terminal are true.
	Width int

	// Height is the initial terminal height (rows) for the PTY.
	// Only used when Interactive and Terminal are true.
	Height int
}

// BuildTestResult holds the result of a build-test request.
type BuildTestResult struct {
	Build BuildResult  `json:"build" yaml:"build"`
	Run   *RunResult   `json:"run,omitempty" yaml:"run,omitempty"`
}

// BuildResult holds the result of the build step.
type BuildResult struct {
	Stdout           string `json:"stdout" yaml:"stdout"`
	Stderr           string `json:"stderr" yaml:"stderr"`
	ExitCode         int    `json:"exit-code" yaml:"exit-code"`
	Retried          bool   `json:"retried,omitempty" yaml:"retried,omitempty"`
	OriginalExitCode int    `json:"original-exit-code,omitempty" yaml:"original-exit-code,omitempty"`
}

// RunResult holds the result of the test run step.
type RunResult struct {
	Stdout   string `json:"stdout" yaml:"stdout"`
	Stderr   string `json:"stderr" yaml:"stderr"`
	ExitCode int    `json:"exit-code" yaml:"exit-code"`
}

type buildTestMetadataPayload struct {
	Timeout     string `json:"timeout,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Terminal    bool   `json:"terminal,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

// BuildTest uploads the source code from SourcePath, builds it with snapcraft,
// and runs spread tests. It returns the build and test results.
func (client *Client) BuildTest(opts *BuildTestOptions) (*BuildTestResult, error) {
	// Create a tarball from the source directory.
	tarball, err := createSourceTarball(opts.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("cannot create source tarball: %w", err)
	}
	defer os.Remove(tarball.Name())
	defer tarball.Close()

	// Build the multipart request.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	// Encode metadata part.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/json"},
		"Content-Disposition": {`form-data; name="request"`},
	})
	if err != nil {
		return nil, fmt.Errorf("cannot encode metadata in request payload: %w", err)
	}

	metadata := buildTestMetadataPayload{}
	if opts.Timeout != 0 {
		metadata.Timeout = opts.Timeout.String()
	}
	if opts.Interactive {
		metadata.Interactive = true
		metadata.Terminal = opts.Terminal
		metadata.Width = opts.Width
		metadata.Height = opts.Height
	}
	if err := json.NewEncoder(part).Encode(&metadata); err != nil {
		return nil, fmt.Errorf("cannot encode metadata: %w", err)
	}

	// Encode source tarball part.
	sourcePart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/gzip"},
		"Content-Disposition": {`form-data; name="source"; filename="source.tar.gz"`},
	})
	if err != nil {
		return nil, fmt.Errorf("cannot encode source in request payload: %w", err)
	}

	// Write the tarball content.
	if _, err := tarball.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("cannot seek tarball: %w", err)
	}
	if _, err := io.Copy(sourcePart, tarball); err != nil {
		return nil, fmt.Errorf("cannot write source tarball: %w", err)
	}

	// Close the multipart writer and capture the complete body.
	contentType := mw.FormDataContentType()
	mw.Close()

	// Send the async request.
	resp, err := client.Requester().Do(context.Background(), &RequestOptions{
		Type:    AsyncRequest,
		Method:  "POST",
		Path:    "/v1/build-test",
		Headers: map[string]string{"Content-Type": contentType},
		Body:    bytes.NewReader(body.Bytes()),
	})
	if err != nil {
		return nil, err
	}

	// Determine if we should stream output via websockets.
	streaming := opts.Stdout != nil || opts.Stderr != nil

	if streaming {
		return client.buildTestStreaming(resp.ChangeID, opts)
	}

	// Non-streaming: wait for the change to complete and extract results.
	waitOpts := &WaitChangeOptions{}
	if opts.Timeout != 0 {
		waitOpts.Timeout = opts.Timeout + 30*time.Second // extra buffer beyond command timeout
	}
	change, err := client.WaitChange(resp.ChangeID, waitOpts)
	if err != nil {
		return nil, fmt.Errorf("cannot wait for build-test to complete: %w", err)
	}
	if change.Err != "" {
		return nil, errors.New(change.Err)
	}

	// Extract results from the change's tasks.
	result := &BuildTestResult{}
	for _, task := range change.Tasks {
		switch task.Kind {
		case "build-test-build":
			result.Build = BuildResult{
				Stdout:           taskGetString(task, "stdout"),
				Stderr:           taskGetString(task, "stderr"),
				ExitCode:         taskGetInt(task, "exit-code"),
				Retried:          taskGetBool(task, "retried"),
				OriginalExitCode: taskGetInt(task, "original-exit-code"),
			}
		case "build-test-run":
			result.Run = &RunResult{
				Stdout:   taskGetString(task, "stdout"),
				Stderr:   taskGetString(task, "stderr"),
				ExitCode: taskGetInt(task, "exit-code"),
			}
		}
	}

	return result, nil
}

// buildTestStreaming handles the streaming case where Stdout/Stderr writers
// are provided. It connects to websockets for each task and streams output
// in real-time, then waits for the change to complete.
//
// Because the build and run tasks are sequential (run waits for build),
// we must connect to the run task's websockets only after the build task
// has completed, since the run task's execution is not registered until
// it starts running.
// BuildTestInteractive starts an interactive build-test session. It returns
// a BuildTestProcess that can be used to send signals, resize the terminal,
// and wait for completion. This is a non-blocking call — the caller should
// set up signal forwarding and then call Wait() on the returned process.
//
// Unlike BuildTest(), this method requires Stdin, Stdout, and Stderr to be
// set, and Interactive and Terminal should both be true.
func (client *Client) BuildTestInteractive(opts *BuildTestOptions) (*BuildTestProcess, error) {
	// Create a tarball from the source directory.
	tarball, err := createSourceTarball(opts.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("cannot create source tarball: %w", err)
	}
	defer os.Remove(tarball.Name())
	defer tarball.Close()

	// Build the multipart request.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	// Encode metadata part.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/json"},
		"Content-Disposition": {`form-data; name="request"`},
	})
	if err != nil {
		return nil, fmt.Errorf("cannot encode metadata in request payload: %w", err)
	}

	metadata := buildTestMetadataPayload{}
	if opts.Timeout != 0 {
		metadata.Timeout = opts.Timeout.String()
	}
	metadata.Interactive = true
	metadata.Terminal = opts.Terminal
	metadata.Width = opts.Width
	metadata.Height = opts.Height
	if err := json.NewEncoder(part).Encode(&metadata); err != nil {
		return nil, fmt.Errorf("cannot encode metadata: %w", err)
	}

	// Encode source tarball part.
	sourcePart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/gzip"},
		"Content-Disposition": {`form-data; name="source"; filename="source.tar.gz"`},
	})
	if err != nil {
		return nil, fmt.Errorf("cannot encode source in request payload: %w", err)
	}

	// Write the tarball content.
	if _, err := tarball.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("cannot seek tarball: %w", err)
	}
	if _, err := io.Copy(sourcePart, tarball); err != nil {
		return nil, fmt.Errorf("cannot write source tarball: %w", err)
	}

	// Close the multipart writer and capture the complete body.
	contentType := mw.FormDataContentType()
	mw.Close()

	// Send the async request.
	resp, err := client.Requester().Do(context.Background(), &RequestOptions{
		Type:    AsyncRequest,
		Method:  "POST",
		Path:    "/v1/build-test",
		Headers: map[string]string{"Content-Type": contentType},
		Body:    bytes.NewReader(body.Bytes()),
	})
	if err != nil {
		return nil, err
	}

	return client.buildTestInteractive(resp.ChangeID, opts)
}

func (client *Client) buildTestStreaming(changeID string, opts *BuildTestOptions) (*BuildTestResult, error) {
	// Poll the change until tasks are available, so we can get task IDs
	// and connect to websockets before the tasks complete.
	var change *Change
	for {
		var err error
		change, err = client.Change(changeID)
		if err != nil {
			return nil, fmt.Errorf("cannot get change %q: %w", changeID, err)
		}
		if len(change.Tasks) > 0 {
			break
		}
		// Brief pause before polling again.
		time.Sleep(100 * time.Millisecond)
	}

	// Find the build and run task IDs.
	var buildTaskID, runTaskID string
	for _, task := range change.Tasks {
		switch task.Kind {
		case "build-test-build":
			buildTaskID = task.ID
		case "build-test-run":
			runTaskID = task.ID
		}
	}

	// Connect to the build task's websockets immediately, since the build
	// task starts first and its execution is registered right away.
	var writesDone []chan bool
	if buildTaskID != "" {
		buildStdoutDone, buildStderrDone := client.streamTaskOutput(buildTaskID, "build", opts.Stdout, opts.Stderr)
		if buildStdoutDone != nil {
			writesDone = append(writesDone, buildStdoutDone)
		}
		if buildStderrDone != nil {
			writesDone = append(writesDone, buildStderrDone)
		}
	}

	// Wait for the build task to complete before connecting to the run
	// task's websockets, since the run task's execution is not registered
	// until the build task finishes and the run task starts.
	waitOpts := &WaitChangeOptions{}
	if opts.Timeout != 0 {
		waitOpts.Timeout = opts.Timeout + 30*time.Second
	}

	// Poll the change until the build task is done (or the whole change is
	// done, which may happen if the build fails and there's no run task).
	for {
		change, err := client.Change(changeID)
		if err != nil {
			return nil, fmt.Errorf("cannot get change %q: %w", changeID, err)
		}
		if change.Err != "" {
			return nil, errors.New(change.Err)
		}

		// Check if the build task is done.
		buildDone := false
		for _, task := range change.Tasks {
			if task.Kind == "build-test-build" && task.Status == "Done" {
				buildDone = true
			}
		}

		// If the build is done, or the whole change is ready, break out.
		if buildDone || change.Ready {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	// Connect to the run task's websockets now that the build is done
	// (the run task's execution should be registered).
	if runTaskID != "" && change.Err == "" {
		// Check if the build succeeded (non-zero exit code means the run
		// task won't execute).
		buildFailed := false
		for _, task := range change.Tasks {
			if task.Kind == "build-test-build" && taskGetInt(task, "exit-code") != 0 {
				buildFailed = true
			}
		}
		if !buildFailed {
			runStdoutDone, runStderrDone := client.streamTaskOutput(runTaskID, "run", opts.Stdout, opts.Stderr)
			if runStdoutDone != nil {
				writesDone = append(writesDone, runStdoutDone)
			}
			if runStderrDone != nil {
				writesDone = append(writesDone, runStderrDone)
			}
		}
	}

	// Wait for the change to complete.
	change, err := client.WaitChange(changeID, waitOpts)
	if err != nil {
		return nil, fmt.Errorf("cannot wait for build-test to complete: %w", err)
	}
	if change.Err != "" {
		return nil, errors.New(change.Err)
	}

	// Wait for all streaming output to be flushed.
	for _, done := range writesDone {
		<-done
	}

	// Extract results from the completed change.
	result := &BuildTestResult{}
	for _, task := range change.Tasks {
		switch task.Kind {
		case "build-test-build":
			result.Build = BuildResult{
				ExitCode:         taskGetInt(task, "exit-code"),
				Retried:          taskGetBool(task, "retried"),
				OriginalExitCode: taskGetInt(task, "original-exit-code"),
			}
		case "build-test-run":
			result.Run = &RunResult{
				ExitCode: taskGetInt(task, "exit-code"),
			}
		}
	}

	return result, nil
}

// streamTaskOutput connects to the stdout and stderr websockets for a task
// and streams output to the provided writers. Returns channels that are
// closed when streaming is done for each websocket.
func (client *Client) streamTaskOutput(taskID, prefix string, stdout, stderr io.Writer) (stdoutDone, stderrDone chan bool) {
	if stdout != nil {
		stdoutConn, err := client.getTaskWebsocket(taskID, prefix+"-stdout")
		if err == nil {
			stdoutDone = wsutil.WebsocketRecvStream(stdout, stdoutConn)
			go func() {
				<-stdoutDone
				_ = stdoutConn.Close()
			}()
		}
	}

	if stderr != nil {
		stderrConn, err := client.getTaskWebsocket(taskID, prefix+"-stderr")
		if err == nil {
			stderrDone = wsutil.WebsocketRecvStream(stderr, stderrConn)
			go func() {
				<-stderrDone
				_ = stderrConn.Close()
			}()
		}
	}

	return stdoutDone, stderrDone
}

// BuildTestProcess represents a running interactive build-test process.
// Use Wait to wait for it to finish.
type BuildTestProcess struct {
	changeID    string
	client      *Client
	timeout     time.Duration
	writesDone  chan struct{}
	controlMu   sync.Mutex
	controlConn clientWebsocket
	stdinDone   chan bool // only used by tests
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

// SendSignal sends a signal to the running build or run process.
func (p *BuildTestProcess) SendSignal(signal string) error {
	p.controlMu.Lock()
	conn := p.controlConn
	p.controlMu.Unlock()
	if conn == nil {
		return fmt.Errorf("no control websocket connection")
	}
	msg := buildTestCommand{
		Command: "signal",
		Signal: &buildTestSignalArgs{
			Name: signal,
		},
	}
	return conn.WriteJSON(msg)
}

// SendResize sends a terminal resize message to the running process.
func (p *BuildTestProcess) SendResize(width, height int) error {
	p.controlMu.Lock()
	conn := p.controlConn
	p.controlMu.Unlock()
	if conn == nil {
		return fmt.Errorf("no control websocket connection")
	}
	msg := buildTestCommand{
		Command: "resize",
		Resize: &buildTestResizeArgs{
			Width:  width,
			Height: height,
		},
	}
	return conn.WriteJSON(msg)
}

// Wait waits for the build-test process to finish and returns the result.
func (p *BuildTestProcess) Wait() (*BuildTestResult, error) {
	waitOpts := &WaitChangeOptions{}
	if p.timeout != 0 {
		waitOpts.Timeout = p.timeout + 30*time.Second
	}
	change, err := p.client.WaitChange(p.changeID, waitOpts)
	if err != nil {
		return nil, fmt.Errorf("cannot wait for build-test to complete: %w", err)
	}
	if change.Err != "" {
		return nil, errors.New(change.Err)
	}

	// Wait for any remaining I/O to be flushed.
	<-p.writesDone

	// Extract results from the completed change.
	result := &BuildTestResult{}
	for _, task := range change.Tasks {
		switch task.Kind {
		case "build-test-build":
			result.Build = BuildResult{
				ExitCode:         taskGetInt(task, "exit-code"),
				Retried:          taskGetBool(task, "retried"),
				OriginalExitCode: taskGetInt(task, "original-exit-code"),
			}
		case "build-test-run":
			result.Run = &RunResult{
				ExitCode: taskGetInt(task, "exit-code"),
			}
		}
	}

	return result, nil
}

// buildTestInteractive handles the interactive case where the user wants
// to send stdin to the build/run processes. It connects to stdio and
// control websockets for each task phase.
func (client *Client) buildTestInteractive(changeID string, opts *BuildTestOptions) (*BuildTestProcess, error) {
	// Poll the change until tasks are available.
	var change *Change
	for {
		var err error
		change, err = client.Change(changeID)
		if err != nil {
			return nil, fmt.Errorf("cannot get change %q: %w", changeID, err)
		}
		if len(change.Tasks) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Find the build and run task IDs.
	var buildTaskID, runTaskID string
	for _, task := range change.Tasks {
		switch task.Kind {
		case "build-test-build":
			buildTaskID = task.ID
		case "build-test-run":
			runTaskID = task.ID
		}
	}

	stdin := opts.Stdin
	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = io.Discard
	}

	// writesDone is closed when ALL I/O (build and run) is complete.
	writesDone := make(chan struct{})
	var wgIO sync.WaitGroup

	// Create the process early so we can atomically swap control connections.
	process := &BuildTestProcess{
		changeID:   changeID,
		client:     client,
		timeout:    opts.Timeout,
		writesDone: writesDone,
	}

	// Connect to the build task's websockets.
	if buildTaskID != "" {
		// Connect to build control websocket.
		buildControlConn, err := client.getTaskWebsocket(buildTaskID, "build-control")
		if err != nil {
			return nil, fmt.Errorf("cannot connect to build control websocket: %w", err)
		}
		process.controlMu.Lock()
		process.controlConn = buildControlConn
		process.controlMu.Unlock()

		// Connect to build stdio websocket (bidirectional).
		buildStdioConn, err := client.getTaskWebsocket(buildTaskID, "build-stdio")
		if err != nil {
			return nil, fmt.Errorf("cannot connect to build stdio websocket: %w", err)
		}

		// Don't forward stdin to the build phase — snapcraft doesn't need
		// interactive input. Send an "end" command to signal no stdin.
		buildStdinDone := make(chan bool, 1)
		go func() {
			buildStdioConn.WriteMessage(websocket.TextMessage, []byte(`{"command":"end"}`))
			close(buildStdinDone)
		}()

		// Receive stdout from the stdio websocket.
		stdoutDone := wsutil.WebsocketRecvStream(stdout, buildStdioConn)

		// Connect to build stderr websocket if needed.
		var stderrDone chan bool
		var buildStderrConn clientWebsocket
		if opts.Stderr != nil {
			buildStderrConn, err = client.getTaskWebsocket(buildTaskID, "build-stderr")
			if err == nil {
				stderrDone = wsutil.WebsocketRecvStream(opts.Stderr, buildStderrConn)
			}
		}

		// Track build I/O completion.
		// Note: we do NOT close buildControlConn here — it stays open
		// until the run phase's control websocket replaces it, so that
		// signals sent during the transition are not lost.
		wgIO.Add(1)
		go func() {
			defer wgIO.Done()
			<-buildStdinDone
			<-stdoutDone
			if stderrDone != nil {
				<-stderrDone
			}
			_ = buildStdioConn.Close()
			if buildStderrConn != nil {
				_ = buildStderrConn.Close()
			}
		}()
	}

	// Wait for the build task to complete before connecting to run websockets.
	for {
		change, err := client.Change(changeID)
		if err != nil {
			return nil, fmt.Errorf("cannot get change %q: %w", changeID, err)
		}
		if change.Err != "" {
			return nil, errors.New(change.Err)
		}

		buildDone := false
		for _, task := range change.Tasks {
			if task.Kind == "build-test-build" && task.Status == "Done" {
				buildDone = true
			}
		}
		if buildDone || change.Ready {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Connect to the run task's websockets if the build succeeded.
	if runTaskID != "" && change.Err == "" {
		buildFailed := false
		for _, task := range change.Tasks {
			if task.Kind == "build-test-build" && taskGetInt(task, "exit-code") != 0 {
				buildFailed = true
			}
		}
		if !buildFailed {
			// Connect to run control websocket.
			runControlConn, err := client.getTaskWebsocket(runTaskID, "run-control")
			if err != nil {
				logger.Debugf("Cannot connect to run control websocket: %v", err)
			} else {
				// Atomically swap the control connection from build to run.
				// Close the old build control after the swap so signals
				// sent during the transition are not lost.
				process.controlMu.Lock()
				oldConn := process.controlConn
				process.controlConn = runControlConn
				process.controlMu.Unlock()
				if oldConn != nil {
					_ = oldConn.Close()
				}
			}

			// Connect to run stdio websocket.
			runStdioConn, err := client.getTaskWebsocket(runTaskID, "run-stdio")
			if err != nil {
				logger.Debugf("Cannot connect to run stdio websocket: %v", err)
			} else {
				// Forward stdin to the run stdio websocket.
				runStdinDone := wsutil.WebsocketSendStream(runStdioConn, stdin, -1)
				process.stdinDone = runStdinDone
				// Receive stdout from the run stdio websocket.
				runStdoutDone := wsutil.WebsocketRecvStream(stdout, runStdioConn)

				// Connect to run stderr websocket if needed.
				var runStderrDone chan bool
				var runStderrConn clientWebsocket
				if opts.Stderr != nil {
					runStderrConn, err = client.getTaskWebsocket(runTaskID, "run-stderr")
					if err == nil {
						runStderrDone = wsutil.WebsocketRecvStream(opts.Stderr, runStderrConn)
					}
				}

				// Track run I/O completion.
				wgIO.Add(1)
				go func() {
					defer wgIO.Done()
					<-runStdinDone
					<-runStdoutDone
					if runStderrDone != nil {
						<-runStderrDone
					}
					_ = runStdioConn.Close()
					if runStderrConn != nil {
						_ = runStderrConn.Close()
					}
					// Close the run control websocket after I/O is done.
					process.controlMu.Lock()
					conn := process.controlConn
					process.controlConn = nil
					process.controlMu.Unlock()
					if conn != nil {
						_ = conn.Close()
					}
				}()
			}
		}
	}

	// Close writesDone when all I/O goroutines are done.
	go func() {
		wgIO.Wait()
		close(writesDone)
	}()

	return process, nil
}

// WaitStdinDone waits for the stdin forwarding to finish. This is useful
// for tests to ensure all stdin has been sent before checking results.
func (p *BuildTestProcess) WaitStdinDone() {
	if p.stdinDone != nil {
		<-p.stdinDone
	}
}

// taskGetString extracts a string value from a task's data, returning "" if not found.
func taskGetString(task *Task, key string) string {
	var s string
	if err := task.Get(key, &s); err != nil {
		return ""
	}
	return s
}

// taskGetInt extracts an int value from a task's data, returning 0 if not found.
func taskGetInt(task *Task, key string) int {
	var n int
	if err := task.Get(key, &n); err != nil {
		return 0
	}
	return n
}

// taskGetBool extracts a bool value from a task's data, returning false if not found.
func taskGetBool(task *Task, key string) bool {
	var b bool
	if err := task.Get(key, &b); err != nil {
		return false
	}
	return b
}

// createSourceTarball creates a gzipped tar archive of the given directory
// and returns a File pointing to the temporary tarball.
func createSourceTarball(sourcePath string) (*os.File, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("cannot stat source path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source path %q is not a directory", sourcePath)
	}

	tmpFile, err := os.CreateTemp("", "pebble-source-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("cannot create temp file: %w", err)
	}

	gzw := gzip.NewWriter(tmpFile)
	tw := tar.NewWriter(gzw)

	err = filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Calculate the relative path within the tarball.
		relPath, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return err
		}
		// Skip the root directory itself.
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("cannot write tar header for %q: %w", relPath, err)
		}

		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		if _, err := io.Copy(tw, f); err != nil {
			return fmt.Errorf("cannot write file %q to tarball: %w", relPath, err)
		}
		return nil
	})
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("cannot walk source directory: %w", err)
	}

	if err := tw.Close(); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("cannot close tar writer: %w", err)
	}
	if err := gzw.Close(); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("cannot close gzip writer: %w", err)
	}

	return tmpFile, nil
}
