package sandbox

import (
	"os"
	"sort"
)

// ReadonlyGlobals is the machine's global resources that exist: a path the policy names but this
// machine does not have is a mount that would fail, not a resource to mount.
func ReadonlyGlobals(policy Policy) []string {
	var readonly []string
	for _, path := range policy.Global {
		if _, err := os.Stat(path); err == nil {
			readonly = append(readonly, path)
		}
	}
	sort.Strings(readonly)
	return readonly
}
