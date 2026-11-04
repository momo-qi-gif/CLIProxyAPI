package claude

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// These fixtures preserve the deployed CliRelay compatibility contract without
// replacing upstream's commentary, signature, or native web-search handling.
func clirelayStream(t *testing.T, events ...string) (string, []*codexClaudeContentBlock, string) {
	t.Helper()
	chunks := [][]byte{[]byte(`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.1-sol"}}`)}
	for _, event := range events {
		chunks = append(chunks, []byte("data: "+event))
	}
	outputs := translateCodexClaudeChunks(t, chunks)
	blocks := assertCodexClaudeContentBlockLifecycle(t, outputs)
	var text, reason string
	for _, block := range blocks {
		if block.Type == "text" {
			text += block.Text
		}
	}
	for _, output := range outputs {
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "data: ") {
				event := ojson.Parse(strings.TrimPrefix(line, "data: "))
				if event.Get("type").String() == "message_delta" {
					reason = event.Get("delta.stop_reason").String()
				}
			}
		}
	}
	return text, blocks, reason
}

func TestCliRelayVisibleSnapshots(t *testing.T) {
	for _, kind := range []string{"output_text", "refusal"} {
		for _, mode := range []string{"done", "part", "item", "terminal", "delta_and_snapshots"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				field := "text"
				if kind == "refusal" {
					field = "refusal"
				}
				part := fmt.Sprintf(`{"type":%q,%q:"hello world"}`, kind, field)
				item := `{"id":"m1","type":"message","content":[` + part + `]}`
				var events []string
				if mode == "delta_and_snapshots" {
					events = append(events, fmt.Sprintf(`{"type":%q,"output_index":0,"content_index":0,"delta":"hello "}`, "response."+kind+".delta"))
				}
				if mode == "done" || mode == "delta_and_snapshots" {
					events = append(events, fmt.Sprintf(`{"type":%q,"output_index":0,"content_index":0,%q:"hello world"}`, "response."+kind+".done", field))
				}
				if mode == "part" || mode == "delta_and_snapshots" {
					events = append(events, `{"type":"response.content_part.done","output_index":0,"content_index":0,"part":`+part+`}`)
				}
				if mode == "item" || mode == "delta_and_snapshots" {
					events = append(events, `{"type":"response.output_item.done","output_index":0,"item":`+item+`}`)
				}
				output := "[]"
				if mode == "terminal" || mode == "delta_and_snapshots" {
					output = "[" + item + "]"
				}
				events = append(events, `{"type":"response.completed","response":{"output":`+output+`,"usage":{}}}`)
				text, _, _ := clirelayStream(t, events...)
				if text != "hello world" {
					t.Fatalf("visible content = %q, want complete text exactly once", text)
				}
			})
		}
	}
}

func TestCliRelayRefusalNonStream(t *testing.T) {
	body := []byte(`{"type":"response.completed","response":{"id":"r1","output":[{"type":"message","content":[{"type":"refusal","refusal":"Cannot comply"}]}],"usage":{}}}`)
	var state any
	got := ConvertCodexResponseToClaudeNonStream(context.Background(), "gpt-6.1-sol", nil, nil, body, &state)
	if text := ojson.GetBytes(got, "content.0.text").String(); text != "Cannot comply" {
		t.Fatalf("refusal text = %q, want preserved refusal", text)
	}
}

func TestCliRelayTerminalAfterDeferredDelta(t *testing.T) {
	text, _, _ := clirelayStream(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"Read"}}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"hello "}`,
		`{"type":"response.completed","response":{"output":[{"id":"f1","type":"function_call","call_id":"c1","name":"Read","arguments":"{}"},{"type":"message","content":[{"type":"output_text","text":"hello world"}]}],"usage":{}}}`,
	)
	if text != "hello world" {
		t.Fatalf("deferred/terminal text = %q, want complete text exactly once", text)
	}
}

func TestCliRelayTruncatedToolNotExecutable(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			_, blocks, reason := clirelayStream(t,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"Read"}}`,
				`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"f1","delta":"{\"path\":"}`,
				`{"type":"`+terminal+`","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{}}}`,
			)
			for _, block := range blocks {
				if block.Type == "tool_use" {
					t.Fatalf("truncated arguments exposed as executable tool: %#v", block)
				}
			}
			if reason != "max_tokens" {
				t.Fatalf("stop_reason = %q, want max_tokens", reason)
			}
		})
	}
}

func TestCliRelayToolWaitsForValidatedCompletion(t *testing.T) {
	var state any
	request := []byte(`{"tools":[{"name":"Read"}]}`)
	for _, event := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"Read"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"f1","delta":"{\"path\":\"a\"}"}`,
	} {
		for _, chunk := range ConvertCodexResponseToClaude(context.Background(), "", request, nil, []byte("data: "+event), &state) {
			if strings.Contains(string(chunk), `"type":"tool_use"`) {
				t.Fatalf("tool started before completion: %s", chunk)
			}
		}
	}
	out := ConvertCodexResponseToClaude(context.Background(), "", request, nil, []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"Read","arguments":"{\"path\":\"a\"}"}}`), &state)
	blocks := assertCodexClaudeContentBlockLifecycle(t, out)
	if len(blocks) != 1 || blocks[0].Arguments != `{"path":"a"}` {
		t.Fatalf("completed tool = %#v, want validated arguments", blocks)
	}
}

func TestCliRelayLengthLimitKeepsMaxTokensWithValidTool(t *testing.T) {
	_, blocks, reason := clirelayStream(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"Read"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"Read","arguments":"{}"}}`,
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{}}}`,
	)
	if len(blocks) != 1 || blocks[0].Type != "tool_use" || reason != "max_tokens" {
		t.Fatalf("length-limited tool lost or stop reason masked: blocks=%#v reason=%q", blocks, reason)
	}
}

func TestCliRelayNonStreamDropsTruncatedTool(t *testing.T) {
	body := []byte(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","call_id":"c1","name":"Read","arguments":"{\"path\":"}],"usage":{}}}`)
	out := ConvertCodexResponseToClaudeNonStream(context.Background(), "", nil, nil, body, nil)
	if len(ojson.GetBytes(out, "content").Array()) != 0 || ojson.GetBytes(out, "stop_reason").String() != "max_tokens" {
		t.Fatalf("nonstream exposed truncated tool or lost length stop reason: %s", out)
	}
}

func TestCliRelayConflictingToolIdentityDoesNotCorruptArguments(t *testing.T) {
	_, blocks, _ := clirelayStream(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"Read"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"different_item","delta":"{\"wrong\":true}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"Read","arguments":"{\"path\":\"a\"}"}}`,
		`{"type":"response.completed","response":{"usage":{}}}`,
	)
	if len(blocks) != 1 || blocks[0].Arguments != `{"path":"a"}` {
		t.Fatalf("conflicting identity corrupted real tool: %#v", blocks)
	}
}

func TestCliRelayTerminalIgnoresLateEvents(t *testing.T) {
	text, _, _ := clirelayStream(t,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`,
		`{"type":"response.completed","response":{"usage":{}}}`,
		`{"type":"response.completed","response":{"usage":{}}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"late"}`,
	)
	if text != "hello" {
		t.Fatalf("late text = %q", text)
	}
}
