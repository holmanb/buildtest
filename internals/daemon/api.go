// Copyright (c) 2014-2020 Canonical Ltd
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

package daemon

import (
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/holmanb/buildtest/internals/overlord/restart"
	"github.com/holmanb/buildtest/internals/overlord/state"
)

var API = []*Command{{
	Path:       "/v1/system-info",
	ReadAccess: OpenAccess{},
	GET:        v1SystemInfo,
}, {
	Path:       "/v1/health",
	ReadAccess: OpenAccess{},
	GET:        v1Health,
}, {
	Path:       "/v1/changes",
	ReadAccess: OpenAccess{},
	GET:        v1GetChanges,
}, {
	Path:        "/v1/changes/{id}",
	ReadAccess:  OpenAccess{},
	WriteAccess: OpenAccess{},
	GET:         v1GetChange,
	POST:        v1PostChange,
}, {
	Path:       "/v1/changes/{id}/wait",
	ReadAccess: OpenAccess{},
	GET:        v1GetChangeWait,
}, {
	Path:       "/v1/tasks/{task-id}/websocket/{websocket-id}",
	ReadAccess: OpenAccess{},
	GET:        v1GetTaskWebsocket,
}, {
	Path:        "/v1/build-test",
	WriteAccess: OpenAccess{},
	ReadAccess:  OpenAccess{},
	POST:        v1PostBuildTest,
}}

var (
	stateEnsureBefore = (*state.State).EnsureBefore

	muxVars = mux.Vars
)

func v1SystemInfo(c *Command, r *http.Request, _ *UserState) Response {
	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()

	result := struct {
		BootID      string `json:"boot-id"`
		HTTPAddress string `json:"http-address,omitempty"`
		Version     string `json:"version"`
	}{
		BootID:      restart.BootID(state),
		HTTPAddress: c.d.options.HTTPAddress,
		Version:     c.d.Version,
	}
	return SyncResponse(result)
}

func userString(u *UserState) string {
	if u == nil {
		return "<unknown>"
	}
	if u.Username != "" {
		return u.Username
	}
	if u.UID != nil {
		return strconv.Itoa(int(*u.UID))
	}
	return "<unknown>"
}
