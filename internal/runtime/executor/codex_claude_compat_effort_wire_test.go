package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Exercise the real executor, including ApplyRequestThinking and the final
// transport payload, rather than assuming the translator's effort survives.
func TestCodexClaudeCompatExplicitEffortWire(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, thinking, effort, want string
		}{
			{"adaptive", `{"type":"adaptive"}`, "low", "low"},
			{"enabled budget", `{"type":"enabled","budget_tokens":32768}`, "medium", "medium"},
			// The model catalog does not allow none; preserve canonical clamping
			// to low instead of letting explicit high override disabled thinking.
			{"disabled", `{"type":"disabled"}`, "high", "low"},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				wire := make(chan []byte, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					payload, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read upstream request: %v", errRead)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					wire <- payload
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
				}))
				defer upstream.Close()
				exec := NewCodexExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "local-test-only"}}
				req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","thinking":%s,"output_config":{"effort":%q},"messages":[{"role":"user","content":"Reply OK"}]}`, tc.thinking, tc.effort))}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
				payload := <-wire
				if effort := ojson.GetBytes(payload, "reasoning.effort").String(); effort != tc.want {
					t.Fatalf("upstream reasoning.effort = %q, want %q; payload=%s", effort, tc.want, payload)
				}
			})
		}
	}
}
