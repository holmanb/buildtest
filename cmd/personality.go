// Copyright (c) 2014-2024 Canonical Ltd
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

package cmd

// ProgramName represents the name of the application binary.
var ProgramName string = "buildtest"

// DisplayName represents the user-facing name of the application.
var DisplayName string = "Buildtest"

// DefaultDir is the Buildtest directory used if $BUILDTEST is not set.
var DefaultDir string = "/var/lib/buildtest/default"

// StateFile is the file name of the state file in the buildtest dir.
var StateFile string = ".buildtest.state"
