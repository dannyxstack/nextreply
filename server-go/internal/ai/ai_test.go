package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

const analysis = `{"language":"zh-CN","latest_message":"你最近是不是很忙？","summary":"对方有点失落","emotion":"失落","intent":"求关注","strategy":"先共情再解释"}`

func reply(style string) string { return `{"style":"` + style + `","label":"x","text":"y"}` }

func TestParseResult(t *testing.T) {
	r, err := ParseResult(`{"status":"ok","analysis":` + analysis + `,"replies":[` +
		strings.Join([]string{reply("empathetic"), reply("funny"), reply("direct"), reply("direct")}, ",") + `]}`)
	if err != nil || len(r.Replies) != 3 {
		t.Fatal("ok result should be trimmed to 3", err)
	}

	if _, err := ParseResult(`{"status":"ok","analysis":` + analysis + `,"replies":[]}`); err == nil {
		t.Fatal("accepted ok with no replies")
	}

	r, err = ParseResult(`{"status":"insufficient_context","analysis":` + analysis + `,"replies":[` + reply("direct") + `]}`)
	if err != nil || len(r.Replies) != 0 {
		t.Fatal("non-ok status should drop replies", err)
	}

	for _, bad := range []string{
		`not json`,
		`{"status":"ok","replies":[]}`,
		`{"status":"maybe","analysis":` + analysis + `,"replies":[]}`,
		`{"status":"ok","analysis":` + analysis + `,"replies":[` + reply("sarcastic") + `]}`,
	} {
		if _, err := ParseResult(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestParamsShape(t *testing.T) {
	c := NewClaude(Settings{APIKey: "x", Fallbacks: "default", MaxTokens: 8000})
	b, err := json.Marshal(c.params(Input{Model: "claude-opus-5-5", Effort: "low", ImageBase64: "aGk=", MediaType: "image/jpeg", Locale: "zh-CN"}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["fallbacks"] != "default" {
		t.Fatal("fallbacks missing", string(b))
	}
	oc := m["output_config"].(map[string]any)
	if oc["effort"] != "low" || oc["format"].(map[string]any)["type"] != "json_schema" {
		t.Fatal("output_config wrong", string(b))
	}
	if _, ok := m["thinking"]; ok {
		t.Fatal("thinking should be omitted in auto mode")
	}
	sys := m["system"].([]any)[0].(map[string]any)
	if sys["cache_control"] == nil {
		t.Fatal("system prompt should be cached")
	}

	// 不支持服务端兜底的模型不发 fallbacks；Haiku 即使配置了 effort 也不传
	b, _ = json.Marshal(c.params(Input{Model: "claude-haiku-4-5", Effort: "low", ImageBase64: "aGk=", MediaType: "image/png"}))
	if strings.Contains(string(b), `"fallbacks"`) || strings.Contains(string(b), `"effort"`) || !strings.Contains(string(b), `"claude-haiku-4-5"`) {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(c.params(Input{Model: "claude-sonnet-5", Effort: "low", ImageBase64: "aGk=", MediaType: "image/png"}))
	if strings.Contains(string(b), `"fallbacks"`) || !strings.Contains(string(b), `"effort":"low"`) {
		t.Fatal(string(b))
	}
}
