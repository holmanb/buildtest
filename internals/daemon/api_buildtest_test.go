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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"

	. "gopkg.in/check.v1"
)

func (s *apiSuite) TestPostBuildTestBadContentType(c *C) {
	req, err := http.NewRequest("POST", "/v1/build-test", bytes.NewBufferString("data"))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler := v1PostBuildTest(apiCmd("/v1/build-test"), req, nil)
	handler.ServeHTTP(rec, req)

	c.Check(rec.Code, Equals, http.StatusBadRequest)
	var rsp resp
	c.Assert(json.NewDecoder(rec.Body).Decode(&rsp), IsNil)
	c.Check(rsp.Status, Equals, 400)
	c.Check(rsp.Result, NotNil)
	result := rsp.Result.(map[string]any)
	c.Check(result["message"], Matches, "must use multipart/form-data.*")
}

func (s *apiSuite) TestPostBuildTestInvalidBoundary(c *C) {
	req, err := http.NewRequest("POST", "/v1/build-test", bytes.NewBufferString("data"))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=short")

	rec := httptest.NewRecorder()
	handler := v1PostBuildTest(apiCmd("/v1/build-test"), req, nil)
	handler.ServeHTTP(rec, req)

	c.Check(rec.Code, Equals, http.StatusBadRequest)
	var rsp resp
	c.Assert(json.NewDecoder(rec.Body).Decode(&rsp), IsNil)
	c.Check(rsp.Status, Equals, 400)
	c.Check(rsp.Result, NotNil)
	result := rsp.Result.(map[string]any)
	c.Check(result["message"], Matches, "invalid boundary.*")
}

func (s *apiSuite) TestPostBuildTestMissingMetadata(c *C) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	// Only write a source part, no metadata part.
	sourcePart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/gzip"},
		"Content-Disposition": {`form-data; name="source"; filename="source.tar.gz"`},
	})
	c.Assert(err, IsNil)
	_, err = io.Copy(sourcePart, bytes.NewBufferString("fake tarball"))
	c.Assert(err, IsNil)
	mw.Close()

	req, err := http.NewRequest("POST", "/v1/build-test", body)
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rec := httptest.NewRecorder()
	handler := v1PostBuildTest(apiCmd("/v1/build-test"), req, nil)
	handler.ServeHTTP(rec, req)

	c.Check(rec.Code, Equals, http.StatusBadRequest)
}

func (s *apiSuite) TestPostBuildTestMissingSource(c *C) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	// Write metadata part only.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/json"},
		"Content-Disposition": {`form-data; name="request"`},
	})
	c.Assert(err, IsNil)
	err = json.NewEncoder(part).Encode(&buildTestMetadata{})
	c.Assert(err, IsNil)
	mw.Close()

	req, err := http.NewRequest("POST", "/v1/build-test", body)
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rec := httptest.NewRecorder()
	handler := v1PostBuildTest(apiCmd("/v1/build-test"), req, nil)
	handler.ServeHTTP(rec, req)

	c.Check(rec.Code, Equals, http.StatusBadRequest)
}

func (s *apiSuite) TestPostBuildTestInvalidTimeout(c *C) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	// Write metadata with invalid timeout.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/json"},
		"Content-Disposition": {`form-data; name="request"`},
	})
	c.Assert(err, IsNil)
	err = json.NewEncoder(part).Encode(&buildTestMetadata{Timeout: "not-a-duration"})
	c.Assert(err, IsNil)

	// Write source part.
	sourcePart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/gzip"},
		"Content-Disposition": {`form-data; name="source"; filename="source.tar.gz"`},
	})
	c.Assert(err, IsNil)
	_, err = io.Copy(sourcePart, bytes.NewBufferString("fake tarball"))
	c.Assert(err, IsNil)
	mw.Close()

	req, err := http.NewRequest("POST", "/v1/build-test", body)
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rec := httptest.NewRecorder()
	handler := v1PostBuildTest(apiCmd("/v1/build-test"), req, nil)
	handler.ServeHTTP(rec, req)

	c.Check(rec.Code, Equals, http.StatusBadRequest)
	var rsp resp
	c.Assert(json.NewDecoder(rec.Body).Decode(&rsp), IsNil)
	c.Check(rsp.Status, Equals, 400)
	c.Check(rsp.Result, NotNil)
	result := rsp.Result.(map[string]any)
	c.Check(result["message"], Matches, "invalid timeout.*")
}

