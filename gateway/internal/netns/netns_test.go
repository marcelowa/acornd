package netns

import (
	"net"
	"strings"
	"syscall"
	"testing"
)

// The kernel parses these bytes, so the framing is the contract: a header whose length excludes
// the padding, then the payload padded to four bytes. A mistake here is a route that silently
// is not installed.
func TestAMessageIsFramedForTheKernel(t *testing.T) {
	payload := []byte{1, 2, 3}
	frame := message(rtmNewRoute, nlmFRequest|nlmFAck, payload)
	if got := native.Uint32(frame[0:]); int(got) != 16+len(payload) {
		t.Fatalf("the header should count the payload but not its padding, got %d", got)
	}
	if got := native.Uint16(frame[4:]); got != rtmNewRoute {
		t.Fatalf("type: want %d, got %d", rtmNewRoute, got)
	}
	if got := native.Uint16(frame[6:]); got != nlmFRequest|nlmFAck {
		t.Fatalf("flags: want request and acknowledgement, got %d", got)
	}
	if len(frame) != 20 {
		t.Fatalf("a three-byte payload pads to twenty, got %d", len(frame))
	}
	if got := frame[16:19]; string(got) != string(payload) {
		t.Fatalf("payload: want %v, got %v", payload, got)
	}
}

// An attribute carries its own length, which is what the kernel walks: two bytes of length, two
// of kind, then the data.
func TestAnAttributeCarriesItsLengthAndKind(t *testing.T) {
	data := []byte{10, 0, 2, 1}
	framed := attribute(rtaGateway, data)
	if got := native.Uint16(framed[0:]); got != 8 {
		t.Fatalf("length: want 8, got %d", got)
	}
	if got := native.Uint16(framed[2:]); got != rtaGateway {
		t.Fatalf("kind: want %d, got %d", rtaGateway, got)
	}
	if got := framed[4:]; string(got) != string(data) {
		t.Fatalf("data: want %v, got %v", data, got)
	}
	// Three bytes of data still pad to a boundary the parser can walk past.
	if padded := attribute(rtaDst, []byte{10, 0, 0}); len(padded) != 8 {
		t.Fatalf("a three-byte attribute is eight bytes, got %d", len(padded))
	}
}

// IPv4 travels as four bytes and IPv6 as sixteen, each with the family the kernel keys on.
func TestAnAddressKeepsItsFamily(t *testing.T) {
	family, bytes, err := familyAndBytes(net.ParseIP("10.0.2.15"))
	if err != nil {
		t.Fatal(err)
	}
	if family != syscall.AF_INET || len(bytes) != 4 {
		t.Fatalf("want IPv4 in four bytes, got family %d and %d bytes", family, len(bytes))
	}
	family, bytes, err = familyAndBytes(net.ParseIP("fd00::1"))
	if err != nil {
		t.Fatal(err)
	}
	if family != syscall.AF_INET6 || len(bytes) != 16 {
		t.Fatalf("want IPv6 in sixteen bytes, got family %d and %d bytes", family, len(bytes))
	}
}

// A route names the table a program looks at, and the type says what happens to a matching
// packet: to a gateway, or nowhere at all.
func TestARouteSaysWhichTableAndWhatHappens(t *testing.T) {
	unicast := routeMessage(syscall.AF_INET, 0, rtnUnicast)
	if unicast[4] != rtTableMain || unicast[7] != rtnUnicast {
		t.Fatalf("a default route belongs to the main table as a unicast: %v", unicast)
	}
	refused := routeMessage(syscall.AF_INET, 8, rtnUnreach)
	if refused[1] != 8 || refused[7] != rtnUnreach {
		t.Fatalf("a refused range keeps its prefix and its type: %v", refused)
	}
	// The type is the security boundary, so it is worth pinning: unreachable is seven, and the
	// kernel answers a wrong type with EINVAL (a route nobody installed) rather than a refusal.
	if rtnUnreach != 7 {
		t.Fatalf("unreachable is route type 7, not %d", rtnUnreach)
	}
}

// A mistake in a range is refused before anything is asked of the kernel, and the message says
// which range was wrong.
func TestABadRangeIsRefusedWithItsName(t *testing.T) {
	if err := Unreachable("10.0.0.0"); err == nil || !strings.Contains(err.Error(), "10.0.0.0") {
		t.Fatalf("a range without a prefix should be refused by name, got %v", err)
	}
	if err := Address("eth0", "nonsense"); err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("an address without a prefix should be refused by name, got %v", err)
	}
	if err := Up("there-is-no-such-interface"); err == nil || !strings.Contains(err.Error(), "there-is-no-such-interface") {
		t.Fatalf("a missing interface should be named, got %v", err)
	}
}
