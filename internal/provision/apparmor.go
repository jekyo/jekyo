package provision

import (
	_ "embed"
	"fmt"
	"strings"
)

// criAppArmorProfile is containerd's default container profile with its
// signal and ptrace peer patterns widened to match the stacked label a
// container actually carries under AppArmor 4+ (issue #33). containerd
// writes no file for this profile — it generates and loads it in-process,
// and skips doing so when one by that name is already loaded — so putting
// a corrected copy on disk and loading it is how the node gets fixed.
//
//go:embed files/cri-containerd.apparmor.d
var criAppArmorProfile string

// CriAppArmorPath is where the corrected profile lives on the server.
const CriAppArmorPath = "/etc/apparmor.d/cri-containerd.apparmor.d"

// criAppArmorFixCmd writes the corrected profile and loads it. Writing the
// file is idempotent; apparmor_parser -r replaces a loaded profile in
// place, so running it twice changes nothing and no container restarts.
func criAppArmorFixCmd() string {
	return fmt.Sprintf("cat > %s <<'JEKYO_AA_EOF'\n%s\nJEKYO_AA_EOF\napparmor_parser -r -W %s",
		CriAppArmorPath, strings.TrimRight(criAppArmorProfile, "\n"), CriAppArmorPath)
}