func (s *apiSuite) TestPostBuildTestSuccess(c *C) {
	d := s.daemon(c)
	s.startOverlord()

	// Create a minimal tarball.
	tarballData := createTestTarballData(c, map[string]string{"hello.txt": "hello"})

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	// Write metadata part.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/json"},
		"Content-Disposition": {`form-data; name="request"`},
	})
	c.Assert(err, IsNil)
	err = json.NewEncoder(part).Encode(&buildTestMetadata{})
	c.Assert(err, IsNil)

	// Write source part.
	sourcePart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/gzip"},
		"Content-Disposition": {`form-data; name="source"; filename="source.tar.gz"`},
	})
	c.Assert(err, IsNil)
	_, err = io.Copy(sourcePart, bytes.NewReader(tarballData))
	c.Assert(err, IsNil)
	mw.Close()

	req, err := http.NewRequest("POST", "/v1/build-test", body)
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	cmd := apiCmd("/v1/build-test")
	cmd.d = d

	rec := httptest.NewRecorder()
	handler := v1PostBuildTest(cmd, req, nil)
	handler.ServeHTTP(rec, req)

	c.Check(rec.Code, Equals, http.StatusAccepted)

	// Parse the async response.
	var rsp resp
	c.Assert(json.NewDecoder(rec.Body).Decode(&rsp), IsNil)
	c.Check(rsp.Type, Equals, ResponseTypeAsync)
	c.Check(rsp.Change, Not(Equals), "")

	// Verify the change was created in state.
	st := d.overlord.State()
	st.Lock()
	chg := st.Change(rsp.Change)
	c.Assert(chg, NotNil)
	c.Check(chg.Kind(), Equals, "build-test")
	st.Unlock()
}

func (s *apiSuite) TestPostBuildTestWithTimeout(c *C) {
	d := s.daemon(c)
	s.startOverlord()

	tarballData := createTestTarballData(c, map[string]string{"hello.txt": "hello"})

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	// Write metadata with timeout.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/json"},
		"Content-Disposition": {`form-data; name="request"`},
	})
	c.Assert(err, IsNil)
	err = json.NewEncoder(part).Encode(&buildTestMetadata{Timeout: "5m"})
	c.Assert(err, IsNil)

	// Write source part.
	sourcePart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/gzip"},
		"Content-Disposition": {`form-data; name="source"; filename="source.tar.gz"`},
	})
	c.Assert(err, IsNil)
	_, err = io.Copy(sourcePart, bytes.NewReader(tarballData))
	c.Assert(err, IsNil)
	mw.Close()

	req, err := http.NewRequest("POST", "/v1/build-test", body)
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	cmd := apiCmd("/v1/build-test")
	cmd.d = d

	rec := httptest.NewRecorder()
	handler := v1PostBuildTest(cmd, req, nil)
	handler.ServeHTTP(rec, req)

	c.Check(rec.Code, Equals, http.StatusAccepted)
}

// createTestTarballData creates a minimal .tar.gz as a byte slice.
func createTestTarballData(c *C, files map[string]string) []byte {
	buf := &bytes.Buffer{}
	gzw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gzw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		err := tw.WriteHeader(hdr)
		c.Assert(err, IsNil)
		_, err = tw.Write([]byte(content))
		c.Assert(err, IsNil)
	}

	err := tw.Close()
	c.Assert(err, IsNil)
	err = gzw.Close()
	c.Assert(err, IsNil)

	return buf.Bytes()
}

func (s *apiSuite) TestPostBuildTestInteractive(c *C) {
	d := s.daemon(c)
	s.startOverlord()

	tarballData := createTestTarballData(c, map[string]string{"hello.txt": "hello"})

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	// Write metadata with interactive and terminal flags.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/json"},
		"Content-Disposition": {`form-data; name="request"`},
	})
	c.Assert(err, IsNil)
	err = json.NewEncoder(part).Encode(&buildTestMetadata{Interactive: true, Terminal: true})
	c.Assert(err, IsNil)

	// Write source part.
	sourcePart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"application/gzip"},
		"Content-Disposition": {`form-data; name="source"; filename="source.tar.gz"`},
	})
	c.Assert(err, IsNil)
	_, err = io.Copy(sourcePart, bytes.NewReader(tarballData))
	c.Assert(err, IsNil)
	mw.Close()

	req, err := http.NewRequest("POST", "/v1/build-test", body)
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	cmd := apiCmd("/v1/build-test")
	cmd.d = d

	rec := httptest.NewRecorder()
	handler := v1PostBuildTest(cmd, req, nil)
	handler.ServeHTTP(rec, req)

	c.Check(rec.Code, Equals, http.StatusAccepted)
	var rsp resp
	c.Assert(json.NewDecoder(rec.Body).Decode(&rsp), IsNil)
	c.Check(rsp.Type, Equals, ResponseTypeAsync)
	c.Check(rsp.Change, Not(Equals), "")
}
