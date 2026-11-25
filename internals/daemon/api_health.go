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

package daemon

import (
	"net/http"
)

type healthInfo struct {
	Healthy bool `json:"healthy"`
}

// v1Health reports the daemon's health. With the service-management
// surface removed, the daemon is healthy whenever it is up and serving
// requests.
func v1Health(c *Command, r *http.Request, _ *UserState) Response {
	return SyncResponse(healthInfo{Healthy: true})
}
