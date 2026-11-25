//go:build integration

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

package tests

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStartup tests that the daemon starts up and logs readiness.
func TestStartup(t *testing.T) {
	pebbleDir := t.TempDir()

	_, stderrCh := pebbleDaemon(t, pebbleDir, "run")
	waitForLog(t, stderrCh, "buildtest", fmt.Sprintf(`"event":"sys_startup:%d","description":"Starting daemon"`, os.Getuid()), 3*time.Second)
	waitForLog(t, stderrCh, "buildtest", "Started daemon", 3*time.Second)
}

// TestCreateDirs tests that the daemon creates the Pebble directory on
// startup with the `--create-dirs` option.
func TestCreateDirs(t *testing.T) {
	tmpDir := t.TempDir()
	pebbleDir := filepath.Join(tmpDir, "buildtest")

	_, stderrCh := pebbleDaemon(t, pebbleDir, "run", "--create-dirs")
	waitForLog(t, stderrCh, "buildtest", "Started daemon", 3*time.Second)

	st, err := os.Stat(pebbleDir)
	if err != nil {
		t.Fatalf("pebble run --create-dirs didn't create Pebble directory: %v", err)
	}
	if !st.IsDir() {
		t.Fatalf("pebble dir %s is not a directory: %v", pebbleDir, err)
	}
}

// TestHTTPPort tests that the daemon starts the HTTP API on the given port
// with the `--http` option, and that /v1/health responds.
func TestHTTPPort(t *testing.T) {
	pebbleDir := t.TempDir()

	port := "61382"
	_, stderrCh := pebbleDaemon(t, pebbleDir, "run", "--http=:"+port)
	waitForLog(t, stderrCh, "buildtest", "Started daemon", 3*time.Second)

	resp, err := http.Get(fmt.Sprintf("http://localhost:%s/v1/health", port))
	if err != nil {
		t.Fatalf("port %s is not being listened by : %v", port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("error checking pebble /v1/health on port %s: %v", port, err)
	}
}

// TestPersistDefault tests that the daemon persists the state to the disk
// by default without setting the environment variable `BUILDTEST_PERSIST`.
func TestPersistDefault(t *testing.T) {
	pebbleDir := t.TempDir()

	_, stderrCh := pebbleDaemon(t, pebbleDir, "run")
	waitForLog(t, stderrCh, "buildtest", "Started daemon", 3*time.Second)

	_, err := os.Stat(filepath.Join(pebbleDir, ".buildtest.state"))
	if err != nil {
		t.Fatalf("pebble run without setting BUILDTEST_PERSIST didn't create the state file: %v", err)
	}
}

// TestPersistNever tests that when the environment variable `BUILDTEST_PERSIST`
// is set to "never", the daemon does not persist its state to the disk.
func TestPersistNever(t *testing.T) {
	t.Setenv("BUILDTEST_PERSIST", "never")
	pebbleDir := t.TempDir()

	_, stderrCh := pebbleDaemon(t, pebbleDir, "run")
	waitForLog(t, stderrCh, "buildtest", "Started daemon", 3*time.Second)

	_, err := os.Stat(filepath.Join(pebbleDir, ".buildtest.state"))
	if err == nil {
		t.Fatalf("pebble run with BUILDTEST_PERSIST set to 'never' still created the state file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pebble run with BUILDTEST_PERSIST set to 'never' got error other than ErrNotExist: %v", err)
	}
}

// TestChangesOverHTTP tests that the changes command works against a running
// daemon over the HTTP API.
func TestChangesOverHTTP(t *testing.T) {
	pebbleDir := t.TempDir()

	port := "61383"
	_, stderrCh := pebbleDaemon(t, pebbleDir, "run", "--http=:"+port)
	waitForLog(t, stderrCh, "buildtest", "Started daemon", 3*time.Second)

	t.Setenv("BUILDTEST_BASEURL", fmt.Sprintf("http://localhost:%s", port))
	output := runPebbleCommand(t, pebbleDir, "changes")
	if output == "" {
		t.Fatal("changes command returned no output")
	}
}
