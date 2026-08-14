package sdk

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
)

// TestRegisterEmitsTypedHookEvents 验证 Register 为所有类型化钩子输出正确的
// HookDesc（事件名与 Python SDK 一致），宿主按事件名分发给流水线/生命周期。
func TestRegisterEmitsTypedHookEvents(t *testing.T) {
	p := &Plugin{
		Name:                   "t",
		MessageHooks:           []MessageHook{{Name: "m", Handler: func(e *Event) error { return nil }}},
		AfterMessageSentHooks:  []AfterMessageSentHook{{Name: "ams", Handler: func(e *Event) error { return nil }}},
		WaitingLLMRequestHooks: []WaitingLLMRequestHook{{Name: "w", Handler: func(e *Event) error { return nil }}},
		LLMResponseHooks:       []LLMResponseHook{{Name: "lr", Handler: func(e *Event, r *LLMResponse) error { return nil }}},
		ToolCallHooks:          []ToolCallHook{{Name: "tc", Handler: func(e *Event, c *ToolCall) error { return nil }}},
		ToolRespondHooks:       []ToolRespondHook{{Name: "tr", Handler: func(e *Event, c *ToolCall) error { return nil }}},
		PluginErrorHooks:       []PluginErrorHook{{Name: "pe", Handler: func(e *Event, pe *PluginError) error { return nil }}},
		AstrbotLoadedHooks:     []AstrbotLoadedHook{{Name: "al", Handler: func() error { return nil }}},
		PlatformLoadedHooks:    []PlatformLoadedHook{{Name: "pl", Handler: func(string) error { return nil }}},
		PluginLoadedHooks:      []PluginLoadedHook{{Name: "pld", Handler: func(string) error { return nil }}},
		PluginUnloadedHooks:    []PluginUnloadedHook{{Name: "pu", Handler: func(string) error { return nil }}},
		AgentBeginHooks:        []AgentBeginHook{{Name: "ab", Handler: func(e *Event) error { return nil }}},
		AgentDoneHooks:         []AgentDoneHook{{Name: "ad", Handler: func(e *Event, r *LLMResponse) error { return nil }}},
	}
	s := &serviceServer{impl: p}
	resp, err := s.Register(context.Background(), &sdkv1.RegisterRequest{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	want := map[string]string{
		"m":   EventOnMessage,
		"ams": EventOnAfterMessageSent,
		"w":   EventOnWaitingLLMRequest,
		"lr":  EventOnLLMResponse,
		"tc":  EventOnUsingLLMTool,
		"tr":  EventOnLLMToolRespond,
		"pe":  EventOnPluginError,
		"al":  EventOnAstrbotLoaded,
		"pl":  EventOnPlatformLoaded,
		"pld": EventOnPluginLoaded,
		"pu":  EventOnPluginUnloaded,
		"ab":  EventOnAgentBegin,
		"ad":  EventOnAgentDone,
	}
	got := map[string]string{}
	for _, h := range resp.Hooks {
		got[h.Name] = h.Event
	}
	for name, ev := range want {
		if got[name] != ev {
			t.Errorf("hook %q: want event %q, got %q", name, ev, got[name])
		}
	}
}

// TestHandleHookPayloadDispatch 验证 HandleHook 按名称分发并把 payload_json 解码
// 为对应类型的载荷（LLMResponse / ToolCall / PluginError）。
func TestHandleHookPayloadDispatch(t *testing.T) {
	seen := ""
	p := &Plugin{
		LLMResponseHooks: []LLMResponseHook{{
			Name: "lr",
			Handler: func(e *Event, r *LLMResponse) error {
				seen = "resp:" + r.Text + "@" + r.Model
				return nil
			},
		}},
		ToolCallHooks: []ToolCallHook{{
			Name: "tc",
			Handler: func(e *Event, c *ToolCall) error {
				seen = "call:" + c.Name + ":" + c.Args["q"].(string)
				return nil
			},
		}},
		ToolRespondHooks: []ToolRespondHook{{
			Name: "tr",
			Handler: func(e *Event, c *ToolCall) error {
				seen = "resp:" + c.Result + ":err=" + strconv.FormatBool(c.IsError)
				return nil
			},
		}},
		PluginErrorHooks: []PluginErrorHook{{
			Name: "pe",
			Handler: func(e *Event, pe *PluginError) error {
				seen = "err:" + pe.PluginName + "/" + pe.HandlerName + ":" + pe.Error
				return nil
			},
		}},
		PlatformLoadedHooks: []PlatformLoadedHook{{
			Name: "pl",
			Handler: func(platform string) error {
				seen = "plat:" + platform
				return nil
			},
		}},
		AstrbotLoadedHooks: []AstrbotLoadedHook{{
			Name:    "al",
			Handler: func() error { seen = "al"; return nil },
		}},
	}
	s := &serviceServer{impl: p}

	cases := []struct {
		name    string
		payload any
		want    string
	}{
		{"lr", &LLMResponse{Text: "hello", Model: "m1"}, "resp:hello@m1"},
		{"tc", &ToolCall{Name: "get_weather", Args: map[string]any{"q": "beijing"}}, "call:get_weather:beijing"},
		{"tr", &ToolCall{Result: "sunny", IsError: true}, "resp:sunny:err=true"},
		{"pe", &PluginError{PluginName: "p", HandlerName: "h", Error: "boom"}, "err:p/h:boom"},
		{"pl", map[string]string{"platform": "aiocqhttp"}, "plat:aiocqhttp"},
		{"al", nil, "al"},
	}
	for _, c := range cases {
		seen = ""
		var payloadJSON []byte
		if c.payload != nil {
			payloadJSON, _ = json.Marshal(c.payload)
		}
		_, err := s.HandleHook(context.Background(), &sdkv1.HandleHookRequest{Name: c.name, PayloadJson: payloadJSON})
		if err != nil {
			t.Fatalf("HandleHook(%s): %v", c.name, err)
		}
		if seen != c.want {
			t.Errorf("hook %s: want %q, got %q", c.name, c.want, seen)
		}
	}
}

// TestHandleHookResultAndMessageHooks 验证 result hooks 仍能装饰回复链、message
// hooks 收到事件、未命中时返回 Handled=false。
func TestHandleHookResultAndMessageHooks(t *testing.T) {
	p := &Plugin{
		ResultHooks: []ResultHook{{
			Name:  "decorate",
			Event: EventOnDecoratingResult,
			Handler: func(e *Event, chain []Component) ([]Component, error) {
				return append(chain, Text("[x]")), nil
			},
		}},
		MessageHooks: []MessageHook{{
			Name:    "observe",
			Handler: func(e *Event) error { return nil },
		}},
	}
	s := &serviceServer{impl: p}

	resp, err := s.HandleHook(context.Background(), &sdkv1.HandleHookRequest{
		Name:      "decorate",
		ChainJson: mustJSON([]Component{Text("hi")}),
	})
	if err != nil {
		t.Fatalf("decorate: err=%v", err)
	}
	if !resp.Handled {
		t.Fatalf("decorate: want handled")
	}
	var chain []Component
	if err := json.Unmarshal(resp.ChainJson, &chain); err != nil {
		t.Fatalf("decorate chain: %v", err)
	}
	if len(chain) != 2 || chain[1].Text != "[x]" {
		t.Fatalf("decorate: want decorated chain, got %+v", chain)
	}

	// 未命中 → Handled=false
	miss, err := s.HandleHook(context.Background(), &sdkv1.HandleHookRequest{Name: "nope"})
	if err != nil || miss.Handled {
		t.Fatalf("miss: err=%v handled=%v", err, miss.Handled)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
