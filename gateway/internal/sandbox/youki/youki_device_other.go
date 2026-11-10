//go:build !linux

package youki

// hostDeviceEntry has nothing to describe where there is no /dev to describe: youki is a Linux
// runtime, and a machine that is not Linux refuses it in Usable rather than here.
func hostDeviceEntry(string) (ociDevice, bool) { return ociDevice{}, false }
