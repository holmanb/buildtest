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

package daemon

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/canonical/pebble/internals/logger"
	"github.com/canonical/pebble/internals/overlord/buildteststate"
)

type buildTestMetadata struct {
	SourcePath  string `json:"source-path,omitempty"`
	Timeout     string `json:"timeout,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Terminal    bool   `json:"terminal,omitempty"`
}

func v1PostBuildTest(c *Command, req *http.Request, user *UserState) Response {
	contentType := req.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return BadRequest("invalid Content-Type %q: %v", contentType, err)
	}
	if mediaType != "multipart/form-data" {
		return BadRequest("must use multipart/form-data content type, got %q", mediaType)
	}

	boundary := params["boundary"]
	if len(boundary) < minBoundaryLength {
		return BadRequest("invalid boundary %q", boundary)
	}

	// Read the metadata part.
	mr := multipart.NewReader(req.Body, boundary)
	part, err := mr.NextPart()
	if err != nil {
		return BadRequest("cannot read request metadata: %v", err)
	}
	if part.FormName() != "request" {
		return BadRequest(`metadata field name must be "request", got %q`, part.FormName())
	}

	var metadata buildTestMetadata
	if err := json.NewDecoder(part).Decode(&metadata); err != nil {
		return BadRequest("cannot decode request metadata: %v", err)
	}

	timeout, err := parseOptionalDuration(metadata.Timeout)
	if err != nil {
		return BadRequest("invalid timeout: %v", err)
	}

	// Read the source tarball part.
	part, err = mr.NextPart()
	if err != nil {
		return BadRequest("cannot read source tarball: %v", err)
	}
	if part.FormName() != "source" {
		return BadRequest(`field name must be "source", got %q`, part.FormName())
	}

	// Save the tarball to a temporary file.
	pebbleDir := c.d.options.Dir
	tempDir := filepath.Join(pebbleDir, "build-test")
	err = os.MkdirAll(tempDir, 0o755)
	if err != nil {
		return ServerError("cannot create temp directory: %v", err)
	}

	tarballFile, err := os.CreateTemp(tempDir, "source-*.tar.gz")
	if err != nil {
		return ServerError("cannot create temp file: %v", err)
	}
	tarballPath := tarballFile.Name()

	_, err = io.Copy(tarballFile, part)
	tarballFile.Close()
	if err != nil {
		os.Remove(tarballPath)
		return ServerError("cannot save source tarball: %v", err)
	}

	logger.SecurityWarn(logger.SecurityAuthzAdmin, userString(user)+",build-test", "Build-test request received")

	// Create the build-test tasks.
	st := c.d.overlord.State()
	st.Lock()
	defer st.Unlock()

	args := &buildteststate.BuildTestArgs{
		SourceTarball: tarballPath,
		Timeout:       timeout,
		Interactive:   metadata.Interactive,
		Terminal:      metadata.Terminal,
	}

	ts, err := buildteststate.BuildTest(st, args)
	if err != nil {
		os.Remove(tarballPath)
		return ServerError("cannot create build-test tasks: %v", err)
	}

	change := st.NewChange("build-test", "Build source and run tests")
	change.AddAll(ts)

	stateEnsureBefore(st, 0) // start it right away

	return AsyncResponse(nil, change.ID())
}
