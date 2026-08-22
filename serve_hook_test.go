package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
)

// TestHandleHookDispatchTableCoversAllCategories 验证表驱动重构后分发表对
// 全部 15 类钩子接线正确：按名分发、payload 解码为对应类型、Handled 置位
// （审查项二-11 行为保持回归）。
func TestHandleHookDispatchTableCoversAllCategories(t *testing.T) {
	seen := ""
	p := &Plugin{
		Name: "t",
		ResultHooks: []ResultHook{{
			Name:  "res",
			Event: EventOnDecoratingResult,
			Handler: func(e *Event, chain []Component) ([]Component, error) {
				seen = "res"
				return append(chain, Text("+d")), nil
			},
		}},
		LLMResponseHooks: []LLMResponseHook{{
			Name:    "llm_response",
			Handler: func(e *Event, r *LLMResponse) error { seen = "llm_response:" + r.Text; return nil },
		}},
		ToolCallHooks: []ToolCallHook{{
			Name:    "tool_call",
			Handler: func(e *Event, c *ToolCall) error { seen = "tool_call:" + c.Name; return nil },
		}},
		ToolRespondHooks: []ToolRespondHook{{
			Name:    "tool_respond",
			Handler: func(e *Event, c *ToolCall) error { seen = "tool_respond:" + c.Result; return nil },
		}},
		PluginErrorHooks: []PluginErrorHook{{
			Name:    "plugin_error",
			Handler: func(e *Event, pe *PluginError) error { seen = "plugin_error:" + pe.HandlerName; return nil },
		}},
		PlatformLoadedHooks: []PlatformLoadedHook{{
			Name:    "platform_loaded",
			Handler: func(platform string) error { seen = "platform_loaded:" + platform; return nil },
		}},
		PluginLoadedHooks: []PluginLoadedHook{{
			Name:    "plugin_loaded",
			Handler: func(name string) error { seen = "plugin_loaded:" + name; return nil },
		}},
		PluginUnloadedHooks: []PluginUnloadedHook{{
			Name:    "plugin_unloaded",
			Handler: func(name string) error { seen = "plugin_unloaded:" + name; return nil },
		}},
		AstrbotLoadedHooks: []AstrbotLoadedHook{{
			Name:    "astrbot_loaded",
			Handler: func() error { seen = "astrbot_loaded"; return nil },
		}},
		AgentBeginHooks: []AgentBeginHook{{
			Name:    "agent_begin",
			Handler: func(e *Event) error { seen = "agent_begin:" + e.MessageStr; return nil },
		}},
		AgentDoneHooks: []AgentDoneHook{{
			Name:    "agent_done",
			Handler: func(e *Event, r *LLMResponse) error { seen = "agent_done:" + r.Text; return nil },
		}},
		MessageHooks: []MessageHook{{
			Name:    "message",
			Handler: func(e *Event) error { seen = "message:" + e.SenderID; return nil },
		}},
		AfterMessageSentHooks: []AfterMessageSentHook{{
			Name:    "after_message_sent",
			Handler: func(e *Event) error { seen = "after_message_sent"; return nil },
		}},
		WaitingLLMRequestHooks: []WaitingLLMRequestHook{{
			Name:    "waiting_llm_request",
			Handler: func(e *Event) error { seen = "waiting_llm_request"; return nil },
		}},
		Hooks: []Hook{{
			Name: "generic_hook", Event: "startup",
			Handler: func(e *Event) error { seen = "generic_hook"; return nil },
		}},
	}
	s := &serviceServer{impl: p}
	eventJSON := mustJSON(&Event{SenderID: "u1", MessageStr: "hello"})

	cases := []struct {
		name    string
		payload any
		want    string
	}{
		{"res", nil, "res"},
		{"llm_response", &LLMResponse{Text: "hi"}, "llm_response:hi"},
		{"tool_call", &ToolCall{Name: "get_weather"}, "tool_call:get_weather"},
		{"tool_respond", &ToolCall{Result: "sunny"}, "tool_respond:sunny"},
		{"plugin_error", &PluginError{HandlerName: "h1"}, "plugin_error:h1"},
		{"platform_loaded", map[string]string{"platform": "aiocqhttp"}, "platform_loaded:aiocqhttp"},
		// payloadString 的裸 JSON 字符串分支
		{"plugin_loaded", `"myplug"`, "plugin_loaded:myplug"},
		{"plugin_unloaded", map[string]string{"plugin_name": "other"}, "plugin_unloaded:other"},
		{"astrbot_loaded", nil, "astrbot_loaded"},
		{"agent_begin", nil, "agent_begin:hello"},
		{"agent_done", &LLMResponse{Text: "done"}, "agent_done:done"},
		{"message", nil, "message:u1"},
		{"after_message_sent", nil, "after_message_sent"},
		{"waiting_llm_request", nil, "waiting_llm_request"},
		{"generic_hook", nil, "generic_hook"},
	}
	for _, c := range cases {
		seen = ""
		var payloadJSON []byte
		switch v := c.payload.(type) {
		case nil:
		case string:
			payloadJSON = []byte(v)
		default:
			payloadJSON = mustJSON(v)
		}
		resp, err := s.HandleHook(context.Background(), &sdkv1.HandleHookRequest{
			Name: c.name, EventJson: eventJSON, PayloadJson: payloadJSON,
		})
		if err != nil {
			t.Fatalf("HandleHook(%s): %v", c.name, err)
		}
		if !resp.Handled || resp.Result == nil || !resp.Result.Handled {
			t.Errorf("hook %s: want handled, got Handled=%v Result=%+v", c.name, resp.Handled, resp.Result)
		}
		if seen != c.want {
			t.Errorf("hook %s: handler saw %q, want %q", c.name, seen, c.want)
		}
	}
}

