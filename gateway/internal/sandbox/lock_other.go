//go:build !unix

package sandbox

import "errors"

// lockFile has nothing to lock with here, and no engine that needs it runs here either.
func lockFile(string, func() error) error {
	return errors.New("sandbox: no file locking on this platform")
}
