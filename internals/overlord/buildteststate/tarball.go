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
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/canonical/pebble/internals/logger"
)

// extractTarball extracts a gzipped tar archive to the destination directory.
func extractTarball(tarballPath, destDir string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return fmt.Errorf("cannot open tarball: %w", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("cannot create gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("cannot read tar entry: %w", err)
		}

		// Security: skip absolute paths and paths that escape the destination.
		if filepath.IsAbs(hdr.Name) {
			continue
		}
		target := filepath.Join(destDir, hdr.Name)
		if !filepath.IsLocal(hdr.Name) {
			logger.Noticef("Skipping tar entry with path traversal: %q", hdr.Name)
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("cannot create directory %q: %w", target, err)
			}
		case tar.TypeReg:
			err = os.MkdirAll(filepath.Dir(target), 0o755)
			if err != nil {
				return fmt.Errorf("cannot create parent directory for %q: %w", target, err)
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("cannot create file %q: %w", target, err)
			}
			_, err = io.Copy(outFile, tr)
			outFile.Close()
			if err != nil {
				return fmt.Errorf("cannot write file %q: %w", target, err)
			}
		case tar.TypeSymlink:
			err = os.MkdirAll(filepath.Dir(target), 0o755)
			if err != nil {
				return fmt.Errorf("cannot create parent directory for %q: %w", target, err)
			}
			err = os.Symlink(hdr.Linkname, target)
			if err != nil {
				return fmt.Errorf("cannot create symlink %q: %w", target, err)
			}
		}
	}

	return nil
}
