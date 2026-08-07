package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// streamEvent is a partial view of one line of Claude's `--output-format
// stream-json` output. Only the fields needed for progress rendering and
// REVIEW_DONE/VERIFY_DONE detection are decoded; everything else is ignored.
type streamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type    string          `json:"type"`    // text | tool_use | tool_result | ...
			Text    string          `json:"text"`    // text block
			Name    string          `json:"name"`    // tool_use block
			Input   json.RawMessage `json:"input"`   // tool_use block
			Content json.RawMessage `json:"content"` // tool_result block (string or [{type,text}])
		} `json:"content"`
	} `json:"message"`
	Result string `json:"result"` // result event: final assistant text
}

// renderClaudeStream consumes Claude stream-json (JSONL) from r, writes a
// human-readable progress trace to w as events arrive, and returns the text
// used for REVIEW_DONE/VERIFY_DONE detection (the final result event, or the
// concatenated assistant text when no result event is present). Every printed
// line is passed through red so credentials never reach w. Blank lines and
// lines that are not valid JSON events are skipped.
func renderClaudeStream(r io.Reader, w io.Writer, red *redactor) (string, error) {
	br := bufio.NewReader(r)
	var result, assistant strings.Builder

	for {
		line, readErr := br.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			var ev streamEvent
			if json.Unmarshal([]byte(trimmed), &ev) == nil {
				renderEvent(ev, w, red, &result, &assistant)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return detectionText(&result, &assistant), readErr
		}
	}
	return detectionText(&result, &assistant), nil
}

func renderEvent(ev streamEvent, w io.Writer, red *redactor, result, assistant *strings.Builder) {
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if c.Text != "" {
					fmt.Fprintln(w, red.line(c.Text))
					assistant.WriteString(c.Text)
					assistant.WriteByte('\n')
				}
			case "tool_use":
				fmt.Fprintln(w, formatToolUse(c.Name, c.Input, red))
			}
		}
	case "user":
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" {
				fmt.Fprintf(w, "← %s\n", truncate(red.line(extractToolResultText(c.Content))))
			}
		}
	case "result":
		result.WriteString(ev.Result)
	}
}

func detectionText(result, assistant *strings.Builder) string {
	if result.Len() > 0 {
		return result.String()
	}
	return assistant.String()
}

// formatToolUse renders a tool invocation as a single progress line. Bash
// commands are shown verbatim (prefixed with "$ "); other tools show the name
// and a compact view of their input.
func formatToolUse(name string, input json.RawMessage, red *redactor) string {
	if name == "Bash" {
		var b struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &b); err == nil && b.Command != "" {
			return "$ " + truncate(red.line(b.Command))
		}
	}
	compact := strings.TrimSpace(string(input))
	if compact == "" || compact == "null" {
		return "→ " + name
	}
	return "→ " + name + " " + truncate(red.line(compact))
}

// extractToolResultText pulls the textual content out of a tool_result block,
// which Claude encodes either as a bare string or as an array of
// {type,text} blocks.
func extractToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			sb.WriteString(b.Text)
		}
		return sb.String()
	}
	return ""
}

const maxLineRunes = 200

// truncate collapses s to its first line, capped at maxLineRunes runes. When
// anything is dropped, the original byte length is appended as a hint.
func truncate(s string) string {
	full := strings.TrimRight(s, "\n")
	line := full
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if r := []rune(line); len(r) > maxLineRunes {
		line = string(r[:maxLineRunes])
	}
	if line != full {
		return fmt.Sprintf("%s… (%d bytes)", line, len(full))
	}
	return line
}
