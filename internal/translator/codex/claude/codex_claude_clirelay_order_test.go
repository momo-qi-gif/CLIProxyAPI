package claude

import (
	"strings"
	"testing"
)

func TestCliRelaySnapshotAfterIdentifiedMessageIsNotRepeatedAtTerminal(t *testing.T) {
	text, _, _ := clirelayStream(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"m1","type":"message"}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello "}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"hello world"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","content":[{"type":"output_text","text":"hello world"}]}}`,
		`{"type":"response.completed","response":{"output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"hello world"}]}],"usage":{}}}`,
	)
	if text != "hello world" {
		t.Fatalf("replayed visible snapshot: %q", text)
	}
}

func TestCliRelayDeferredMixedContentRetainsFIFO(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"f1","type":"function_call","call_id":"c1","name":"Read"}}`),
		[]byte(`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"FIRST"}`),
		[]byte(`data: {"type":"response.reasoning_summary_part.added","output_index":2}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","output_index":2,"delta":"SECOND"}`),
		[]byte(`data: {"type":"response.output_text.delta","output_index":3,"content_index":0,"delta":"THIRD"}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"f1","type":"function_call","call_id":"c1","name":"Read","arguments":"{}"}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{}}}`),
	}
	outputs := translateCodexClaudeChunks(t, chunks)
	assertCodexClaudeContentBlockLifecycle(t, outputs)
	joined := stringJoinChunks(outputs)
	first, second, third := strings.Index(joined, "FIRST"), strings.Index(joined, "SECOND"), strings.Index(joined, "THIRD")
	if first < 0 || second <= first || third <= second {
		t.Fatalf("mixed deferred content reordered or missing: %s", joined)
	}
}

func stringJoinChunks(chunks [][]byte) string {
	var result strings.Builder
	for _, chunk := range chunks {
		result.Write(chunk)
	}
	return result.String()
}
