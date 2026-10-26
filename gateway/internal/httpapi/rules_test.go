package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/vault"
)

// The rules the edge applies before it calls anything. They are refusals rather than defaults:
// a field that does nothing is worse than a request that fails, so each one is a sentence the
// caller reads instead of a workspace that quietly did not get what it asked for.

func TestAWorkspaceBodyRefusesWhatThisBuildDoesNotHave(t *testing.T) {
	text := func(value string) *string { return &value }
	boolean := func(value bool) *bool { return &value }
	cases := []struct {
		name string
		body workspaceBody
		want string
	}{
		{
			"the trust flag that no longer exists",
			workspaceBody{Trusted: boolean(true)},
			"there is no trust flag",
		},
		{
			"a network this build does not have",
			workspaceBody{Network: text("sometimes")},
			`network is "on" or "off"`,
		},
		{
			"an engine this build does not implement",
			workspaceBody{Engine: text("firecracker")},
			"sandbox engine is one of bwrap, microsandbox, host",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := test.body.refusal()
			if !strings.Contains(got, test.want) {
				t.Errorf("refusal = %q, want it to say %q", got, test.want)
			}
		})
	}
	// The values it does have are not refused, including the empty one: "the install's default"
	// is a real answer, and so is "off".
	accepted := []workspaceBody{
		{},
		{Network: text("off")},
		{Network: text("on")},
		{Engine: text("bwrap")},
		{Engine: text("")},
		{Trusted: nil},
	}
	for _, body := range accepted {
		if refusal := body.refusal(); refusal != "" {
			t.Errorf("%+v should be accepted, refused with %q", body, refusal)
		}
	}
}

func TestAWorkspaceBodyChangesOnlyWhatItNames(t *testing.T) {
	// A partial update is the point: a field the caller did not mention keeps what it had.
	boolean := func(value bool) *bool { return &value }
	existing := control.Workspace{
		Name:             "work",
		Title:            "Work",
		Image:            "debian:12",
		Network:          "off",
		Private:          false,
		Engine:           "bwrap",
		KeepAlive:        false,
		DisableVMNesting: boolean(true),
		DisableDevices:   boolean(true),
	}
	title := "Renamed"
	engine := "host"
	keepAlive := true
	updated := workspaceBody{Title: &title, Engine: &engine, KeepAlive: &keepAlive}.apply(existing)
	if updated.Title != "Work" {
		// Title is deliberately outside apply: create and update treat it differently.
		t.Errorf("apply should not touch the title, got %q", updated.Title)
	}
	if updated.Engine != "host" || !updated.KeepAlive {
		t.Errorf("the named fields were not applied: %+v", updated)
	}
	if updated.Image != "debian:12" || updated.Network != "off" || updated.DisableVMNesting == nil || !*updated.DisableVMNesting {
		t.Errorf("an unmentioned field changed: %+v", updated)
	}
	if updated.DisableDevices == nil || !*updated.DisableDevices {
		t.Errorf("the device opt-out was not kept: %+v", updated)
	}
	// Compared field by field through the registry's own view: a workspace carries a member list now, so
	// it is no longer a value Go can compare with ==.
	if changed := (workspaceBody{}).apply(existing); !reflect.DeepEqual(changed, existing) {
		t.Errorf("an empty body should change nothing: %+v", changed)
	}
}

func TestAnAnswerNamesWhereTheSecretGoes(t *testing.T) {
	cases := []struct {
		value string
		want  vault.Scope
	}{
		{"", vault.Workspace},
		{"workspace", vault.Workspace},
		{"global", vault.Global},
	}
	for _, test := range cases {
		got, err := answerScope(test.value)
		if err != nil {
			t.Fatalf("%q: %v", test.value, err)
		}
		if got != test.want {
			t.Errorf("answerScope(%q) = %v, want %v", test.value, got, test.want)
		}
	}
	if _, err := answerScope("elsewhere"); err == nil {
		t.Error("a scope this build does not have should be refused")
	}
}

