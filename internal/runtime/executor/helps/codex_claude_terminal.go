package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// CodexClaudeIncompleteFailureReason reports an incomplete terminal that Claude
// cannot represent as a successful partial response. It returns only known-safe
// reason labels so arbitrary upstream values are not exposed in client errors.
func CodexClaudeIncompleteFailureReason(payload []byte) (string, bool) {
	eventType := ojson.GetBytesNoCopy(payload, "type").String()
	if eventType != "response.incomplete" && ojson.GetBytesNoCopy(payload, "response.status").String() != "incomplete" {
		return "", false
	}
	reason := strings.TrimSpace(ojson.GetBytesNoCopy(payload, "response.incomplete_details.reason").String())
	if reason == "max_output_tokens" {
		return "", false
	}
	if reason == "content_filter" {
		return reason, true
	}
	return "unknown_reason", true
}
