// Copyright (C) 2024 Canonical Ltd
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

// AccessChecker checks whether a particular request is allowed.
type AccessChecker interface {
	// CheckAccess reports whether access should be granted or denied. If
	// access is granted, return nil. If access is denied, return a non-nil
	// error such as Unauthorized("access denied").
	CheckAccess(d *Daemon, r *http.Request, user *UserState) Response
}

// OpenAccess allows all incoming requests over unix domain sockets and
// HTTP, even without user credentials (or invalid credentials).
//
// With the service-management surface removed, all of Buildtest's
// endpoints are open access: the daemon is expected to run on a trusted
// network.
type OpenAccess struct{}

func (ac OpenAccess) CheckAccess(d *Daemon, r *http.Request, user *UserState) Response {
	return nil
}
