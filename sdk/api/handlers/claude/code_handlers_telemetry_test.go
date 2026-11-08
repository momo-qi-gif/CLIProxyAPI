package claude

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestClaudeBareMessageVisibleTiming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Set("cpa.started_at", time.Now().Add(-time.Second))
	data := make(chan []byte, 1)
	data <- []byte(`{"type":"message","content":[{"type":"text","text":"synthetic"}],"stop_reason":"end_turn"}` + "\n")
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)
	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)
	if c.GetString("cpa.outcome") != "success" {
		t.Fatal("bare message lost successful completion")
	}
	value, ok := c.Get("cpa.first_content_ms")
	if !ok {
		t.Fatal("bare message lost visible text latency")
	}
	if milliseconds, valid := value.(int64); !valid || milliseconds < 900 {
		t.Fatalf("invalid visible latency %v", value)
	}
}

func TestClaudeVisibleTextTimingOnly(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(map[bool]string{false: "thinking_only", true: "visible_text"}[visible], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			c.Set("cpa.started_at", time.Now().Add(-time.Second))
			payload := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hidden\"}}\n\n"
			if visible {
				payload = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"synthetic\"}}\n\n"
			}
			payload += "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			data := make(chan []byte, 1)
			data <- []byte(payload)
			close(data)
			errs := make(chan *interfaces.ErrorMessage)
			close(errs)
			NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)
			value, exists := c.Get("cpa.first_content_ms")
			if exists != visible {
				t.Fatalf("visible timing present=%v want %v", exists, visible)
			}
			if visible {
				milliseconds, ok := value.(int64)
				if !ok || milliseconds < 900 {
					t.Fatalf("invalid first-content timing %v", value)
				}
			}
		})
	}
}
