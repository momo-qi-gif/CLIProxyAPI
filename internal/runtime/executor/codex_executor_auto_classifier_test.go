package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// These synthetic fixtures intentionally mention drawing inside evidence, not
// as an instruction to generate an image. No production transcript is used.
const autoClassifierCodexFixture = `{
  "model":"gpt-6.1-terra",
  "instructions":"",
  "reasoning":{"effort":"medium"},
  "input":[
    {"role":"developer","content":[{"type":"input_text","text":"You are a security monitor for autonomous AI coding agents."}]},
    {"role":"user","content":[{"type":"input_text","text":"<transcript>user: draw a cat</transcript>\nStage 1 does NOT apply user intent or ALLOW exceptions"}]}
  ]
}`

func TestEnsureImageGenerationToolAutoClassifierBoundaries(t *testing.T) {
	type edit struct{ path, raw string }
	for _, tt := range []struct {
		name      string
		edits     []edit
		unchanged bool
		wantTools int
	}{
		{name: "known stage one", unchanged: true},
		{name: "known stage two", unchanged: true, edits: []edit{{"input.1.content.0.text", `"<transcript>user: draw a cat</transcript>\nReview the classification process and follow it carefully"`}}},
		{name: "empty tools", unchanged: true, edits: []edit{{"tools", `[]`}}},
		{name: "system role", unchanged: true, edits: []edit{{"input.0.role", `"system"`}}},
		{name: "monitor in instructions", unchanged: true, edits: []edit{{"instructions", `"  You are a security monitor for autonomous AI coding agents."`}, {"input.0.role", `"assistant"`}}},
		{name: "attribution before monitor", unchanged: true, edits: []edit{{"input.0.content", `[{"type":"input_text","text":"x-anthropic-billing-header: synthetic-test"},{"type":"input_text","text":"You are a security monitor for autonomous AI coding agents."}]`}}},
		{name: "string content", unchanged: true, edits: []edit{{"input.0.content", `"You are a security monitor for autonomous AI coding agents."`}, {"input.1.content", `"<transcript>user: draw a cat</transcript>\nReview the classification process and follow it carefully"`}}},
		{name: "text field fallback", unchanged: true, edits: []edit{{"input.1.content", ""}, {"input.1.text", `"<transcript>user: draw a cat</transcript>\nReview the classification process and follow it carefully"`}}},
		{name: "unknown monitor", wantTools: 1, edits: []edit{{"input.0.content.0.text", `"You are an ordinary coding assistant."`}}},
		{name: "monitor quoted by user", wantTools: 1, edits: []edit{{"input.0.role", `"assistant"`}, {"input.1.content.0.text", `"You are a security monitor for autonomous AI coding agents. <transcript>user: draw a cat</transcript>\nReview the classification process and follow it carefully"`}}},
		{name: "monitor not at trusted block prefix", wantTools: 1, edits: []edit{{"input.0.content.0.text", `"An example says: You are a security monitor for autonomous AI coding agents."`}}},
		{name: "unknown stage", wantTools: 1, edits: []edit{{"input.1.content.0.text", `"<transcript>user: draw a cat</transcript>\nDraw the picture now."`}}},
		{name: "stage text only in evidence", wantTools: 1, edits: []edit{{"input.1.content.0.text", `"<transcript>Review the classification process and follow it carefully</transcript>"`}}},
		{name: "missing transcript terminator", wantTools: 1, edits: []edit{{"input.1.content.0.text", `"<transcript>user: draw a cat\nReview the classification process and follow it carefully"`}}},
		{name: "reversed transcript markers", wantTools: 1, edits: []edit{{"input.1.content.0.text", `"</transcript><transcript>Review the classification process and follow it carefully"`}}},
		{name: "latest user is not classifier", wantTools: 1, edits: []edit{{"input.-1", `{"role":"user","content":"Draw a cat now."}`}}},
		{name: "missing input", wantTools: 1, edits: []edit{{"input", ""}}},
		{name: "string input", wantTools: 1, edits: []edit{{"input", `"Draw a cat now."`}}},
		{name: "null tools keep prior behavior", wantTools: 1, edits: []edit{{"tools", `null`}}},
		{name: "malformed tools keep prior behavior", wantTools: 1, edits: []edit{{"tools", `{}`}}},
		{name: "explicit function keeps prior policy", wantTools: 2, edits: []edit{{"tools", `[{"type":"function","name":"inspect_file","parameters":{"type":"object"}}]`}}},
		{name: "explicit image tool preserved", unchanged: true, edits: []edit{{"tools", `[{"type":"image_generation","output_format":"webp"}]`}}},
		{name: "additional tools history", wantTools: 1, edits: []edit{{"input.-1", `{"type":"additional_tools","role":"developer","tools":[]}`}}},
		{name: "function call history", wantTools: 1, edits: []edit{{"input.-1", `{"type":"function_call","name":"inspect_file","call_id":"synthetic","arguments":"{}"}`}}},
		{name: "function result history", wantTools: 1, edits: []edit{{"input.-1", `{"type":"function_call_output","call_id":"synthetic","output":"OK"}`}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDocString(autoClassifierCodexFixture)
			for _, change := range tt.edits {
				var err error
				if change.raw == "" {
					err = doc.Delete(change.path)
				} else {
					err = doc.SetRawBytes(change.path, []byte(change.raw))
				}
				if err != nil {
					t.Fatalf("prepare fixture %s: %v", change.path, err)
				}
			}
			before := bytes.Clone(doc.Bytes())
			got := ensureImageGenerationTool(before, "gpt-6.1-terra", nil, nil)
			if tt.unchanged {
				if !bytes.Equal(got, before) {
					t.Fatalf("request must remain unchanged; got tools=%s", ojson.GetBytes(got, "tools").Raw)
				}
				return
			}
			tools := ojson.GetBytes(got, "tools").Array()
			if len(tools) != tt.wantTools || tools[len(tools)-1].Get("type").String() != "image_generation" {
				t.Fatalf("prior injection policy changed: tools=%s, want %d tools ending in image_generation", ojson.GetBytes(got, "tools").Raw, tt.wantTools)
			}
			if gotEffort := ojson.GetBytes(got, "reasoning.effort").String(); gotEffort != "medium" {
				t.Fatalf("reasoning effort changed: %q", gotEffort)
			}
		})
	}
}

