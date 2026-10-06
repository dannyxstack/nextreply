package ai

// /v1/reply 的结果格式。改动时同步更新桌面端 src-tauri/src/ai/types.rs 和 src/shared/ipc.ts。

import (
	"encoding/json"
	"errors"
)

type Status string

const (
	StatusOK                  Status = "ok"
	StatusInsufficientContext Status = "insufficient_context"
	StatusNotAConversation    Status = "not_a_conversation"
)

type Analysis struct {
	Language      string `json:"language"`
	LatestMessage string `json:"latest_message"`
	Summary       string `json:"summary"`
	Emotion       string `json:"emotion"`
	Intent        string `json:"intent"`
	Strategy      string `json:"strategy"`
}

type Reply struct {
	Style string `json:"style"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

type Result struct {
	Status   Status   `json:"status"`
	Analysis Analysis `json:"analysis"`
	Replies  []Reply  `json:"replies"`
}

// ReplyJSONSchema 结构化输出用的 JSON Schema。字段顺序有意义：analysis 在 replies 前面，
// 模型先写出对上下文的简短理解，再写回复。
var ReplyJSONSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"status", "analysis", "replies"},
	"properties": map[string]any{
		"status": map[string]any{
			"type": "string",
			"enum": []string{"ok", "insufficient_context", "not_a_conversation"},
		},
		"analysis": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"language", "latest_message", "summary", "emotion", "intent", "strategy"},
			"properties": map[string]any{
				"language":       map[string]any{"type": "string", "description": "BCP-47 tag of the conversation's main language, e.g. zh-CN, en"},
				"latest_message": map[string]any{"type": "string", "description": "The other person's most recent message, verbatim"},
				"summary":        map[string]any{"type": "string", "description": "One short sentence (max ~20 chars in CJK / 12 words) describing the situation, in the conversation language"},
				"emotion":        map[string]any{"type": "string", "description": "The other person's likely emotion, a few words"},
				"intent":         map[string]any{"type": "string", "description": "What the other person likely wants, a few words"},
				"strategy":       map[string]any{"type": "string", "description": "Reply approach, a few words"},
			},
		},
		"replies": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"style", "label", "text"},
				"properties": map[string]any{
					"style": map[string]any{"type": "string", "enum": []string{"empathetic", "funny", "direct"}},
					"label": map[string]any{"type": "string"},
					"text":  map[string]any{"type": "string"},
				},
			},
		},
	},
}

var validStyles = map[string]bool{"empathetic": true, "funny": true, "direct": true}

// ParseResult 解析并校验模型输出：
// 非 ok 状态不返回回复；ok 状态必须至少有一条回复，最多保留 3 条。
func ParseResult(text string) (*Result, error) {
	var raw struct {
		Status   *Status   `json:"status"`
		Analysis *Analysis `json:"analysis"`
		Replies  *[]Reply  `json:"replies"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, err
	}
	if raw.Status == nil || raw.Analysis == nil || raw.Replies == nil {
		return nil, errors.New("missing fields")
	}
	r := &Result{Status: *raw.Status, Analysis: *raw.Analysis, Replies: *raw.Replies}
	switch r.Status {
	case StatusOK:
	case StatusInsufficientContext, StatusNotAConversation:
		r.Replies = []Reply{}
		return r, nil
	default:
		return nil, errors.New("unknown status")
	}
	for _, rep := range r.Replies {
		if !validStyles[rep.Style] || rep.Label == "" || rep.Text == "" {
			return nil, errors.New("invalid reply")
		}
	}
	if len(r.Replies) == 0 {
		return nil, errors.New("status ok but no replies")
	}
	if len(r.Replies) > 3 {
		r.Replies = r.Replies[:3]
	}
	return r, nil
}
