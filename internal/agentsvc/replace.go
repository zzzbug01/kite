package agentsvc

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// replaceFiles copies each src onto dst. A later failure restores every dst
// already replaced, so a two-binary upgrade does not leave one new and one old.
// cleanup deletes the backups after the caller accepts the new files.
func replaceFiles(pairs [][2]string) (cleanup func(), err error) {
	type saved struct {
		dst  string
		prev string
	}
	var done []saved
	undo := func() {
		for i := len(done) - 1; i >= 0; i-- {
			_ = os.Remove(done[i].dst)
			if done[i].prev != "" {
				_ = os.Rename(done[i].prev, done[i].dst)
			}
		}
	}
	for _, pair := range pairs {
		src, dst := pair[0], pair[1]
		if pathsEqual(src, dst) {
			continue
		}
		prev := ""
		if st, statErr := os.Stat(dst); statErr == nil && !st.IsDir() {
			prev = dst + ".prev"
			_ = os.Remove(prev)
			if err := os.Rename(dst, prev); err != nil {
				undo()
				return nil, fmt.Errorf("move %s aside: %w", dst, err)
			}
		}
		if err := copyFileContents(dst, src); err != nil {
			if prev != "" {
				_ = os.Remove(dst)
				_ = os.Rename(prev, dst)
			}
			undo()
			return nil, err
		}
		done = append(done, saved{dst: dst, prev: prev})
	}
	return func() {
		for _, item := range done {
			if item.prev != "" {
				_ = os.Remove(item.prev)
			}
		}
	}, nil
}

func copyFileContents(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	closeErr := out.Close()
	if cerr != nil || closeErr != nil {
		os.Remove(tmp)
		if cerr != nil {
			return cerr
		}
		return closeErr
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Chmod(dst, 0o755)
}
