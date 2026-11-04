package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexClaudeIncompleteTerminalContract(t *testing.T) {
	for _, tc := range []struct {
		name, reason, wantReason string
		wantError                bool
	}{
		{name: "max output tokens", reason: "max_output_tokens"},
		{name: "content filter", reason: "content_filter", wantError: true, wantReason: "content_filter"},
		{name: "unknown reason", reason: "vendor_specific", wantError: true, wantReason: "unknown_reason"},
		{name: "missing reason", wantError: true, wantReason: "unknown_reason"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-6.1-sol\"}}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"PONG\"}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"PONG\"}]}}\n\n")
					_, _ = fmt.Fprintf(w, `data: {"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":%q},"output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`+"\n\n", tc.reason)
				}))
				defer upstream.Close()

				exec := NewCodexExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL, "api_key": "test"}}
				req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"Reply PONG"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}

				var terminalErr error
				var output strings.Builder
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						terminalErr = err
					} else {
						for chunk := range result.Chunks {
							output.Write(chunk.Payload)
							if chunk.Err != nil {
								terminalErr = chunk.Err
							}
						}
					}
				} else {
					result, err := exec.Execute(context.Background(), auth, req, opts)
					terminalErr = err
					output.Write(result.Payload)
				}

				if tc.wantError {
					if terminalErr == nil {
						t.Fatalf("incomplete reason %q was accepted; output=%s", tc.reason, output.String())
					}
					if got := statusCodeFromTestError(t, terminalErr); got != http.StatusBadGateway {
						t.Fatalf("status = %d, want %d; err=%v", got, http.StatusBadGateway, terminalErr)
					}
					if !strings.Contains(terminalErr.Error(), tc.wantReason) {
						t.Fatalf("error %q does not contain safe reason %q", terminalErr, tc.wantReason)
					}
					if stream && strings.Contains(output.String(), `"type":"message_stop"`) {
						t.Fatalf("failed terminal was translated as successful Claude completion: %s", output.String())
					}
					return
				}
				if terminalErr != nil {
					t.Fatalf("max_output_tokens incomplete unexpectedly failed: %v", terminalErr)
				}
				if !strings.Contains(output.String(), "PONG") {
					t.Fatalf("max_output_tokens incomplete lost partial output: %s", output.String())
				}
				if stream && (!strings.Contains(output.String(), `"stop_reason":"max_tokens"`) || !strings.Contains(output.String(), `"type":"message_stop"`)) {
					t.Fatalf("max_output_tokens incomplete did not close as max_tokens: %s", output.String())
				}
			})
		}
	}
}

func TestCodexResponsesIncompleteContractRemainsUnchanged(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`)
	}))
	defer upstream.Close()

	exec := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL, "api_key": "test"}}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-6.1-sol",
		Payload: []byte(`{"model":"gpt-6.1-sol","input":"Reply PONG"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("official Responses incomplete behavior changed: %v", err)
	}
	if !resp.Incomplete {
		t.Fatal("response.incomplete was not preserved")
	}
	if got := ojson.GetBytes(resp.Payload, "incomplete_details.reason").String(); got != "content_filter" {
		t.Fatalf("incomplete reason = %q, want content_filter; payload=%s", got, resp.Payload)
	}
}

func TestCodexWebsocketClaudeIncompleteTerminalFailure(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err = conn.ReadMessage(); err != nil {
			t.Errorf("read response.create: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "claude-incomplete", Provider: "codex", Attributes: map[string]string{
		"base_url": upstream.URL, "api_key": "test", "websockets": "true",
	}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-6.1-sol",
		Payload: []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("Claude websocket accepted content_filter response.incomplete")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; err=%v", got, http.StatusBadGateway, err)
	}
}

func TestCodexWebsocketStreamClaudeIncompleteTerminalFailure(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err = conn.ReadMessage(); err != nil {
			t.Errorf("read response.create: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "claude-incomplete-stream", Provider: "codex", Attributes: map[string]string{
		"base_url": upstream.URL, "api_key": "test", "websockets": "true",
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-6.1-sol",
		Payload: []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true})
	if err != nil {
		if got := statusCodeFromTestError(t, err); got != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d; err=%v", got, http.StatusBadGateway, err)
		}
		return
	}
	var terminalErr error
	var output strings.Builder
	for chunk := range result.Chunks {
		output.Write(chunk.Payload)
		if chunk.Err != nil {
			terminalErr = chunk.Err
		}
	}
	if terminalErr == nil {
		t.Fatalf("Claude websocket stream accepted content_filter response.incomplete: %s", output.String())
	}
	if got := statusCodeFromTestError(t, terminalErr); got != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; err=%v", got, http.StatusBadGateway, terminalErr)
	}
	if strings.Contains(output.String(), `"type":"message_stop"`) {
		t.Fatalf("failed terminal was translated as successful Claude completion: %s", output.String())
	}
}

func TestCodexDuplexClaudeIncompleteTerminalFailure(t *testing.T) {
	for _, created := range []bool{false, true} {
		t.Run(fmt.Sprintf("created=%t", created), func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade websocket: %v", err)
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if _, _, err = conn.ReadMessage(); err != nil {
					t.Errorf("read response.create: %v", err)
					return
				}
				if created {
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`))
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`))
				_, _, _ = conn.ReadMessage()
			}))
			defer upstream.Close()

			input := make(chan cliproxyexecutor.WebsocketInput)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), input)
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			exec := NewCodexWebsocketsExecutor(cfg)
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := &cliproxyauth.Auth{ID: "claude-incomplete-duplex", Provider: "codex", Attributes: map[string]string{
				"base_url": upstream.URL, "api_key": "test", "websockets": "true",
			}}
			result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gpt-6.1-sol",
				Payload: []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}]}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatClaude,
				Stream:       true,
				Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "claude-incomplete-duplex"},
			})
			if err != nil {
				t.Fatalf("ExecuteStream error: %v", err)
			}
			var terminalErr error
			var sawIncomplete bool
			for chunk := range result.Chunks {
				if ojson.GetBytes(chunk.Payload, "type").String() == "response.incomplete" {
					sawIncomplete = true
				}
				if chunk.Err != nil {
					if !sawIncomplete {
						t.Error("duplex error arrived before its raw terminal event")
					}
					terminalErr = chunk.Err
				}
			}
			if terminalErr == nil || !strings.Contains(terminalErr.Error(), "content_filter") {
				t.Fatalf("expected the explicit incomplete reason, not a later connection error: %v", terminalErr)
			}
			if got := statusCodeFromTestError(t, terminalErr); got != http.StatusBadGateway {
				t.Fatalf("status = %d, want %d; err=%v", got, http.StatusBadGateway, terminalErr)
			}
		})
	}
}
