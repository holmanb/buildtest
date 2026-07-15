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
	"time"

	"github.com/canonical/pebble/internals/wsutil"
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
}

// BuildTestResult holds the result of a build-test request.
type BuildTestResult struct {
	Build BuildResult  `json:"build" yaml:"build"`
	Run   *RunResult   `json:"run,omitempty" yaml:"run,omitempty"`
}

// BuildResult holds the result of the build step.
type BuildResult struct {
	Stdout   string `json:"stdout" yaml:"stdout"`
	Stderr   string `json:"stderr" yaml:"stderr"`
	ExitCode int    `json:"exit-code" yaml:"exit-code"`
}

// RunResult holds the result of the test run step.
type RunResult struct {
	Stdout   string `json:"stdout" yaml:"stdout"`
	Stderr   string `json:"stderr" yaml:"stderr"`
	ExitCode int    `json:"exit-code" yaml:"exit-code"`
}

type buildTestMetadataPayload struct {
	Timeout string `json:"timeout,omitempty"`
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
				Stdout:   taskGetString(task, "stdout"),
				Stderr:   taskGetString(task, "stderr"),
				ExitCode: taskGetInt(task, "exit-code"),
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
func (client *Client) buildTestStreaming(changeID string, opts *BuildTestOptions) (*BuildTestResult, error) {
	// Wait for the change to be ready enough to get task IDs.
	waitOpts := &WaitChangeOptions{}
	if opts.Timeout != 0 {
		waitOpts.Timeout = opts.Timeout + 30*time.Second
	}
	change, err := client.WaitChange(changeID, waitOpts)
	if err != nil {
		return nil, fmt.Errorf("cannot wait for build-test to complete: %w", err)
	}
	if change.Err != "" {
		return nil, errors.New(change.Err)
	}

	// Connect to websockets for each task and stream output.
	result := &BuildTestResult{}
	var writesDone []chan bool

	for _, task := range change.Tasks {
		switch task.Kind {
		case "build-test-build":
			buildStdoutDone, buildStderrDone := client.streamTaskOutput(task.ID, "build", opts.Stdout, opts.Stderr)
			if buildStdoutDone != nil {
				writesDone = append(writesDone, buildStdoutDone)
			}
			if buildStderrDone != nil {
				writesDone = append(writesDone, buildStderrDone)
			}
			result.Build = BuildResult{
				ExitCode: taskGetInt(task, "exit-code"),
			}
		case "build-test-run":
			runStdoutDone, runStderrDone := client.streamTaskOutput(task.ID, "run", opts.Stdout, opts.Stderr)
			if runStdoutDone != nil {
				writesDone = append(writesDone, runStdoutDone)
			}
			if runStderrDone != nil {
				writesDone = append(writesDone, runStderrDone)
			}
			result.Run = &RunResult{
				ExitCode: taskGetInt(task, "exit-code"),
			}
		}
	}

	// Wait for all streaming output to be flushed.
	for _, done := range writesDone {
		<-done
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