func TestAnAnswerKeepsWhatTheToolAskedFor(t *testing.T) {
	prompt := control.SecretPrompt{
		Name:        "npm-token",
		Description: "the token the registry wants",
		Hosts:       []string{"registry.npmjs.org"},
	}
	// A card that names nothing keeps the tool's description and hosts: those are the request,
	// not a default.
	if got := answerDescription(prompt, secretAnswerBody{}); got != prompt.Description {
		t.Errorf("description = %q, want the tool's", got)
	}
	hosts, refusal := answerHosts(control.New(control.Options{}), prompt, secretAnswerBody{})
	if refusal != "" {
		t.Fatalf("the tool's own host should be enough: %s", refusal)
	}
	if len(hosts) != 1 || hosts[0] != "registry.npmjs.org" {
		t.Errorf("hosts = %v, want the tool's", hosts)
	}
	// A card that names its own wins, and whitespace is the caller's to keep or lose.
	description := "  shortened  "
	if got := answerDescription(prompt, secretAnswerBody{Description: &description}); got != "shortened" {
		t.Errorf("description = %q, want it trimmed", got)
	}
	own := []string{"*.example.com"}
	hosts, refusal = answerHosts(control.New(control.Options{}), prompt, secretAnswerBody{Hosts: &own})
	if refusal != "" {
		t.Fatal(refusal)
	}
	if len(hosts) != 1 || hosts[0] != "*.example.com" {
		t.Errorf("hosts = %v, want the card's", hosts)
	}
}

func TestAnEngineThatSubstitutesNeedsAHost(t *testing.T) {
	// A zero install has no sandbox manager, and the delivery it reports is substitution: the
	// value never enters the guest, so it has to be told where it may go.
	ctl := control.New(control.Options{})
	_, refusal := answerHosts(ctl, control.SecretPrompt{Name: "token"}, secretAnswerBody{})
	if refusal == "" {
		t.Fatal("a substitute engine with no host should be refused")
	}
	// And a value is never optional, whatever the scope.
	if _, _, refusal = answerTarget(ctl, control.SecretPrompt{Name: "token"}, secretAnswerBody{}); refusal != "a secret needs a value" {
		t.Errorf("refusal = %q, want the missing value to be named first", refusal)
	}
	if _, _, refusal = answerTarget(ctl, control.SecretPrompt{Name: "token"}, secretAnswerBody{Value: "v", Scope: "elsewhere"}); refusal == "" {
		t.Error("a scope this build does not have should be refused")
	}
}

func TestABodyThatIsNotJSONIsRefusedBeforeTheHandler(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"not JSON", `{"name":`, http.StatusBadRequest, "invalid JSON body"},
		{"a body over the limit", string(make([]byte, 2<<20)), http.StatusBadRequest, "invalid JSON body"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(test.body))
			var out struct{}
			if decode(recorder, request, &out) {
				t.Fatal("a body that is not JSON should not decode")
			}
			if recorder.Code != test.status {
				t.Errorf("status = %d, want %d", recorder.Code, test.status)
			}
			if got := recorder.Body.String(); !strings.Contains(got, test.want) {
				t.Errorf("body = %q, want it to say %q", got, test.want)
			}
		})
	}
	// A body the handler can use says nothing and does not write.
	recorder := httptest.NewRecorder()
	var out struct {
		Name string `json:"name"`
	}
	if !decode(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"work"}`)), &out) {
		t.Fatal("a valid body should decode")
	}
	if out.Name != "work" || recorder.Code != http.StatusOK {
		t.Errorf("decoded %+v, wrote %d", out, recorder.Code)
	}
}

// The stream's wire format: a named event, the payload on one data line, and a blank line to end
// it. A client parses this, so it is pinned here rather than only in an end-to-end run.
func TestAnEventIsNamedAndEnded(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := writeEvent(recorder, "state", map[string]string{"error": "no such conversation"}); err != nil {
		t.Fatal(err)
	}
	want := "event: state\ndata: {\"error\":\"no such conversation\"}\n\n"
	if got := recorder.Body.String(); got != want {
		t.Errorf("event = %q, want %q", got, want)
	}
	// A value that cannot be encoded is an error, not a truncated event.
	if err := writeEvent(recorder, "frame", make(chan int)); err == nil {
		t.Error("a value that is not JSON should be an error")
	}
}

func TestJSONIsAnsweredAsJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusConflict, map[string]string{"error": "no workspace"})
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if recorder.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", recorder.Code)
	}
	var decoded map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}
	if decoded["error"] != "no workspace" {
		t.Errorf("answer = %v", decoded)
	}
}

func TestTheSmallHelpersDoWhatTheirNamesSay(t *testing.T) {
	if got := firstNonEmpty("", "", "third"); got != "third" {
		t.Errorf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("firstNonEmpty of nothing = %q", got)
	}
	if got := derefString(nil); got != "" {
		t.Errorf("derefString(nil) = %q", got)
	}
	value := "set"
	if got := derefString(&value); got != "set" {
		t.Errorf("derefString = %q", got)
	}
}
