package claude

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestClaudeStreamFailureLog(t *testing.T) {
	for _, scenario := range []string{"terminal_error", "missing_terminal", "inline_error", "success"} {
		t.Run(scenario, func(t *testing.T) {
			logger := log.StandardLogger()
			previous := logger.ReplaceHooks(make(log.LevelHooks))
			hook := new(logtest.Hook)
			logger.AddHook(hook)
			level := logger.GetLevel()
			logger.SetLevel(log.InfoLevel)
			t.Cleanup(func() { logger.ReplaceHooks(previous); logger.SetLevel(level) })
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
			c.Writer.Flush()
			const sensitive = "synthetic-secret-must-not-enter-logs"
			data := make(chan []byte, 1)
			errs := make(chan *interfaces.ErrorMessage, 1)
			switch scenario {
			case "terminal_error":
				errs <- &interfaces.ErrorMessage{StatusCode: 408, Error: errors.New(sensitive)}
			case "inline_error":
				data <- []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"" + sensitive + "\"}}\n\n")
			case "success":
				data <- []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}
			close(data)
			close(errs)
			h := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			h.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)
			wantOutcome := "stream_error"
			if scenario == "success" {
				wantOutcome = "success"
			}
			if c.GetString("cpa.outcome") != wantOutcome {
				t.Errorf("outcome = %q, want %q", c.GetString("cpa.outcome"), wantOutcome)
			}
			count := 0
			for _, entry := range hook.AllEntries() {
				if !strings.HasPrefix(entry.Message, "claude_stream_failed ") {
					continue
				}
				count++
				if entry.Data["http_status"] != 200 {
					t.Errorf("HTTP status metadata = %v", entry.Data)
				}
				if entry.Data["stream_completed"] != false {
					t.Errorf("completion metadata = %v", entry.Data)
				}
				text, err := entry.String()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(text, sensitive) {
					t.Fatal("sensitive error leaked to log")
				}
			}
			want := 1
			if scenario == "success" {
				want = 0
			}
			if count != want {
				t.Errorf("failure log entries = %d, want %d", count, want)
			}
			if recorder.Code != 200 {
				t.Errorf("committed HTTP status changed: %d", recorder.Code)
			}
		})
	}
}