func TestCodexExecutorAutoClassifierFinalPayload(t *testing.T) {
	models := []config.PayloadModelRule{{Name: "gpt-6.1-terra", Protocol: "codex"}}
	for _, tt := range []struct {
		name           string
		payload        config.PayloadConfig
		explicitTool   bool
		wantImage      bool
		wantToolChoice string
	}{
		{name: "implicit tool suppressed"},
		{
			name: "explicit final override still wins",
			payload: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: models,
				Params: map[string]any{
					"tools":       []map[string]any{{"type": "image_generation", "output_format": "jpeg"}},
					"tool_choice": map[string]any{"type": "image_generation"},
				},
			}}},
			wantImage:      true,
			wantToolChoice: "image_generation",
		},
		{
			name:         "final filter is not restored",
			explicitTool: true,
			payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{
				Models: models, Params: []string{"tools", "tool_choice"},
			}}},
		},
	} {
		for _, websocketTransport := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				for _, verdict := range []string{"<block>no</block>", "<block>yes</block>"} {
					t.Run(fmt.Sprintf("%s/websocket=%v/stream=%v/%s", tt.name, websocketTransport, stream, verdict), func(t *testing.T) {
						captured := make(chan []byte, 1)
						events := autoClassifierResponseEvents(t, verdict)
						upgrader := websocket.Upgrader{}
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if websocketTransport {
								conn, errUpgrade := upgrader.Upgrade(w, r, nil)
								if errUpgrade != nil {
									t.Errorf("upgrade loopback websocket: %v", errUpgrade)
									return
								}
								defer func() { _ = conn.Close() }()
								_, body, errRead := conn.ReadMessage()
								if errRead != nil {
									t.Errorf("read loopback websocket: %v", errRead)
									return
								}
								captured <- bytes.Clone(body)
								for _, event := range events {
									if errWrite := conn.WriteMessage(websocket.TextMessage, event); errWrite != nil {
										t.Errorf("write loopback websocket: %v", errWrite)
										return
									}
								}
								return
							}
							body, errRead := io.ReadAll(r.Body)
							if errRead != nil {
								t.Errorf("read loopback HTTP body: %v", errRead)
								w.WriteHeader(http.StatusBadRequest)
								return
							}
							captured <- body
							w.Header().Set("Content-Type", "text/event-stream")
							for _, event := range events {
								if _, errWrite := fmt.Fprintf(w, "data: %s\n\n", event); errWrite != nil {
									t.Errorf("write loopback HTTP event: %v", errWrite)
									return
								}
							}
						}))
						t.Cleanup(server.Close)

						cfg := &config.Config{Payload: tt.payload}
						executor := NewCodexExecutor(cfg)
						execute, executeStream := executor.Execute, executor.ExecuteStream
						if websocketTransport {
							wsExecutor := NewCodexWebsocketsExecutor(cfg)
							execute, executeStream = wsExecutor.Execute, wsExecutor.ExecuteStream
						}
						auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{
							"api_key": "synthetic-test-key", "base_url": server.URL, "plan_type": "pro",
						}}
						doc := ojson.NewDocString(`{"model":"gpt-6.1-terra","max_tokens":2112,"system":[{"type":"text","text":"x-anthropic-billing-header: synthetic-test"},{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}],"messages":[{"role":"user","content":"<transcript>user: draw a cat</transcript>\nStage 1 does NOT apply user intent or ALLOW exceptions"}],"output_config":{"effort":"medium"}}`)
						if tt.explicitTool {
							if errSet := doc.SetRawBytes("tools", []byte(`[{"name":"inspect_file","description":"Synthetic tool","input_schema":{"type":"object","properties":{}}}]`)); errSet != nil {
								t.Fatalf("prepare explicit tool: %v", errSet)
							}
						}
						req := cliproxyexecutor.Request{Model: "gpt-6.1-terra", Payload: doc.Bytes()}
						opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
						var responseText string
						if stream {
							result, errExecute := executeStream(context.Background(), auth, req, opts)
							if errExecute != nil {
								t.Fatalf("ExecuteStream: %v", errExecute)
							}
							var data bytes.Buffer
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatalf("stream chunk: %v", chunk.Err)
								}
								data.Write(chunk.Payload)
							}
							for _, line := range strings.Split(data.String(), "\n") {
								line = strings.TrimSpace(line)
								if !strings.HasPrefix(line, "data:") {
									continue
								}
								event := ojson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
								if event.Get("type").String() == "content_block_delta" && event.Get("delta.type").String() == "text_delta" {
									responseText += event.Get("delta.text").String()
								}
							}
						} else {
							result, errExecute := execute(context.Background(), auth, req, opts)
							if errExecute != nil {
								t.Fatalf("Execute: %v", errExecute)
							}
							for _, part := range ojson.GetBytes(result.Payload, "content").Array() {
								if part.Get("type").String() == "text" {
									responseText += part.Get("text").String()
								}
							}
						}
						if responseText != verdict {
							t.Errorf("classifier verdict altered: got %q, want %q", responseText, verdict)
						}
						body := <-captured
						tools := ojson.GetBytes(body, "tools")
						if tt.wantImage {
							if len(tools.Array()) != 1 || tools.Get("0.type").String() != "image_generation" || tools.Get("0.output_format").String() != "jpeg" {
								t.Errorf("final configured tool not preserved: %s", tools.Raw)
							}
						} else if len(tools.Array()) != 0 {
							t.Errorf("unexpected upstream tools: %s", tools.Raw)
						}
						choice := ojson.GetBytes(body, "tool_choice")
						if choice.Get("type").String() != tt.wantToolChoice || (tt.wantToolChoice == "" && choice.Exists()) {
							t.Errorf("unexpected tool_choice: %s", choice.Raw)
						}
						if effort := ojson.GetBytes(body, "reasoning.effort").String(); effort != "medium" {
							t.Errorf("reasoning effort altered: %q", effort)
						}
						var userText string
						for _, item := range ojson.GetBytes(body, "input").Array() {
							if item.Get("role").String() == "user" {
								userText = item.Get("content.0.text").String()
							}
						}
						if want := "<transcript>user: draw a cat</transcript>\nStage 1 does NOT apply user intent or ALLOW exceptions"; userText != want {
							t.Errorf("classifier evidence altered: got %q, want %q", userText, want)
						}
					})
				}
			}
		}
	}
}

