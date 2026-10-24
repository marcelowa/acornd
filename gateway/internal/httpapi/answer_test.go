package httpapi

import (
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/vault"
)

// Answering a secret request is the one place a value arrives from a person, and every refusal in
// it has to be a sentence the card can show: no value, a scope nobody implements, or a host the
// engine needs before it can keep the value out of anywhere else.

// noEngine is a control with no sandbox tier: its delivery is the substitute one, which is the
// engine that needs a host.
func noEngine(t *testing.T) *control.Control {
	t.Helper()
	return control.New(control.Options{
		Workspace:   control.Workspace{Name: "w", Path: t.TempDir()},
		DataDir:     t.TempDir(),
		StoragePath: t.TempDir() + "/storage",
	})
}

func TestAnAnswerSaysWhereTheValueGoes(t *testing.T) {
	prompt := control.SecretPrompt{Name: "TOKEN", Description: "asked for by the tool", Hosts: []string{"api.example.com"}}

	scope, hosts, refusal := answerTarget(noEngine(t), prompt, secretAnswerBody{Value: "s3cret"})
	if refusal != "" {
		t.Fatalf("a plain answer should be accepted: %q", refusal)
	}
	if scope != vault.Workspace {
		t.Errorf("an answer with no scope belongs to the workspace, got %v", scope)
	}
	if len(hosts) != 1 || hosts[0] != "api.example.com" {
		t.Errorf("the hosts should be the ones the tool asked for: %v", hosts)
	}

	// No value is a refusal, because a secret nobody can use is worse than none.
	if _, _, refusal := answerTarget(noEngine(t), prompt, secretAnswerBody{}); refusal == "" {
		t.Error("an empty value should be refused")
	} else if !strings.Contains(refusal, "needs a value") {
		t.Errorf("answered %q", refusal)
	}

	// A scope the wire does not have is refused rather than defaulted: a value that landed in the
	// wrong place is a secret a different workspace can read.
	if _, _, refusal := answerTarget(noEngine(t), prompt, secretAnswerBody{Value: "v", Scope: "everywhere"}); refusal == "" {
		t.Error("an unknown scope should be refused")
	} else if !strings.Contains(refusal, `"workspace" or "global"`) {
		t.Errorf("answered %q", refusal)
	}
}

// The hosts an answer names replace the ones the tool asked for; naming none is refused when the
// engine substitutes, because a substitute with no host has nowhere it may put the value, and
// accepted when the engine hands the value over in the environment where hosts mean nothing.
func TestAnAnswerNamesItsOwnHostsOrInheritsTheTools(t *testing.T) {
	ctl := noEngine(t)
	prompt := control.SecretPrompt{Name: "TOKEN", Hosts: []string{"asked.example.com"}}

	named := []string{"given.example.com"}
	_, hosts, refusal := answerTarget(ctl, prompt, secretAnswerBody{Value: "v", Hosts: &named})
	if refusal != "" {
		t.Fatalf("naming a host should be accepted: %q", refusal)
	}
	if len(hosts) != 1 || hosts[0] != "given.example.com" {
		t.Errorf("the hosts should be the ones that were given: %v", hosts)
	}

	if !ctl.SecretNeedsHost() {
		t.Skip("this build's engine does not substitute")
	}
	empty := []string{}
	if _, _, refusal := answerTarget(ctl, prompt, secretAnswerBody{Value: "v", Hosts: &empty}); refusal == "" {
		t.Error("a substitute engine needs at least one host")
	} else if !strings.Contains(refusal, "at least one allowed host") {
		t.Errorf("answered %q", refusal)
	}
}

// The card's description wins when the person wrote one, and the tool's is kept otherwise: the
// list is the only way to tell two secrets apart, so it should say what the person meant.
func TestTheDescriptionIsWhatTheCardSaidOrWhatTheToolAskedWith(t *testing.T) {
	prompt := control.SecretPrompt{Name: "TOKEN", Description: "asked for by the tool"}

	if got := answerDescription(prompt, secretAnswerBody{}); got != "asked for by the tool" {
		t.Errorf("without one of its own the tool's is kept: %q", got)
	}
	written := "what I typed"
	if got := answerDescription(prompt, secretAnswerBody{Description: &written}); got != written {
		t.Errorf("the card's description should win: %q", got)
	}
	blank := ""
	if got := answerDescription(prompt, secretAnswerBody{Description: &blank}); got != blank {
		t.Errorf("an emptied description stays empty rather than falling back: %q", got)
	}
}
