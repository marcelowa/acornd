package sandbox

import (
	"fmt"
)

// NestingSupport says whether a sandbox on this machine could nest virtual machines — run a
// hypervisor inside the sandbox, which is what developing one looks like from the inside.
//
// It is a capability, not a policy: whether any workspace asks for nesting is the policy's
// business, and the two have to agree before the engine is told anything. Reason says why
// not when Available is false, in a sentence fit for doctor and the state frame.
type NestingSupport struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// Unavailable is the shape every refusal takes.
func Unavailable(reason string, args ...any) NestingSupport {
	return NestingSupport{Reason: fmt.Sprintf(reason, args...)}
}
