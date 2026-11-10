package microsandbox

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// kvmParameterEnabled reads one `nested` parameter the way its module writes it: kvm_intel
// prints a bool (Y/N), kvm_amd prints an int (1/0). Anything else is unknown.
func kvmParameterEnabled(value string) (enabled, known bool) {
	switch strings.TrimSpace(value) {
	case "Y", "y", "1":
		return true, true
	case "N", "n", "0":
		return false, true
	}
	return false, false
}

// nestedFromKVMParameters decides from what /sys/module/kvm_intel/parameters/nested and
// kvm_amd's say. An empty string is a file that is not there.
func nestedFromKVMParameters(intel, amd string) sandbox.NestingSupport {
	var disabled []string
	for _, parameter := range []struct{ module, value string }{
		{"kvm_intel", intel},
		{"kvm_amd", amd},
	} {
		enabled, known := kvmParameterEnabled(parameter.value)
		if !known {
			continue
		}
		if enabled {
			return sandbox.NestingSupport{Available: true}
		}
		disabled = append(disabled, parameter.module)
	}
	if len(disabled) > 0 {
		return sandbox.Unavailable(
			"the host kernel has nested virtualization off (%s.nested=0): turn it on and restart",
			strings.Join(disabled, ", "),
		)
	}
	return sandbox.Unavailable("the host kernel does not report nested virtualization support")
}

// appleNestedPattern matches the chips that can nest: Apple's hardware-assisted nested
// virtualization (FEAT_NV2) arrived with the M3.
var appleNestedPattern = regexp.MustCompile(`^Apple M(\d+)`)

// nestedFromApple decides from the CPU brand and the OS version: nested virtualization needs
// an Apple Silicon M3 or later, running macOS 15 (Sequoia) or later.
func nestedFromApple(brand, version string) sandbox.NestingSupport {
	chip := appleNestedPattern.FindStringSubmatch(strings.TrimSpace(brand))
	if chip == nil {
		return sandbox.Unavailable("nested virtualization needs an Apple Silicon M3 or later, and this machine reports %q", strings.TrimSpace(brand))
	}
	generation, err := strconv.Atoi(chip[1])
	if err != nil || generation < 3 {
		return sandbox.Unavailable("nested virtualization needs an Apple Silicon M3 or later")
	}
	major, err := strconv.Atoi(strings.SplitN(strings.TrimSpace(version), ".", 2)[0])
	if err != nil {
		return sandbox.Unavailable("cannot tell the macOS version from %q; nested virtualization needs macOS 15 or later", version)
	}
	if major < 15 {
		return sandbox.Unavailable("nested virtualization needs macOS 15 or later, and this machine reports %s", version)
	}
	return sandbox.NestingSupport{Available: true}
}
