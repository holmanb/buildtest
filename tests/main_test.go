//go:build integration

// Copyright (c) 2024 Canonical Ltd
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

package tests

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holmanb/buildtest/internals/servicelog"
)

var buildtestBin = flag.String("buildtestbin", "", "Path to the pre-built Buildtest binary")

// TestMain builds the buildtest binary of `-buildtestbin` flag is not set
// before running the integration tests.
func TestMain(m *testing.M) {
	flag.Parse()

	if *buildtestBin == "" {
		goBuild := exec.Command("go", "build", "-o", "../buildtest", "../cmd/buildtest")
		if err := goBuild.Run(); err != nil {
			fmt.Println("Cannot build buildtest binary:", err)
			os.Exit(1)
		}
		*buildtestBin = "../buildtest"
	} else {
		// Use the pre-built Buildtest binary provided by the buildtestBin flag.
		fmt.Println("Using pre-built Buildtest binary at:", *buildtestBin)
	}

	exitCode := m.Run()
	os.Exit(exitCode)
}

// createLayer creates a layer file with layerYAML under the directory "pebbleDir/layers".
func createLayer(t *testing.T, pebbleDir, layerFileName, layerYAML string) {
	t.Helper()

	layersDir := filepath.Join(pebbleDir, "layers")
	err := os.MkdirAll(layersDir, 0o755)
	if err != nil {
		t.Fatalf("Cannot create layers directory: %v", err)
	}

	layerPath := filepath.Join(layersDir, layerFileName)
	err = os.WriteFile(layerPath, []byte(layerYAML), 0o755)
	if err != nil {
		t.Fatalf("Cannot create layers file: %v", err)
	}
}

// pebbleDaemon starts the pebble daemon with optional arguments
// and returns two channels yielding the lines from standard output and
// standard error. The runOrEnter argument should be "run" or "enter".
func pebbleDaemon(t *testing.T, pebbleDir string, runOrEnter string, args ...string) (stdoutCh chan string, stderrCh chan string) {
	t.Helper()

	stdoutCh = make(chan string)
	stderrCh = make(chan string)

	cmd := exec.Command(*buildtestBin, append([]string{runOrEnter}, args...)...)
	cmd.Env = append(os.Environ(), "BUILDTEST="+pebbleDir)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("Cannot create stdout pipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("Cannot create stderr pipe: %v", err)
	}

	err = cmd.Start()
	if err != nil {
		t.Fatalf("Error starting 'pebble %s': %v", runOrEnter, err)
	}

	stopStdout := make(chan struct{})
	stopStderr := make(chan struct{})
	waitDone := make(chan struct{})

	go func() {
		cmd.Wait()
		close(stopStdout)
		close(stopStderr)
		close(waitDone)
	}()

	t.Cleanup(func() {
		select {
		case <-waitDone:
		default:
			// If Pebble hasn't exited yet, send Ctrl-C signal. Don't error if
			// it fails (in case it's just terminated).
			cmd.Process.Signal(os.Interrupt)
			<-waitDone
		}
	})

	readLines := func(reader io.Reader, ch chan string, stop <-chan struct{}) {
		defer close(ch)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			select {
			case ch <- scanner.Text():
			case <-stop:
				return
			}
		}
		// Don't check scanner.Err: may get "file already closed" errors.
	}

	go readLines(stdoutPipe, stdoutCh, stopStdout)
	go readLines(stderrPipe, stderrCh, stopStderr)

	return stdoutCh, stderrCh
}

// waitForLog waits until an expectedLog from an expectedService appears in the logs channel, or fails the test after a
// specified timeout if the expectedLog is still not found.
func waitForLog(t *testing.T, linesCh <-chan string, expectedService, expectedLog string, timeout time.Duration) {
	t.Helper()

	timeoutCh := time.After(timeout)
	for {
		select {
		case line, ok := <-linesCh:
			if !ok {
				t.Fatalf("channel closed before all expected logs were received")
			}
			log, err := servicelog.Parse([]byte(line))
			if err != nil {
				// Not a service log line (e.g. a daemon log line); fall
				// back to a plain substring match.
				if expectedService == "buildtest" && strings.Contains(line, expectedLog) {
					return
				}
				continue
			}

			if log.Service == expectedService && strings.Contains(log.Message, expectedLog) {
				return
			}

		case <-timeoutCh:
			t.Fatalf("timed out after %v waiting for log %s", 3*time.Second, expectedLog)
		}
	}
}

// waitForText waits until an expected string appears in the textCh channel, or fails the test after a
// specified timeout if the expectedText is still not found.
func waitForText(t *testing.T, textCh <-chan string, expectedText string, timeout time.Duration) {
	t.Helper()

	timeoutCh := time.After(timeout)
	for {
		select {
		case text, ok := <-textCh:
			if !ok {
				t.Fatal("channel closed before expected text was received")
				return // Exit the loop if the channel is closed
			}
			if strings.Contains(text, expectedText) {
				return // Exit the loop if the expected text is found
			}

		case <-timeoutCh:
			t.Fatalf("timed out after %v waiting for text: %q", timeout, expectedText)
		}
	}
}

// waitForFile waits until a file exists, or fails the test after a specified timeout
// if the file still doesn't exist.
func waitForFile(t *testing.T, file string, timeout time.Duration) {
	t.Helper()

	timeoutCh := time.After(timeout)
	ticker := time.NewTicker(time.Millisecond)
	for {
		select {
		case <-timeoutCh:
			t.Fatalf("timeout waiting for file %s", file)

		case <-ticker.C:
			stat, err := os.Stat(file)
			if err == nil && stat.Mode().IsRegular() {
				return
			}
		}
	}
}

// waitForTermination drains the given channel to wait for the command to
// terminate within a given time.
func waitForTermination(t *testing.T, ch <-chan string, timeout time.Duration) {
	timeoutCh := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				// Channel closed, program exited.
				return
			}
		case <-timeoutCh:
			t.Fatal("timed out waiting for pebble to terminate (channel to drain)")
		}
	}
}

// runPebbleCommand runs a pebble command and returns the standard output.
func runPebbleCommand(t *testing.T, pebbleDir string, args ...string) string {
	t.Helper()

	cmd := exec.Command(*buildtestBin, args...)
	cmd.Env = append(os.Environ(), "BUILDTEST="+pebbleDir)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("error executing pebble command: %v\nOutput:\n%s", err, output)
	}

	return string(output)
}
