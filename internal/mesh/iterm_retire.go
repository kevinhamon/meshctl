package mesh

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The optional macOS iTerm2 profile (a Taskfile that rendered .iterm2/ into
// iTerm2's DynamicProfiles) is retired: the harness names the agent itself,
// and `meshctl agent identity` covers any terminal. Onboard, and so
// `pool upgrade`, removes the files an earlier meshctl wrote — only when every
// one is byte-for-byte a version meshctl shipped. A profile someone edited or
// added images to is theirs and stays, with its Taskfile. An installed profile
// in iTerm2's own folder is never touched.

const (
	retiredTaskfile     = "Taskfile.yaml"
	retiredITermDir     = ".iterm2"
	retiredITermProfile = ".iterm2/profile.json.tmpl"
	retiredITermValues  = ".iterm2.values.yaml"
)

// Hashes of every version meshctl shipped. The values file is compared after
// normalising the per-agent fields (name, badge, colours).
var (
	shippedTaskfiles = map[string]bool{
		"104f237776f31bac293e0132f9e494072ea6989c916c865aae747d57395c158b": true,
		"a050ccc6a7901f966d02ad3fddbb245eb1f39877db438b5fccd86c105b6120db": true,
	}
	shippedITermProfiles = map[string]bool{
		"8ccdef90506d2829ddf388d33077ec117faf56a0ded7d4d913d58ba3366e50f1": true,
		"c80a91ce3dd5e188caeaf221d8619ff2011f434943f90ccaa695e3f63aa679e6": true,
	}
	shippedITermValues = map[string]bool{
		"09d303650b5ee8edabb440a88279959e677221e69ad1c1b13fa80c592b1d3e81": true,
		"ee789a31be82a213fb8d46b0fabeba18eadef80992f3ad5792de86c4ace0f15f": true,
	}
	reITermField = regexp.MustCompile(`(?m)^(  (?:name|badge|red|green|blue|alpha): ).*$`)
)

func fileSHA(path string, norm bool) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if norm {
		b = reITermField.ReplaceAllFunc(b, func(m []byte) []byte {
			sub := reITermField.FindSubmatch(m)
			field := string(sub[1])
			switch {
			case field == "  name: ":
				return []byte(field + "N")
			case field == "  badge: ":
				return []byte(field + "B")
			default:
				return []byte(field + "C")
			}
		})
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), true
}

// planITermRetire lists the retired files to remove from an agent workspace
// (paths relative to dir). Empty when there is nothing to remove or the
// profile was customised.
func planITermRetire(dir string) []string {
	var remove []string
	if dirExists(filepath.Join(dir, retiredITermDir)) || fileExists(filepath.Join(dir, retiredITermValues)) {
		entries, _ := os.ReadDir(filepath.Join(dir, retiredITermDir))
		for _, e := range entries {
			if e.Name() != filepath.Base(retiredITermProfile) {
				return nil // images or other files someone added: a custom profile
			}
		}
		if fileExists(filepath.Join(dir, retiredITermProfile)) {
			if h, _ := fileSHA(filepath.Join(dir, retiredITermProfile), false); !shippedITermProfiles[h] {
				return nil
			}
			remove = append(remove, retiredITermProfile)
		}
		if fileExists(filepath.Join(dir, retiredITermValues)) {
			if h, _ := fileSHA(filepath.Join(dir, retiredITermValues), true); !shippedITermValues[h] {
				return nil
			}
			remove = append(remove, retiredITermValues)
		}
	}
	if h, ok := fileSHA(filepath.Join(dir, retiredTaskfile), false); ok && shippedTaskfiles[h] {
		remove = append(remove, retiredTaskfile)
	}
	sort.Strings(remove)
	return remove
}

// retireITerm removes what planITermRetire lists, and the .iterm2/ folder
// once it is empty.
func retireITerm(dir string) ([]string, error) {
	remove := planITermRetire(dir)
	if len(remove) == 0 {
		return nil, nil
	}
	for _, rel := range remove {
		if err := os.Remove(filepath.Join(dir, rel)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	_ = os.Remove(filepath.Join(dir, retiredITermDir)) // only succeeds when empty
	return []string{"removed the retired iTerm2 profile files: " + strings.Join(remove, ", ")}, nil
}
