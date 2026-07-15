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

package buildteststate_test

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"

	. "gopkg.in/check.v1"

	"github.com/canonical/pebble/internals/overlord/buildteststate"
)

type tarballSuite struct{}

var _ = Suite(&tarballSuite{})

func (s *tarballSuite) TestExtractTarballBasic(c *C) {
	destDir := c.MkDir()
	tarballPath := filepath.Join(c.MkDir(), "test.tar.gz")

	err := createTarball(tarballPath, map[string]string{
		"hello.txt":         "hello world",
		"subdir/nested.txt": "nested content",
	})
	c.Assert(err, IsNil)

	err = buildteststate.ExtractTarball(tarballPath, destDir)
	c.Assert(err, IsNil)

	content, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
	c.Assert(err, IsNil)
	c.Check(string(content), Equals, "hello world")

	content, err = os.ReadFile(filepath.Join(destDir, "subdir", "nested.txt"))
	c.Assert(err, IsNil)
	c.Check(string(content), Equals, "nested content")
}

func (s *tarballSuite) TestExtractTarballSymlink(c *C) {
	destDir := c.MkDir()
	tarballPath := filepath.Join(c.MkDir(), "test.tar.gz")

	err := createTarballWithSymlinks(tarballPath,
		map[string]string{"target.txt": "target content"},
		map[string]string{"link.txt": "target.txt"},
	)
	c.Assert(err, IsNil)

	err = buildteststate.ExtractTarball(tarballPath, destDir)
	c.Assert(err, IsNil)

	linkTarget, err := os.Readlink(filepath.Join(destDir, "link.txt"))
	c.Assert(err, IsNil)
	c.Check(linkTarget, Equals, "target.txt")

	content, err := os.ReadFile(filepath.Join(destDir, "target.txt"))
	c.Assert(err, IsNil)
	c.Check(string(content), Equals, "target content")
}

func (s *tarballSuite) TestExtractTarballPathTraversal(c *C) {
	destDir := c.MkDir()
	tarballPath := filepath.Join(c.MkDir(), "test.tar.gz")

	err := createTarballWithSymlinks(tarballPath,
		map[string]string{"safe.txt": "safe content"},
		map[string]string{"../escape.txt": "should be skipped"},
	)
	c.Assert(err, IsNil)

	err = buildteststate.ExtractTarball(tarballPath, destDir)
	c.Assert(err, IsNil)

	// Safe file should exist.
	content, err := os.ReadFile(filepath.Join(destDir, "safe.txt"))
	c.Assert(err, IsNil)
	c.Check(string(content), Equals, "safe content")

	// Path traversal file should NOT exist outside destDir.
	_, err = os.Stat(filepath.Join(filepath.Dir(destDir), "escape.txt"))
	c.Check(os.IsNotExist(err), Equals, true)
}

func (s *tarballSuite) TestExtractTarballAbsolutePath(c *C) {
	destDir := c.MkDir()
	tarballPath := filepath.Join(c.MkDir(), "test.tar.gz")

	// Create a tarball with an absolute path entry.
	err := createTarballWithAbsPath(tarballPath,
		map[string]string{"safe.txt": "safe content"},
		"/etc/passwd",
	)
	c.Assert(err, IsNil)

	err = buildteststate.ExtractTarball(tarballPath, destDir)
	c.Assert(err, IsNil)

	// Safe file should exist.
	content, err := os.ReadFile(filepath.Join(destDir, "safe.txt"))
	c.Assert(err, IsNil)
	c.Check(string(content), Equals, "safe content")

	// Absolute path file should NOT be extracted.
	_, err = os.Stat(filepath.Join(destDir, "etc", "passwd"))
	c.Check(os.IsNotExist(err), Equals, true)
}

func (s *tarballSuite) TestExtractTarballInvalidGzip(c *C) {
	destDir := c.MkDir()
	tarballPath := filepath.Join(c.MkDir(), "test.tar.gz")

	err := os.WriteFile(tarballPath, []byte("this is not a gzip file"), 0o644)
	c.Assert(err, IsNil)

	err = buildteststate.ExtractTarball(tarballPath, destDir)
	c.Check(err, NotNil)
}

func (s *tarballSuite) TestExtractTarballMissingFile(c *C) {
	destDir := c.MkDir()
	tarballPath := filepath.Join(c.MkDir(), "nonexistent.tar.gz")

	err := buildteststate.ExtractTarball(tarballPath, destDir)
	c.Check(err, NotNil)
}

// createTarballWithSymlinks creates a gzipped tar archive with regular files and symlinks.
func createTarballWithSymlinks(path string, files map[string]string, symlinks map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gzw := gzip.NewWriter(f)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return err
		}
	}

	for name, target := range symlinks {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o777,
			Typeflag: tar.TypeSymlink,
			Linkname: target,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
	}
	return nil
}

// createTarballWithAbsPath creates a tarball with regular files plus an entry
// with an absolute path (which should be skipped during extraction).
func createTarballWithAbsPath(path string, files map[string]string, absPath string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gzw := gzip.NewWriter(f)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return err
		}
	}

	// Add absolute path entry.
	hdr := &tar.Header{
		Name: absPath,
		Mode: 0o644,
		Size: 0,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}

	return nil
}
