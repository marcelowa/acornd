package sandbox

import (
	"sync"
	"testing"
)

// Parallel end-to-end tests each call a driver package's Register, and Engine reads the table
// while they do. Without a lock the runtime stops the program with "concurrent map writes", or
// -race reports it against whichever tests happened to be running.
func TestRegisteringADriverFromManyGoroutinesIsSafe(t *testing.T) {
	const goroutines, rounds = 64, 2000
	factory := func(_, _ string) Driver { return nil }

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range rounds {
				RegisterDriver("registry-race-test", factory)
			}
		}()
		go func() {
			defer wg.Done()
			for range rounds {
				_, _ = Engine("", "", "registry-race-test")
			}
		}()
	}
	wg.Wait()

	if _, err := Engine("", "", "registry-race-test"); err != nil {
		t.Errorf("a registered engine should be found: %v", err)
	}
}
