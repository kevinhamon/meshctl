package mesh

import (
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path by way of a temp file in the same
// directory plus a rename, so a reader never sees a truncated file and a crash
// mid-write leaves the previous contents intact. Every mesh state file that is
// read-modify-written (request frontmatter, claims, presence, the dispatch
// count, doctrine stamps) goes through here; a plain os.WriteFile is only for
// files nothing else reads concurrently.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
