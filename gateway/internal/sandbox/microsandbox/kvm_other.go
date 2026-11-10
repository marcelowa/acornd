//go:build !linux

package microsandbox

// kvmHint is empty where there is no /dev/kvm to explain.
func kvmHint() string { return "" }
