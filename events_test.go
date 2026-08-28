package sdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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
	resp, err := s.Register(context.Background(), &sdkv1.RegisterRequest{ProtocolVersion: P1ProtocolVersion})
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
		Name:  "decorate",
		Chain: componentsToProto([]Component{Text("hi")}),
	})
	if err != nil {
		t.Fatalf("decorate: err=%v", err)
	}
	if !resp.Handled {
		t.Fatalf("decorate: want handled")
	}
	chain := protoToComponents(resp.Chain)
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

// TestP1NativeEventRoundTrip 验证 P1 native Event 数据面：Event → SDKEvent
// → proto wire → SDKEvent → Event 完整往返（固定字段 / 多类型组件 / metadata）。
func TestP1NativeEventRoundTrip(t *testing.T) {
	ev := &Event{
		Type: "message", Platform: "aiocqhttp", PlatformID: "default",
		MessageType: "GroupMessage", SelfID: "2408045264", SenderID: "u1",
		SenderName: "tester", ConvID: "g:1", GroupName: "g",
		IsGroup: true, IsAtBot: true, IsAdmin: false,
		MessageStr: "hi", PlainText: "hi", RawMessage: `{"x":1}`,
		MessageID: "m1", Timestamp: 1700000000,
		Metadata: map[string]any{"foo": "bar", "n": float64(3), "nested": map[string]any{"a": true}},
		Chain: []Component{
			{Type: CompAt, TargetID: "2408045264", Name: "bot"},
			{Type: CompPlain, Text: "hello"},
			{Type: CompImage, Base64: base64.StdEncoding.EncodeToString([]byte("imgdata"))},
			{Type: CompJson, Data: map[string]any{"app": "test"}},
			{Type: CompReply, ID: "r1", Text: "quoted"},
		},
	}
	se := EventToSDKEvent(ev)
	wire, err := proto.Marshal(se)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var se2 sdkv1.SDKEvent
	if err := proto.Unmarshal(wire, &se2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	back := SDKEventToEvent(&se2)
	if back.SenderID != "u1" || back.MessageID != "m1" || !back.IsGroup || !back.IsAtBot {
		t.Fatalf("fixed fields mismatch: %+v", back)
	}
	if back.Timestamp != 1700000000 {
		t.Fatalf("timestamp mismatch")
	}
	if back.Metadata["foo"] != "bar" || back.Metadata["nested"].(map[string]any)["a"] != true {
		t.Fatalf("metadata mismatch: %v", back.Metadata)
	}
	if len(back.Chain) != 5 {
		t.Fatalf("chain length = %d", len(back.Chain))
	}
	if back.Chain[0].Type != CompAt || back.Chain[1].Text != "hello" {
		t.Fatalf("chain[0,1] mismatch: %+v", back.Chain)
	}
	if back.Chain[2].Type != CompImage || back.Chain[2].Base64 != ev.Chain[2].Base64 {
		t.Fatalf("image base64 mismatch")
	}
	if back.Chain[3].Data["app"] != "test" {
		t.Fatalf("json data mismatch: %v", back.Chain[3].Data)
	}
	if back.Chain[4].Type != CompReply || back.Chain[4].ID != "r1" {
		t.Fatalf("reply mismatch: %+v", back.Chain[4])
	}
}

// TestP1ProtocolNegotiationMismatch 验证协议版本不匹配 → 明确失败（不 silent
// fallback 到 legacy）。
func TestP1ProtocolNegotiationMismatch(t *testing.T) {
	srv := &serviceServer{impl: &Plugin{Name: "p"}}
	_, err := srv.Register(context.Background(), &sdkv1.RegisterRequest{ProtocolVersion: 0})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("want FailedPrecondition on version mismatch, got %v", err)
	}
	if !strings.Contains(err.Error(), "protocol version mismatch") {
		t.Fatalf("want clear upgrade message, got %v", err)
	}
}
