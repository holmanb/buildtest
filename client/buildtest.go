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
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BuildTestOptions holds the options for a build-test request.
type BuildTestOptions struct {
	// SourcePath is the local path to the source directory to be built and tested.
	// The directory will be tar'd and uploaded to the server.
	SourcePath string

	// Timeout is the optional overall timeout for the operation.
	Timeout time.Duration
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

	// Capture the header before closing.
	header := body.String()
	contentType := mw.FormDataContentType()
	mw.Close()
	footer := body.String()

	// Send the async request.
	resp, err := client.Requester().Do(context.Background(), &RequestOptions{
		Type:    AsyncRequest,
		Method:  "POST",
		Path:    "/v1/build-test",
		Headers: map[string]string{"Content-Type": contentType},
		Body:    io.MultiReader(strings.NewReader(header), strings.NewReader(footer)),
	})
	if err != nil {
		return nil, err
	}

	// Wait for the change to complete.
	waitOpts := &WaitChangeOptions{}
	if opts.Timeout != 0 {
		waitOpts.Timeout = opts.Timeout + 30*time.Second // extra buffer beyond command timeout
	}
	change, err := client.WaitChange(resp.ChangeID, waitOpts)
	if err != nil {
		return nil, fmt.Errorf("cannot wait for build-test to complete: %w", err)
	}
	if change.Err != "" {
		return nil, fmt.Errorf("build-test change failed: %s", change.Err)
	}

	// Extract results from the change's tasks.
	result := &BuildTestResult{}
	for _, task := range change.Tasks {
		var apiData map[string]any
		if err := task.Get("api-data", &apiData); err != nil {
			continue
		}

		switch task.Kind {
		case "build-test-build":
			result.Build = BuildResult{
				Stdout:   jsonString(apiData, "stdout"),
				Stderr:   jsonString(apiData, "stderr"),
				ExitCode: jsonInt(apiData, "exit-code"),
			}
		case "build-test-run":
			result.Run = &RunResult{
				Stdout:   jsonString(apiData, "stdout"),
				Stderr:   jsonString(apiData, "stderr"),
				ExitCode: jsonInt(apiData, "exit-code"),
			}
		}
	}

	return result, nil
}

// jsonString extracts a string value from a map[string]any, returning "" if not found.
func jsonString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

// jsonInt extracts an int value from a map[string]any, returning 0 if not found.
func jsonInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
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