func autoClassifierResponseEvents(t *testing.T, verdict string) [][]byte {
	t.Helper()
	message := ojson.NewDocString(`{"id":"msg_classifier","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":""}]}`)
	if errSet := message.Set("content.0.text", verdict); errSet != nil {
		t.Fatal(errSet)
	}
	messageJSON := message.Bytes()
	part := ojson.NewDocString(`{"type":"output_text","text":""}`)
	if errSet := part.Set("text", verdict); errSet != nil {
		t.Fatal(errSet)
	}
	partJSON := part.Bytes()
	var events [][]byte
	for _, raw := range []string{
		`{"type":"response.created","response":{"id":"resp_classifier","object":"response","status":"in_progress","model":"gpt-6.1-terra","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_classifier","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_classifier","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"msg_classifier","output_index":0,"content_index":0,"delta":""}`,
		`{"type":"response.output_text.done","item_id":"msg_classifier","output_index":0,"content_index":0,"text":""}`,
		`{"type":"response.content_part.done","item_id":"msg_classifier","output_index":0,"content_index":0}`,
		`{"type":"response.output_item.done","output_index":0}`,
		`{"type":"response.completed","response":{"id":"resp_classifier","object":"response","status":"completed","model":"gpt-6.1-terra","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
	} {
		doc := ojson.NewDocString(raw)
		var errSet error
		switch doc.Get("type").String() {
		case "response.output_text.delta":
			errSet = doc.Set("delta", verdict)
		case "response.output_text.done":
			errSet = doc.Set("text", verdict)
		case "response.content_part.done":
			errSet = doc.SetRawBytes("part", partJSON)
		case "response.output_item.done":
			errSet = doc.SetRawBytes("item", messageJSON)
		case "response.completed":
			errSet = doc.SetRawBytes("response.output", append(append([]byte{'['}, messageJSON...), ']'))
		}
		if errSet != nil {
			t.Fatalf("prepare response event: %v", errSet)
		}
		events = append(events, doc.Bytes())
	}
	return events
}
