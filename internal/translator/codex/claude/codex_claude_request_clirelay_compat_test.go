package claude

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertClaudeRequestToCodex_UnknownForcedToolFallsBackToAuto(t *testing.T) {
	for _, choice := range []string{
		`{"type":"tool"}`,
		`{"type":"tool","name":"Missing"}`,
	} {
		input := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Read","input_schema":{"type":"object"}}],"tool_choice":` + choice + `}`)
		out, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", input, true)
		if err != nil {
			t.Fatalf("ConvertClaudeRequestToCodex returned error: %v", err)
		}
		if got := ojson.GetBytes(out, "tool_choice").Raw; got != `"auto"` {
			t.Errorf("tool_choice = %s, want auto; translated=%s", got, out)
		}
	}
}

func TestConvertClaudeRequestToCodex_ExplicitEffortOverridesEnabledBudget(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		input := []byte(`{"thinking":{"type":"enabled","budget_tokens":8192},"output_config":{"effort":"` + effort + `"},"messages":[{"role":"user","content":"hi"}]}`)
		out, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", input, true)
		if err != nil {
			t.Fatalf("ConvertClaudeRequestToCodex returned error: %v", err)
		}
		if got := ojson.GetBytes(out, "reasoning.effort").String(); got != effort {
			t.Errorf("effort %q produced reasoning.effort %q; translated=%s", effort, got, out)
		}
	}
}

func TestConvertClaudeRequestToCodex_AdaptiveEffortPassesThrough(t *testing.T) {
	input := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"minimal"},"messages":[{"role":"user","content":"hi"}]}`)
	if !ojson.ValidBytes(input) {
		t.Fatalf("invalid test request JSON: %s", input)
	}
	out, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", input, true)
	if err != nil {
		t.Fatalf("ConvertClaudeRequestToCodex returned error: %v", err)
	}
	if got := ojson.GetBytes(out, "reasoning.effort").String(); got != "minimal" {
		t.Fatalf("adaptive effort = %q, want minimal; translated=%s", got, out)
	}
}

func TestConvertClaudeRequestToCodex_OmitsToolChoiceWithoutTools(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "empty tools", input: `{"messages":[{"role":"user","content":"hi"}],"tools":[],"tool_choice":{"type":"any"}}`},
		{name: "tools field absent", input: `{"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.input)
			if !ojson.ValidBytes(input) {
				t.Fatalf("invalid test request JSON: %s", input)
			}
			out, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", input, true)
			if err != nil {
				t.Fatalf("ConvertClaudeRequestToCodex returned error: %v", err)
			}
			if got := ojson.GetBytes(out, "tool_choice"); got.Exists() {
				t.Fatalf("tool_choice must be omitted without tools, got %s", got.Raw)
			}
		})
	}
}
