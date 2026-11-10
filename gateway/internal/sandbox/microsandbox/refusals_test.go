package microsandbox

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRestartHintReadsWhatTheEngineSaid(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("this change needs a restart"), true},
		{errors.New("no changes were applied"), true},
		{errors.New("RESTART REQUIRED"), true},
		{errors.New("sandbox not found"), false},
		{errors.New(""), false},
	}
	for _, test := range cases {
		if got := restartHint(test.err); got != test.want {
			t.Errorf("restartHint(%q) = %v, want %v", test.err, got, test.want)
		}
	}
}

func TestHostsCompareInTheEnginesVocabulary(t *testing.T) {
	pattern := func(shape map[string]string) json.RawMessage {
		encoded, err := json.Marshal(shape)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	cases := []struct {
		name string
		raw  []json.RawMessage
		want []string
		same bool
	}{
		{"exact, in any case", []json.RawMessage{pattern(map[string]string{"exact": "api.example.com"})}, []string{"API.example.com"}, true},
		{"a wildcard is not an exact host", []json.RawMessage{pattern(map[string]string{"wildcard": "*.example.com"})}, []string{"api.example.com"}, false},
		{"one wildcard for another", []json.RawMessage{pattern(map[string]string{"wildcard": "*.example.com"})}, []string{"*.example.com"}, true},
		{"any is a shape of its own", []json.RawMessage{pattern(map[string]string{"any": "true"})}, []string{"example.com"}, false},
		{"nothing against nothing", nil, nil, true},
		{"a pattern this build cannot read is a change", []json.RawMessage{[]byte(`"not-an-object"`)}, nil, false},
	}
	for _, test := range cases {
		if got := sameHosts(test.raw, test.want); got != test.same {
			t.Errorf("%s: sameHosts = %v, want %v", test.name, got, test.same)
		}
	}
}

// On a machine with /dev/kvm, the hint explains how to get into the group — or, when the account is
// already in it, that the bits are what is wrong. It is the message somebody reads after losing an
// afternoon, so it has to say one of the two.
func TestTheKVMHintSaysWhatToDo(t *testing.T) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("this machine has no /dev/kvm to explain")
	}
	hint := kvmHint()
	if hint == "" {
		t.Fatal("with /dev/kvm present the hint should say something")
	}
	if !strings.Contains(hint, "group") && !strings.Contains(hint, "permission bits") {
		t.Errorf("the hint should name the fix: %q", hint)
	}
}

func TestTheEngineVersionIsARelease(t *testing.T) {
	if got := EngineVersion(); !strings.Contains(got, ".") {
		t.Errorf("the engine version is a release: %q", got)
	}
}
