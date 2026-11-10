//go:build linux

package youki

import "testing"

// A dev_t is not a raw integer: the major is split across bits 8..19 and 32.., and the minor
// across bits 0..7 and 12... The masks must recover both without letting the unused high bits
// leak in, which is what the conversion guard is about.
func TestYoukiSplitsLinuxDeviceNumbers(t *testing.T) {
	const major, minor = 0x123, 0xabcde
	dev := uint64(major)<<8 | uint64(minor)&0xff | (uint64(minor)&^0xff)<<12
	if got := deviceMajor(dev); got != major {
		t.Errorf("major = %#x, want %#x", got, major)
	}
	if got := deviceMinor(dev); got != minor {
		t.Errorf("minor = %#x, want %#x", got, minor)
	}
}
