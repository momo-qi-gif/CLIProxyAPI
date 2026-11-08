// Package logging provides Gin middleware for HTTP request logging and panic recovery.
// It integrates Gin web framework with logrus for structured logging of HTTP requests,
// responses, and error handling with panic recovery capabilities.
package logging

import (
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// aiAPIPrefixes defines path prefixes for AI API requests that should have request ID tracking.
var aiAPIPrefixes = []string{
	"/v1",
	"/v1beta",
	"/openai/v1",
	"/backend-api/codex",
}

const (
	skipGinLogKey        = "__gin_skip_request_logging__"
	creditsUsedKey       = "__antigravity_credits_used__"
	CPAStartedAtKey      = "cpa.started_at"
	CPAOutcomeKey        = "cpa.outcome"
	CPAModelKey          = "cpa.model"
	CPAStreamedKey       = "cpa.streamed"
	CPAFirstContentMSKey = "cpa.first_content_ms"
)

// CPAEventMessage serializes only approved telemetry fields so the production
// formatter's general field allowlist cannot silently discard these events.
func CPAEventMessage(event string, fields log.Fields) string {
	switch event {
	case "cpa_request_result", "cpa_connection_stage", "utls_connection_setup_retry", "claude_stream_failed":
	default:
		return "cpa_telemetry_invalid_event"
	}
	parts := []string{event}
	for _, key := range []string{"correlation_id", "attempt", "stage", "result", "duration_ms", "local_port", "next_attempt", "http_request_sent", "outcome", "http_status", "upstream_status", "failure_kind", "stream_completed", "first_content_ms", "streamed"} {
		value, exists := fields[key]
		if !exists {
			continue
		}
		var encoded string
		switch v := value.(type) {
		case string:
			encoded = strconv.Quote(v)
		case bool, int, int64, float64:
			encoded = fmt.Sprint(v)
		default:
			continue
		}
		parts = append(parts, key+"="+encoded)
	}
	return strings.Join(parts, " ")
}

func logCPARequestResult(c *gin.Context, requestID string, start time.Time) {
	outcome := c.GetString(CPAOutcomeKey)
	switch outcome {
	case "success", "stream_error", "cancelled":
	default:
		outcome = "unknown"
	}
	status := c.Writer.Status()
	if status == 499 {
		outcome = "cancelled"
	} else if status >= 400 {
		outcome = "http_error"
	} else if outcome == "unknown" && c.Request.Context().Err() != nil {
		outcome = "cancelled"
	}
	model := c.GetString(CPAModelKey)
	valid := len(model) > 0 && len(model) <= 128
	for _, char := range model {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-_/.:[]()", char)) {
			valid = false
		}
	}
	if !valid {
		model = "unknown"
	}
	fields := log.Fields{"request_id": requestID, "correlation_id": requestID, "outcome": outcome, "model": model,
		"http_status": status, "duration_ms": float64(time.Since(start).Microseconds()) / 1000,
		"streamed": c.GetBool(CPAStreamedKey)}
	if first, ok := c.Get(CPAFirstContentMSKey); ok {
		if milliseconds, valid := first.(int64); valid && milliseconds >= 0 {
			fields["first_content_ms"] = milliseconds
		}
	}
	log.WithContext(c.Request.Context()).WithFields(fields).Info(CPAEventMessage("cpa_request_result", fields))
}

// GinLogrusLogger returns a Gin middleware handler that logs HTTP requests and responses
// using logrus. It captures request details including method, path, status code, latency,
// client IP, and any error messages. Request ID is only added for AI API requests.
//
// Output format (AI API): [2025-12-23 20:14:10] [info ] | a1b2c3d4 | 200 |       23.559s | ...
// Output format (others): [2025-12-23 20:14:10] [info ] | -------- | 200 |       23.559s | ...
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for request logging
func GinLogrusLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := util.MaskSensitiveQuery(c.Request.URL.RawQuery)
		// OAuth state is a callback capability; authorization codes and issued
		// client IDs must not be included in management access logs either.
		if strings.HasPrefix(path, "/v8/management/oauth/") {
			raw = ""
		}

		// Only generate request ID for AI API paths
		var requestID string
		if isAIAPIPath(path) {
			generatedID, errGenerate := GenerateRequestID()
			if errGenerate != nil {
				log.WithError(errGenerate).Error("failed to generate request ID")
			} else {
				requestID = generatedID
				SetGinRequestID(c, requestID)
				ctx := WithRequestID(c.Request.Context(), requestID)
				c.Request = c.Request.WithContext(ctx)
			}
		}

		if c.Request.Method == http.MethodPost && path == "/v1/messages" {
			c.Set(CPAStartedAtKey, start)
		}
		c.Next()
		if c.Request.Method == http.MethodPost && path == "/v1/messages" {
			logCPARequestResult(c, requestID, start)
		}

		// Keep failed health probes visible, including responses from global middleware.
		if path == "/healthz" && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) &&
			c.Writer.Status() >= http.StatusOK && c.Writer.Status() < http.StatusMultipleChoices {
			return
		}

		if shouldSkipGinRequestLogging(c) {
			return
		}

		if raw != "" {
			path = path + "?" + raw
		}

		latency := time.Since(start)
		if latency > time.Minute {
			latency = latency.Truncate(time.Second)
		} else {
			latency = latency.Truncate(time.Millisecond)
		}

		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()

		if requestID == "" {
			requestID = "--------"
		}
		logLine := fmt.Sprintf("%3d | %13v | %15s | %-7s %q", statusCode, latency, clientIP, method, path)
		if creditsUsed(c) {
			logLine += " [credits]"
		}
		if errorMessage != "" {
			logLine = logLine + " | " + errorMessage
		}

		entry := log.WithField("request_id", requestID)

		switch {
		case statusCode >= http.StatusInternalServerError:
			entry.Error(logLine)
		case statusCode >= http.StatusBadRequest:
			entry.Warn(logLine)
		default:
			entry.Info(logLine)
		}
	}
}

// isAIAPIPath checks if the given path is an AI API endpoint that should have request ID tracking.
func isAIAPIPath(path string) bool {
	for _, prefix := range aiAPIPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// GinLogrusRecovery returns a Gin middleware handler that recovers from panics and logs
// them using logrus. When a panic occurs, it captures the panic value, stack trace,
// and request path, then returns a 500 Internal Server Error response to the client.
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for panic recovery
func GinLogrusRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			// Let net/http handle ErrAbortHandler so the connection is aborted without noisy stack logs.
			panic(http.ErrAbortHandler)
		}

		log.WithFields(log.Fields{
			"panic": recovered,
			"stack": string(debug.Stack()),
			"path":  c.Request.URL.Path,
		}).Error("recovered from panic")

		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

// SkipGinRequestLogging marks the provided Gin context so that GinLogrusLogger
// will skip emitting a log line for the associated request.
func SkipGinRequestLogging(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(skipGinLogKey, true)
}

func shouldSkipGinRequestLogging(c *gin.Context) bool {
	if c == nil {
		return false
	}
	val, exists := c.Get(skipGinLogKey)
	if !exists {
		return false
	}
	flag, ok := val.(bool)
	return ok && flag
}

func creditsUsed(c *gin.Context) bool {
	if c == nil {
		return false
	}
	val, exists := c.Get(creditsUsedKey)
	if !exists {
		return false
	}
	flag, ok := val.(bool)
	return ok && flag
}