// TestHandleHookMatchSemantics 验证原实现的匹配语义在表驱动下保持：
// 按名匹配、未匹配跳过；同一切片内多同名钩子只有首个被执行（首个命中即
// 返回）；跨类别同名时分表中靠前的类别优先（result > message > 通用 Hook）。
func TestHandleHookMatchSemantics(t *testing.T) {
	seen := ""
	p := &Plugin{
		ResultHooks: []ResultHook{{
			Name: "prio",
			Handler: func(e *Event, chain []Component) ([]Component, error) {
				seen = "result"
				return chain, nil
			},
		}},
		MessageHooks: []MessageHook{
			{Name: "prio", Handler: func(e *Event) error { seen = "message"; return nil }},
			{Name: "dup", Handler: func(e *Event) error { seen = "dup-first"; return nil }},
			{Name: "dup", Handler: func(e *Event) error { seen = "dup-second"; return nil }},
		},
		Hooks: []Hook{
			{Name: "prio", Handler: func(e *Event) error { seen = "generic"; return nil }},
		},
	}
	s := &serviceServer{impl: p}
	ctx := context.Background()

	// 未命中任何钩子：全部跳过，Handled=false
	seen = ""
	resp, err := s.HandleHook(ctx, &sdkv1.HandleHookRequest{Name: "nope"})
	if err != nil {
		t.Fatalf("miss: %v", err)
	}
	if resp.Handled || seen != "" {
		t.Fatalf("miss: want unhandled & no run, got Handled=%v seen=%q", resp.Handled, seen)
	}

	// 同一切片内同名：只有首个执行
	seen = ""
	resp, err = s.HandleHook(ctx, &sdkv1.HandleHookRequest{Name: "dup"})
	if err != nil || !resp.Handled || seen != "dup-first" {
		t.Fatalf("dup: want first same-name hook only, got seen=%q Handled=%v err=%v", seen, resp.Handled, err)
	}

	// 跨类别同名：result（表首）优先于 message / 通用 Hook
	seen = ""
	resp, err = s.HandleHook(ctx, &sdkv1.HandleHookRequest{Name: "prio"})
	if err != nil || !resp.Handled || seen != "result" {
		t.Fatalf("prio: want result-category priority, got seen=%q Handled=%v err=%v", seen, resp.Handled, err)
	}
}

// TestHandleHookNilHandlerStopsDispatch 验证命中但 Handler 为 nil 时立即以
// Handled=false 返回，不再落入后续类别（对应原实现的提前 return）。
func TestHandleHookNilHandlerStopsDispatch(t *testing.T) {
	ran := false
	p := &Plugin{
		MessageHooks: []MessageHook{{Name: "x"}}, // Handler 为 nil
		Hooks:        []Hook{{Name: "x", Handler: func(e *Event) error { ran = true; return nil }}},
	}
	s := &serviceServer{impl: p}
	resp, err := s.HandleHook(context.Background(), &sdkv1.HandleHookRequest{Name: "x"})
	if err != nil {
		t.Fatalf("nil handler: %v", err)
	}
	if resp.Handled {
		t.Errorf("nil handler: want Handled=false, got %+v", resp)
	}
	if ran {
		t.Errorf("nil handler: later category must not run after an earlier nil-handler match")
	}
}

// TestHandleHookErrorAndPanic 验证 handler 错误与 panic 的传递语义保持：
// 均以非 nil error 返回（panic 由 safeErr 转为错误）。
func TestHandleHookErrorAndPanic(t *testing.T) {
	sentinel := errors.New("boom")
	cases := []struct {
		name    string
		p       *Plugin
		wantErr string
	}{
		{
			name: "handler error",
			p: &Plugin{LLMResponseHooks: []LLMResponseHook{{
				Name: "e", Handler: func(e *Event, r *LLMResponse) error { return sentinel },
			}}},
			wantErr: "boom",
		},
		{
			name: "handler panic",
			p: &Plugin{MessageHooks: []MessageHook{{
				Name: "e", Handler: func(e *Event) error { panic("kaboom") },
			}}},
			wantErr: "handler panic",
		},
		{
			name: "result hook error",
			p: &Plugin{ResultHooks: []ResultHook{{
				Name: "e", Handler: func(e *Event, chain []Component) ([]Component, error) { return nil, sentinel },
			}}},
			wantErr: "boom",
		},
	}
	for _, c := range cases {
		resp, err := (&serviceServer{impl: c.p}).HandleHook(context.Background(), &sdkv1.HandleHookRequest{Name: "e"})
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: want error containing %q, got resp=%+v err=%v", c.name, c.wantErr, resp, err)
		}
	}
}

