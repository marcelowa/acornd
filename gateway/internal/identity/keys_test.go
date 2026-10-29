package identity

import (
	"testing"
	"time"
)

// The client this package makes for itself has a timeout: a publisher that accepts the connection and then says
// nothing must not hold the door open forever, and a zero timeout is the shape that does.
func TestTheDefaultKeyClientHasATimeout(t *testing.T) {
	if got := defaultClient().Timeout; got != 10*time.Second {
		t.Errorf("the default client's timeout is %s, want 10s", got)
	}
}
