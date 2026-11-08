package logging

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestCPARequestResultMetadata(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		status   int
		declared string
		want     string
	}{
		{"completed", 200, "success", "success"}, {"stream_error", 200, "stream_error", "stream_error"},
		{"old_200_unknown", 200, "", "unknown"}, {"http_error", 500, "", "http_error"}, {"cancel", 499, "", "cancelled"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			logger := log.StandardLogger()
			old := logger.ReplaceHooks(make(log.LevelHooks))
			hook := new(logtest.Hook)
			logger.AddHook(hook)
			level := logger.GetLevel()
			logger.SetLevel(log.InfoLevel)
			defer logger.ReplaceHooks(old)
			defer logger.SetLevel(level)
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(GinLogrusLogger())
			router.POST("/v1/messages", func(c *gin.Context) {
				c.Set("cpa.model", "gpt-6.1-sol")
				c.Set("cpa.streamed", true)
				if scenario.declared != "" {
					c.Set("cpa.outcome", scenario.declared)
				}
				c.Set("cpa.first_content_ms", int64(12))
				c.Status(scenario.status)
			})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader("private prompt")))
			count := 0
			for _, e := range hook.AllEntries() {
				if !strings.HasPrefix(e.Message, "cpa_request_result ") {
					continue
				}
				count++
				if e.Data["outcome"] != scenario.want {
					t.Errorf("outcome=%v", e.Data)
				}
				id, _ := e.Data["correlation_id"].(string)
				if len(id) != 36 {
					t.Errorf("missing full request ID: %v", e.Data)
				}
				if e.Data["first_content_ms"] != int64(12) {
					t.Errorf("missing first content metric")
				}
				if _, ok := e.Data["duration_ms"]; !ok {
					t.Error("missing duration")
				}
				formatted, err := (&LogFormatter{}).Format(e)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(formatted), "correlation_id=\""+id+"\"") || !strings.Contains(string(formatted), "outcome=\""+scenario.want+"\"") {
					t.Errorf("production formatter dropped result fields: %s", formatted)
				}
				raw, _ := e.String()
				if strings.Contains(raw, "private prompt") {
					t.Fatal("payload leaked")
				}
			}
			if count != 1 {
				t.Errorf("result events=%d, want 1", count)
			}
		})
	}
}

func TestCPARequestStartAvailableToHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(GinLogrusLogger())
	router.POST("/v1/messages", func(c *gin.Context) {
		value, exists := c.Get("cpa.started_at")
		start, ok := value.(time.Time)
		if !exists || !ok || start.IsZero() {
			t.Error("missing request start")
		}
		c.Status(200)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))
}
