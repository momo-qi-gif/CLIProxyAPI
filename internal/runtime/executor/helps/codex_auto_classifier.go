package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// IsToollessAutoClassifierDoc recognizes known Claude Code approval side queries
// after Codex translation. Their transcripts are evidence, not instructions to
// invoke tools. Only implicit tool injection may be skipped; explicit tools,
// tool history and unknown prompt formats retain their existing policy.
func IsToollessAutoClassifierDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	if tools := doc.Get("tools"); tools.Exists() && (!tools.IsArray() || len(tools.Array()) != 0) {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}

	const monitor = "You are a security monitor for autonomous AI coding agents."
	monitorInstructions := strings.HasPrefix(strings.TrimSpace(doc.Get("instructions").String()), monitor)
	lastUser := ""
	for _, item := range input.Array() {
		switch item.Get("type").String() {
		case "additional_tools", "function_call", "function_call_output":
			return false
		}
		switch item.Get("role").String() {
		case "system", "developer":
			if strings.HasPrefix(strings.TrimSpace(autoClassifierInputItemText(item)), monitor) {
				monitorInstructions = true
			}
			// Attribution blocks can precede the monitor in the same message.
			for _, part := range item.Get("content").Array() {
				if part.Get("type").String() == "input_text" && strings.HasPrefix(strings.TrimSpace(part.Get("text").String()), monitor) {
					monitorInstructions = true
				}
			}
		case "user":
			lastUser = autoClassifierInputItemText(item)
		}
	}
	if !monitorInstructions {
		return false
	}
	start := strings.Index(lastUser, "<transcript>")
	end := strings.LastIndex(lastUser, "</transcript>")
	if start < 0 || end < start+len("<transcript>") {
		return false
	}
	suffix := lastUser[end+len("</transcript>"):]
	return strings.Contains(suffix, "Stage 1 does NOT apply user intent or ALLOW exceptions") ||
		strings.Contains(suffix, "Review the classification process and follow it carefully")
}

func autoClassifierInputItemText(item ojson.Result) string {
	content := item.Get("content")
	if content.Type == ojson.String {
		return content.String()
	}
	if !content.IsArray() {
		if text := item.Get("text"); text.Type == ojson.String {
			return text.String()
		}
		return ""
	}
	var text strings.Builder
	for _, part := range content.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "input_text", "output_text", "text":
			if value := part.Get("text"); value.Type == ojson.String {
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(value.String())
			}
		}
	}
	return text.String()
}
