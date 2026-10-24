package control

import (
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// What a conversation actually runs. Three layers, in a fixed order — what it chose, then what its
// definition names, then the install default — and the interesting cases are the ones where a layer
// is half filled in: a provider with no model is not a choice, and falling through it must not take
// the definition's answer with it.

func TestAModelNeedsBothHalvesToCount(t *testing.T) {
	input := resolveInputs{
		explicit:      protocol.ModelBinding{Provider: "chose", ModelID: "chosen"},
		fallbackModel: protocol.ModelBinding{Provider: "default", ModelID: "default-model"},
		resolved:      &protocol.ResolvedAgent{Model: "definition/from-agent"},
	}
	if provider, model := input.effectiveModel(); provider != "chose" || model != "chosen" {
		t.Errorf("a full choice wins: %s/%s", provider, model)
	}
	// A provider without a model is half a binding, and it must fall through rather than being
	// used: "chose/" is not a model anybody can run.
	input.explicit = protocol.ModelBinding{Provider: "chose"}
	if provider, model := input.effectiveModel(); provider != "definition" || model != "from-agent" {
		t.Errorf("half a choice should fall to the definition: %s/%s", provider, model)
	}
	input.explicit = protocol.ModelBinding{ModelID: "chosen"}
	if provider, model := input.effectiveModel(); provider != "definition" || model != "from-agent" {
		t.Errorf("half a choice should fall to the definition: %s/%s", provider, model)
	}
	// With no definition either, the install's default answers.
	input.resolved = nil
	if provider, model := input.effectiveModel(); provider != "default" || model != "default-model" {
		t.Errorf("nothing above should leave the install default: %s/%s", provider, model)
	}
}

// When the level that ran is not the level that was asked for, the answer says both: the interface
// shows "high (ran medium)" from this, and without it a model that cannot do the level looks like
// it ignored the choice.
func TestTheThinkingAnswerSaysWhatWasAskedFor(t *testing.T) {
	levels := []string{"off", "low", "medium"}
	cases := []struct {
		name       string
		explicit   string
		definition string
		def        string
		wantLevel  string
		wantAsked  string
	}{
		{"asked for what the model has", "low", "", "", "low", ""},
		{"asked for more than the model has", "high", "", "", "medium", "high"},
		{"asked for more than the model has, from the definition", "", "high", "", "medium", "high"},
		{"asked for more than the model has, from the install", "", "", "high", "medium", "high"},
		{"nothing asked", "", "", "", "", ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := resolveInputs{
				explicit:        protocol.ModelBinding{Provider: "p", ModelID: "m", ThinkingLevel: test.explicit},
				fallbackModel:   protocol.ModelBinding{Provider: "p", ModelID: "m"},
				defaultThinking: test.def,
				levels:          levels,
			}
			if test.definition != "" {
				input.resolved = &protocol.ResolvedAgent{Model: "p/m", Thinking: test.definition}
			}
			effective := input.effective()
			if effective.ThinkingLevel != test.wantLevel {
				t.Errorf("level = %q, want %q", effective.ThinkingLevel, test.wantLevel)
			}
			if effective.ThinkingRequested != test.wantAsked {
				t.Errorf("requested = %q, want %q", effective.ThinkingRequested, test.wantAsked)
			}
		})
	}
}

// A conversation always has an agent to show: the default when it named none, and the general one
// when there is no default either. A subagent's configuration is copied and never re-applied, so
// this is the only thing that fills the name in for it.
func TestAConversationAlwaysNamesAnAgent(t *testing.T) {
	input := resolveInputs{
		explicit:      protocol.ModelBinding{Provider: "p", ModelID: "m"},
		fallbackModel: protocol.ModelBinding{Provider: "p", ModelID: "m"},
	}
	if got := input.effective().Definition; got != GeneralAgent {
		t.Errorf("with nothing chosen the definition is %q, want %q", got, GeneralAgent)
	}
	input.defaultAgent = "reviewer"
	if got := input.effective().Definition; got != "reviewer" {
		t.Errorf("the default agent should win over the general one, got %q", got)
	}
	input.explicit.Definition = "writer"
	if got := input.effective().Definition; got != "writer" {
		t.Errorf("a chosen agent should win over the default, got %q", got)
	}
}

// The weakest level a model offers, ordered by the scale rather than by the order the provider
// listed them: a provider that lists "high, off" must still answer "off".
func TestTheLowestLevelIsTheWeakestNotTheFirst(t *testing.T) {
	cases := []struct {
		levels []string
		want   string
	}{
		{[]string{"high", "off", "medium"}, "off"},
		{[]string{"max", "minimal"}, "minimal"},
		{[]string{"xhigh", "low"}, "low"},
		{[]string{"medium"}, "medium"},
		// A level this build has never heard of ranks last rather than first: an unknown name is
		// not the weakest, it is unrankable.
		{[]string{"ultra", "off"}, "off"},
	}
	for _, test := range cases {
		if got := lowestThinking(test.levels); got != test.want {
			t.Errorf("lowestThinking(%v) = %q, want %q", test.levels, got, test.want)
		}
	}
}
