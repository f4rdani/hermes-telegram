package engine

import (
	"encoding/json"
)

type EventType string

const (
	EventSystem     EventType = "system"
	EventText       EventType = "text"
	EventToolUse    EventType = "tool_use"
	EventToolResult EventType = "tool_result"
	EventResult     EventType = "result"
)

type StreamEvent struct {
	Type       EventType              `json:"type"`
	Subtype    string                 `json:"subtype,omitempty"`
	Model      string                 `json:"model,omitempty"`
	SessionID  string                 `json:"session_id,omitempty"`
	Text       string                 `json:"text,omitempty"`
	Name       string                 `json:"name,omitempty"`
	Input      map[string]interface{} `json:"input,omitempty"`
	Output     string                 `json:"output,omitempty"`
	DurationMs int                    `json:"duration_ms,omitempty"`
	IsError    bool                   `json:"is_error,omitempty"`
	ExitCode   int                    `json:"exit_code,omitempty"`
	Tokens     *TokenUsage            `json:"tokens,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Timestamp  int64                  `json:"timestamp,omitempty"`
}

type TokenUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	Total      int `json:"total"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

type RunResult struct {
	SessionID   string
	FinalText   string
	Tokens      TokenUsage
	DurationMs  int
	ExitCode    int
	Error       string
	ToolHistory []string
}

func ParseEvent(line []byte) (*StreamEvent, error) {
	var ev StreamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}
