package claude

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

// Snapshots extend a single output/content part; repeated done and terminal
// events must not replay text already delivered by deltas.
func appendCodexVisibleText(output []byte, params *ConvertCodexResponseToClaudeParams, event ojson.Result, text string, snapshot, refusal bool) []byte {
	outputIndex := params.CurrentMessageIndex
	if index := event.Get("output_index"); index.Exists() {
		outputIndex = index.Int()
	}
	outputKey := fmt.Sprintf("output:%d", outputIndex)
	key := fmt.Sprintf("%s:%d:%t", outputKey, event.Get("content_index").Int(), refusal)
	if params.VisibleTextByPart == nil {
		params.VisibleTextByPart = make(map[string]string)
	}
	previous := params.VisibleTextByPart[key]
	if snapshot {
		if !strings.HasPrefix(text, previous) {
			return output
		}
		text = strings.TrimPrefix(text, previous)
	}
	if text == "" {
		return output
	}
	params.VisibleTextByPart[key] = previous + text
	if params.CommentaryMessageOpen {
		output = append(output, stopCodexTextBlock(params)...)
		output = append(output, startCodexThinkingBlock(params)...)
		return append(output, appendCodexThinkingDelta(params, text)...)
	}
	output = append(output, finalizeCodexThinkingBlock(params)...)
	output = append(output, startCodexTextBlock(params)...)
	doc := ojson.NewDocString(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
	_ = doc.Set("index", params.BlockIndex)
	_ = doc.Set("delta.text", text)
	return translatorcommon.AppendSSEEventBytes(output, "content_block_delta", doc.Bytes(), 2)
}

func appendCodexVisibleMessageSnapshot(output []byte, params *ConvertCodexResponseToClaudeParams, outputIndex int, item ojson.Result) []byte {
	commentary := params.CommentaryMessageOpen || item.Get("phase").String() == "commentary"
	params.CommentaryMessageOpen = commentary
	item.Get("content").ForEach(func(index, part ojson.Result) bool {
		kind := part.Get("type").String()
		if kind != "output_text" && kind != "refusal" {
			return true
		}
		event := ojson.NewDocString(`{}`)
		if outputIndex >= 0 {
			_ = event.Set("output_index", outputIndex)
		}
		_ = event.Set("content_index", index.Int())
		field := "text"
		if kind == "refusal" {
			field = "refusal"
		}
		output = appendCodexVisibleText(output, params, ojson.ParseBytes(event.Bytes()), part.Get(field).String(), true, kind == "refusal")
		return true
	})
	if commentary {
		output = append(output, finalizeCodexCommentaryBlock(params)...)
	} else {
		output = append(output, stopCodexTextBlock(params)...)
	}
	params.CommentaryMessageOpen = false
	return output
}
