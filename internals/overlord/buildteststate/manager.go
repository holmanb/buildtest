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
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/tomb.v2"

	"github.com/canonical/pebble/internals/logger"
	"github.com/canonical/pebble/internals/overlord/state"
)

const (
	connectTimeout   = 5 * time.Second
	handshakeTimeout = 5 * time.Second
)

// Websocket IDs for build-test streaming output.
const (
	WsBuildStdout = "build-stdout"
	WsBuildStderr = "build-stderr"
	WsRunStdout   = "run-stdout"
	WsRunStderr   = "run-stderr"
)

var websocketUpgrader = websocket.Upgrader{
	CheckOrigin:      func(r *http.Request) bool { return true },
	HandshakeTimeout: handshakeTimeout,
}

// BuildTestManager manages build-test tasks.
type BuildTestManager struct {
	pebbleDir string

	executions     map[string]*buildTestExecution
	executionsCond *sync.Cond
}

// buildTestExecution tracks the execution of a build-test task, including
// websocket connections for streaming output.
type buildTestExecution struct {
	taskKind string // "build-test-build" or "build-test-run"

	websockets     map[string]*websocket.Conn
	websocketsLock sync.Mutex
	ioConnected    chan struct{}
}

// NewManager creates a new BuildTestManager.
func NewManager(pebbleDir string, runner *state.TaskRunner) *BuildTestManager {
	manager := &BuildTestManager{
		pebbleDir:      pebbleDir,
		executions:     make(map[string]*buildTestExecution),
		executionsCond: sync.NewCond(&sync.Mutex{}),
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

// registerExecution creates a buildTestExecution for the given task and
// registers it on the manager so that Connect can find it.
func (m *BuildTestManager) registerExecution(taskID, taskKind string, wsIDs []string) *buildTestExecution {
	e := &buildTestExecution{
		taskKind:    taskKind,
		websockets:  make(map[string]*websocket.Conn),
		ioConnected: make(chan struct{}),
	}
	// Populate expected websocket IDs with nil connections until connected.
	for _, id := range wsIDs {
		e.websockets[id] = nil
	}

	m.executionsCond.L.Lock()
	m.executions[taskID] = e
	m.executionsCond.Broadcast()
	m.executionsCond.L.Unlock()

	return e
}

// unregisterExecution removes the buildTestExecution for the given task.
func (m *BuildTestManager) unregisterExecution(taskID string) {
	m.executionsCond.L.Lock()
	delete(m.executions, taskID)
	m.executionsCond.L.Unlock()
}

// Connect upgrades the HTTP connection to a websocket and connects to the
// given websocket ID for the specified task's execution. This is called by
// the daemon's v1GetTaskWebsocket handler.
func (m *BuildTestManager) Connect(r *http.Request, w http.ResponseWriter, task *state.Task, websocketID string) error {
	stopWait := make(chan struct{})
	defer func() {
		m.executionsCond.L.Lock()
		close(stopWait)
		m.executionsCond.Broadcast()
		m.executionsCond.L.Unlock()
	}()

	executionCh := make(chan *buildTestExecution, 1)
	go func() {
		e := m.waitExecution(task.ID(), stopWait)
		if e != nil {
			executionCh <- e
		}
	}()

	st := task.State()
	st.Lock()
	change := task.Change()
	st.Unlock()

	// Wait till the execution object is ready or the request is cancelled.
	select {
	case e := <-executionCh:
		return e.connect(r, w, websocketID)
	case <-r.Context().Done():
		return r.Context().Err()
	case <-change.Ready():
		st.Lock()
		defer st.Unlock()
		return change.Err()
	}
}

func (m *BuildTestManager) waitExecution(taskID string, stop <-chan struct{}) *buildTestExecution {
	m.executionsCond.L.Lock()
	defer m.executionsCond.L.Unlock()

	for {
		select {
		case <-stop:
			return nil
		default:
		}

		e := m.executions[taskID]
		if e != nil {
			return e
		}
		m.executionsCond.Wait()
	}
}

// connect upgrades the HTTP connection to a websocket and stores it in the
// execution's websocket map.
func (e *buildTestExecution) connect(r *http.Request, w http.ResponseWriter, websocketID string) error {
	e.websocketsLock.Lock()
	conn, ok := e.websockets[websocketID]
	e.websocketsLock.Unlock()
	if !ok {
		return os.ErrNotExist
	}
	if conn != nil {
		return errors.New(websocketID + " websocket already connected")
	}

	conn, err := websocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}

	e.websocketsLock.Lock()
	defer e.websocketsLock.Unlock()
	e.websockets[websocketID] = conn

	// Check if all expected websockets are now connected.
	allConnected := true
	for _, c := range e.websockets {
		if c == nil {
			allConnected = false
			break
		}
	}
	if allConnected {
		// Only close ioConnected once.
		select {
		case <-e.ioConnected:
			// Already closed.
		default:
			close(e.ioConnected)
		}
	}

	return nil
}

// getWebsocket returns the websocket connection for the given ID.
func (e *buildTestExecution) getWebsocket(key string) *websocket.Conn {
	e.websocketsLock.Lock()
	defer e.websocketsLock.Unlock()
	return e.websockets[key]
}

// waitIOConnected waits till all the I/O websockets are connected or the
// connect timeout elapses (or the provided ctx is cancelled).
func (e *buildTestExecution) waitIOConnected(ctx context.Context, taskID string) error {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			logger.Noticef("Build-test %s: timeout waiting for websocket connections", taskID)
			return fmt.Errorf("build-test %s: timeout waiting for websocket connections: %w", taskID, ctx.Err())
		}
		return ctx.Err()
	case <-e.ioConnected:
		return nil
	}
}
