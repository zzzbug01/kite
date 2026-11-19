package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"strings"
)

// BinaryName is the file name of one kite command inside a release archive.
func BinaryName(command, goos string) string {
	if goos == "windows" && !strings.HasSuffix(command, ".exe") {
		return command + ".exe"
	}
	return command
}

// Extract returns the bytes of binary inside a .tar.gz or .zip archive.
// binary is a base name such as "kite" or "kite.exe". Paths that escape
// the archive root are rejected.
func Extract(archive []byte, binary string) ([]byte, error) {
	binary = path.Base(binary)
	if binary == "." || binary == "/" || strings.Contains(binary, "..") {
		return nil, fmt.Errorf("invalid binary name %q", binary)
	}
	if isZip(archive) {
		return extractZip(archive, binary)
	}
	return extractTar(archive, binary)
}

func isZip(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K'
}

func extractZip(archive []byte, binary string) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Clean("/" + strings.ReplaceAll(f.Name, "\\", "/"))
		if path.Base(name) != binary || strings.Contains(name, "..") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		buf, err := readLimited(rc, maxAsset)
		rc.Close()
		if err != nil {
			return nil, err
		}
		return buf, nil
	}
	return nil, fmt.Errorf("archive does not contain %s", binary)
}

func extractTar(archive []byte, binary string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errorsIsEOF(err) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := path.Clean("/" + hdr.Name)
		if path.Base(name) != binary || strings.Contains(name, "..") {
			continue
		}
		return readLimited(tr, maxAsset)
	}
	return nil, fmt.Errorf("archive does not contain %s", binary)
}

func readLimited(r io.Reader, n int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > n {
		return nil, fmt.Errorf("binary exceeds %d bytes", n)
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("binary is empty")
	}
	return buf, nil
}

func errorsIsEOF(err error) bool {
	return err == io.EOF
}