// TestHandleHookResultHookEventFilter 验证 result 钩子的事件白名单：
// 事件缺省视为 on_decorating_result；不匹配事件的条目跳过但继续扫描同切片
// 中后续同名钩子；Stop 写回 resp.Stop 且 markHandled 之后 EventResult 也能
// 读到最终 Stop。
func TestHandleHookResultHookEventFilter(t *testing.T) {
	seen := ""
	p := &Plugin{ResultHooks: []ResultHook{
		// 事件不在白名单 → 跳过该条，继续扫描同切片
		{Name: "r", Event: "bogus_event", Handler: func(e *Event, chain []Component) ([]Component, error) {
			seen = "bogus"
			return chain, nil
		}},
		// 事件缺省 → 视为 on_decorating_result，命中；Stop 写回 resp
		{Name: "r", Stop: true, Handler: func(e *Event, chain []Component) ([]Component, error) {
			seen = "default"
			return append(chain, Text("+d")), nil
		}},
	}}
	s := &serviceServer{impl: p}
	resp, err := s.HandleHook(context.Background(), &sdkv1.HandleHookRequest{
		Name:      "r",
		ChainJson: mustJSON([]Component{Text("hi")}),
	})
	if err != nil {
		t.Fatalf("HandleHook: %v", err)
	}
	if !resp.Handled || seen != "default" {
		t.Fatalf("want default-event hook handled, seen=%q Handled=%v", seen, resp.Handled)
	}
	if !resp.Stop || resp.Result == nil || !resp.Result.StopPropagation {
		t.Fatalf("want Stop mirrored to resp.Stop / Result.StopPropagation, got %+v", resp)
	}
	var chain []Component
	if err := json.Unmarshal(resp.ChainJson, &chain); err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 2 || chain[1].Text != "+d" {
		t.Fatalf("want decorated chain, got %+v", chain)
	}

	// 全部条目事件都不匹配 → 不命中，Handled=false
	bad := &serviceServer{impl: &Plugin{ResultHooks: []ResultHook{{
		Name: "r", Event: "bogus_event",
		Handler: func(e *Event, chain []Component) ([]Component, error) { return chain, nil },
	}}}}
	resp2, err := bad.HandleHook(context.Background(), &sdkv1.HandleHookRequest{Name: "r"})
	if err != nil || resp2.Handled {
		t.Fatalf("non-whitelisted event: want unhandled, got resp=%+v err=%v", resp2, err)
	}

	// on_result_handling 也在白名单内
	ok2 := &serviceServer{impl: &Plugin{ResultHooks: []ResultHook{{
		Name: "r", Event: EventOnResultHandling,
		Handler: func(e *Event, chain []Component) ([]Component, error) { return chain, nil },
	}}}}
	resp3, err := ok2.HandleHook(context.Background(), &sdkv1.HandleHookRequest{Name: "r"})
	if err != nil || !resp3.Handled {
		t.Fatalf("on_result_handling: want handled, got resp=%+v err=%v", resp3, err)
	}
}

// TestHandleHookNilImpl 验证 impl 为 nil 时安全返回 Handled=false。
func TestHandleHookNilImpl(t *testing.T) {
	s := &serviceServer{}
	resp, err := s.HandleHook(context.Background(), &sdkv1.HandleHookRequest{Name: "any"})
	if err != nil {
		t.Fatalf("nil impl: %v", err)
	}
	if resp.Handled || resp.Result != nil {
		t.Fatalf("nil impl: want empty unhandled response, got %+v", resp)
	}
}

// TestHookDispatchersTableOrder 固定分发表的顺序锚点：result 装饰链最先、
// 通用 Hook 兜底——顺序即同名钩子的优先级，属行为的一部分（审查项二-11）。
func TestHookDispatchersTableOrder(t *testing.T) {
	if len(hookDispatchers) != 15 {
		t.Fatalf("hookDispatchers: want 15 entries (one per hook category), got %d", len(hookDispatchers))
	}
	if hookDispatchers[0].desc != "result" {
		t.Errorf("first dispatcher should be result, got %q", hookDispatchers[0].desc)
	}
	if last := hookDispatchers[len(hookDispatchers)-1].desc; last != "hook" {
		t.Errorf("last dispatcher should be the generic hook fallback, got %q", last)
	}
}
