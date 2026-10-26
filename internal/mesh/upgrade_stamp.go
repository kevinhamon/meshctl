package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kevinhamon/meshctl/internal/selfupdate"
)

// upgrade_stamp.go — which meshctl last upgraded the pool, so a newer
// installed binary can say the pool is behind (mesh-console ticket 06,
// decision C6: shown in the console and doctor, not in agents' standing
// orders). Local and ignored: it records what ran on this machine.

const upgradeStampFile = "upgraded.json"

// UpgradeStamp records a completed `meshctl pool upgrade`.
type UpgradeStamp struct {
	Version string `json:"version"`
	At      string `json:"at"` // RFC 3339, UTC
}

// UpgradeStampPath is <pool dir>/.memory/upgraded.json.
func UpgradeStampPath(poolRoot string) string {
	return filepath.Join(PoolDir(poolRoot), MemoryDir, upgradeStampFile)
}

// WriteUpgradeStamp records that version upgraded the pool now.
func WriteUpgradeStamp(poolRoot, version string, now time.Time) error {
	p := UpgradeStampPath(poolRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(UpgradeStamp{Version: version, At: now.UTC().Format(time.RFC3339)})
	return writeFileAtomic(p, append(b, '\n'), 0o644)
}

// ReadUpgradeStamp reads the stamp; false when there is none.
func ReadUpgradeStamp(poolRoot string) (UpgradeStamp, bool) {
	b, err := os.ReadFile(UpgradeStampPath(poolRoot))
	if err != nil {
		return UpgradeStamp{}, false
	}
	var s UpgradeStamp
	if json.Unmarshal(b, &s) != nil || s.Version == "" {
		return UpgradeStamp{}, false
	}
	return s, true
}

// UpgradeNotice says the pool is behind when the running meshctl is a newer
// release than the one that last upgraded it; "" otherwise (no stamp yet, a
// development build, or current). Local only: no network.
func UpgradeNotice(poolRoot, running string) string {
	s, ok := ReadUpgradeStamp(poolRoot)
	if !ok || !selfupdate.Newer(running, s.Version) {
		return ""
	}
	when := s.At
	if t, err := time.Parse(time.RFC3339, s.At); err == nil {
		when = t.Local().Format("2006-01-02")
	}
	return fmt.Sprintf("meshctl %s is installed, but this pool was last upgraded with %s (%s): agents keep the older "+
		"standing orders and wiring until `meshctl pool upgrade` runs. Ask whoever maintains the pool to run it, then "+
		"restart open agent sessions.", running, s.Version, when)
}
