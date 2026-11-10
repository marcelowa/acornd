package netns

import (
	"errors"
	"strings"
	"testing"
)

// A command with no program to become is a usage error before anything is configured.
func TestCommandNeedsAProgram(t *testing.T) {
	_, err := Command(nil)
	if err == nil || !errors.Is(err, ErrUsage) {
		t.Fatalf("a netns command with no program should be a usage error: %v", err)
	}
	if !strings.Contains(err.Error(), "no command") {
		t.Errorf("the refusal should say there is no command: %v", err)
	}
}

// An unknown flag is the caller's mistake, and it is refused before the namespace is touched.
func TestCommandRefusesAnUnknownFlag(t *testing.T) {
	if _, err := Command([]string{"-nonsense", "true"}); err == nil || !errors.Is(err, ErrUsage) {
		t.Fatalf("an unknown flag should be a usage error: %v", err)
	}
}

// A namespace with no address or gateway would reach nothing, and the refusal says which is
// missing rather than leaving a namespace that cannot reach anything.
func TestCommandRefusesANamespaceWithNoAddressOrGateway(t *testing.T) {
	for _, argv := range [][]string{
		{"true"},
		{"--address", "10.0.2.15/24", "true"},
		{"--gateway", "10.0.2.2", "true"},
	} {
		_, err := Command(argv)
		if err == nil || !strings.Contains(err.Error(), "no address or gateway") {
			t.Errorf("%v should be refused for its address or gateway: %v", argv, err)
		}
	}
}

// The sequence, without a namespace: the interfaces, the address, the route, then every refused
// range. The order is the contract, because the route has to be in place before the container.
type recorder struct {
	calls []string
	err   error
}

func (r *recorder) Up(name string) error {
	r.calls = append(r.calls, "up "+name)
	return r.err
}

func (r *recorder) Address(link, address string) error {
	r.calls = append(r.calls, "address "+link+" "+address)
	return r.err
}

func (r *recorder) Gateway(link, gateway string) error {
	r.calls = append(r.calls, "gateway "+link+" "+gateway)
	return r.err
}

func (r *recorder) Unreachable(range_ string) error {
	r.calls = append(r.calls, "unreachable "+range_)
	return r.err
}

func TestConfigureRefusesEveryPrivateRange(t *testing.T) {
	r := &recorder{}
	if err := configure(r, "10.0.2.15/24", "10.0.2.2", true); err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := append([]string{
		"up lo",
		"up eth0",
		"address eth0 10.0.2.15/24",
		"gateway eth0 10.0.2.2",
	}, func() []string {
		var calls []string
		for _, refused := range refusedRanges {
			calls = append(calls, "unreachable "+refused)
		}
		return calls
	}()...)
	if len(r.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", r.calls, want)
	}
	for index := range want {
		if r.calls[index] != want[index] {
			t.Errorf("call %d = %q, want %q", index, r.calls[index], want[index])
		}
	}
}

func TestConfigureLeavesAGrantedNamespaceAlone(t *testing.T) {
	r := &recorder{}
	if err := configure(r, "10.0.2.15/24", "10.0.2.2", false); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(r.calls) != 4 {
		t.Fatalf("a granted namespace should get no refused ranges, got %v", r.calls)
	}
}
