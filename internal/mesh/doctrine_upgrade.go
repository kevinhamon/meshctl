package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// doctrineStampFile records the hash of each doctrine file as meshctl last wrote
// it. An on-disk file whose hash still matches is pristine (safe to replace); one
// that differs was edited locally and must not be clobbered by an upgrade.
const doctrineStampFile = ".emitted.json"

// upstreamSuffix names the side-by-side copy of the new embedded doctrine left
// next to a locally-edited file, for a manual merge.
const upstreamSuffix = ".upstream"

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func readDoctrineStamps(dest string) map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(dest, doctrineStampFile)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeDoctrineStamps(dest string, m map[string]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dest, doctrineStampFile), append(b, '\n'), 0o644)
}

// DoctrineUpgrade is the outcome of an upgrade.
type DoctrineUpgrade struct {
	Did       []string // files written (new or replaced)
	Conflicts []string // locally-edited files left alone; `<file>.upstream` holds the new version
}

// UpgradeDoctrine refreshes the pool's doctrine from the embedded copy (ADR-0031)
// without clobbering local edits. Per file:
//   - missing, or unchanged since meshctl wrote it → (re)written;
//   - already identical to the embedded copy → left as is;
//   - edited locally (or never stamped and different) → left alone, the new
//     version written beside it as `<file>.upstream` for a manual merge, and
//     reported as a conflict — unless force, which backs the file up to
//     `<file>.bak-<timestamp>` and overwrites it.
func UpgradeDoctrine(poolRoot string, force bool, now time.Time) (DoctrineUpgrade, error) {
	var res DoctrineUpgrade
	dest := filepath.Join(poolRoot, DoctrineDir)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return res, err
	}
	stamps := readDoctrineStamps(dest)
	entries, err := templatesFS.ReadDir("templates/doctrine")
	if err != nil {
		return res, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		want, err := templatesFS.ReadFile("templates/doctrine/" + name)
		if err != nil {
			return res, err
		}
		out := filepath.Join(dest, name)
		rel := filepath.Join(DoctrineDir, name)
		upstream := out + upstreamSuffix
		have, err := os.ReadFile(out)
		switch {
		case err != nil && !os.IsNotExist(err):
			return res, err
		case err == nil && sha(have) == sha(want):
			stamps[name] = sha(want)
			_ = os.Remove(upstream)
			continue
		case err == nil && stamps[name] != sha(have):
			// Locally edited.
			if !force {
				if err := os.WriteFile(upstream, want, 0o644); err != nil {
					return res, err
				}
				res.Conflicts = append(res.Conflicts, rel)
				continue
			}
			bak := fmt.Sprintf("%s.bak-%s", out, now.Format("20060102-150405"))
			if err := os.WriteFile(bak, have, 0o644); err != nil {
				return res, err
			}
			res.Did = append(res.Did, "backed up local edits: "+filepath.Join(DoctrineDir, filepath.Base(bak)))
		}
		if err := os.WriteFile(out, want, 0o644); err != nil {
			return res, err
		}
		stamps[name] = sha(want)
		_ = os.Remove(upstream)
		res.Did = append(res.Did, "doctrine: "+rel)
	}
	return res, writeDoctrineStamps(dest, stamps)
}

// stampEmitted records the hash of freshly emitted doctrine files (init path).
func stampEmitted(dest string, names []string) error {
	stamps := readDoctrineStamps(dest)
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dest, n))
		if err != nil {
			return err
		}
		stamps[n] = sha(b)
	}
	return writeDoctrineStamps(dest, stamps)
}
