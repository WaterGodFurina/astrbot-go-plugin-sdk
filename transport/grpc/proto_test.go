package grpctransport

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"testing"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1"
	"google.golang.org/protobuf/proto"
)

// TestP1NativeEventRoundTrip 验证 P1 native Event 数据面：Event → SDKEvent
// → proto wire → SDKEvent → Event 完整往返（固定字段 / 多类型组件 / metadata）。
func TestP1NativeEventRoundTrip(t *testing.T) {
	ev := &sdk.Event{
		Type: "message", Platform: "aiocqhttp", PlatformID: "default",
		MessageType: "GroupMessage", SelfID: "2408045264", SenderID: "u1",
		SenderName: "tester", ConvID: "g:1", GroupName: "g",
		IsGroup: true, IsAtBot: true, IsAdmin: false,
		MessageStr: "hi", PlainText: "hi", RawMessage: `{"x":1}`,
		MessageID: "m1", Timestamp: 1700000000,
		Metadata: map[string]any{"foo": "bar", "n": float64(3), "nested": map[string]any{"a": true}},
		Chain: []sdk.Component{
			{Type: sdk.CompAt, TargetID: "2408045264", Name: "bot"},
			{Type: sdk.CompPlain, Text: "hello"},
			{Type: sdk.CompImage, Base64: base64.StdEncoding.EncodeToString([]byte("imgdata"))},
			{Type: sdk.CompJson, Data: map[string]any{"app": "test"}},
			{Type: sdk.CompReply, ID: "r1", Text: "quoted"},
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
	if back.Chain[0].Type != sdk.CompAt || back.Chain[1].Text != "hello" {
		t.Fatalf("chain[0,1] mismatch: %+v", back.Chain)
	}
	if back.Chain[2].Type != sdk.CompImage || back.Chain[2].Base64 != ev.Chain[2].Base64 {
		t.Fatalf("image base64 mismatch")
	}
	if back.Chain[3].Data["app"] != "test" {
		t.Fatalf("json data mismatch: %v", back.Chain[3].Data)
	}
	if back.Chain[4].Type != sdk.CompReply || back.Chain[4].ID != "r1" {
		t.Fatalf("reply mismatch: %+v", back.Chain[4])
	}
}

// TestP1NativeEventMatrix 覆盖 spec 要求的完整 Event/Component/Metadata 矩阵：
// 普通/群/私聊、AtBot/Admin、多平台/message_type、各组件类型、组合链、各类
// metadata（含空值语义）。全部走 Event → SDKEvent → wire → SDKEvent → Event。
func TestP1NativeEventMatrix(t *testing.T) {
	base64Png := base64.StdEncoding.EncodeToString([]byte("fakepngbytes"))
	cases := []struct {
		name string
		ev   *sdk.Event
	}{
		{name: "friend_plain_metadata_empty", ev: &sdk.Event{
			Type: "message", Platform: "telegram", PlatformID: "t1",
			MessageType: "FriendMessage", SelfID: "s", SenderID: "a",
			SenderName: "Alice", ConvID: "c", MessageStr: "hi",
			Chain:    []sdk.Component{{Type: sdk.CompPlain, Text: "hi"}},
			Metadata: map[string]any{},
		}},
		{name: "group_at_bot_admin", ev: &sdk.Event{
			Type: "message", Platform: "qq_official", PlatformID: "q1",
			MessageType: "GroupMessage", SelfID: "bot", SenderID: "owner",
			SenderName: "Owner", ConvID: "g:9", GroupName: "G",
			IsGroup: true, IsAtBot: true, IsAdmin: true,
			MessageStr: "@bot hi", Timestamp: 123,
			Chain: []sdk.Component{
				{Type: sdk.CompAt, TargetID: "bot", Name: "bot"},
				{Type: sdk.CompPlain, Text: "hi"},
			},
		}},
		{name: "friend_no_at_not_admin", ev: &sdk.Event{
			Type: "message", Platform: "aiocqhttp", PlatformID: "d",
			MessageType: "FriendMessage", SelfID: "s", SenderID: "u",
			MessageStr: "hello", IsGroup: false, IsAtBot: false, IsAdmin: false,
			Chain: []sdk.Component{{Type: sdk.CompPlain, Text: "hello"}},
		}},
		{name: "reply_plain", ev: &sdk.Event{
			Type: "message", Platform: "lark", MessageType: "GroupMessage",
			Chain: []sdk.Component{
				{Type: sdk.CompReply, ID: "r9", Text: "quoted"},
				{Type: sdk.CompPlain, Text: "answer"},
			},
		}},
		{name: "image_plain", ev: &sdk.Event{
			Type: "message", Platform: "discord", MessageType: "GroupMessage",
			Chain: []sdk.Component{
				{Type: sdk.CompImage, Base64: base64Png, URL: "https://example.com/x.png"},
				{Type: sdk.CompPlain, Text: "看图"},
			},
		}},
		{name: "at_image_plain", ev: &sdk.Event{
			Type: "message", Platform: "kook", MessageType: "GroupMessage",
			Chain: []sdk.Component{
				{Type: sdk.CompAt, TargetID: "u1", Name: "U"},
				{Type: sdk.CompImage, File: "/tmp/a.png", Path: "/tmp/a.png"},
				{Type: sdk.CompPlain, Text: "mixed"},
			},
		}},
		{name: "all_media_types", ev: &sdk.Event{
			Type: "message", Platform: "misskey", MessageType: "GroupMessage",
			Chain: []sdk.Component{
				{Type: sdk.CompPlain, Text: "t"},
				{Type: sdk.CompAt, TargetID: "x"},
				{Type: sdk.CompAtAll},
				{Type: sdk.CompImage, File: "f.png"},
				{Type: sdk.CompRecord, File: "a.ogg", URL: "https://e/r.ogg"},
				{Type: sdk.CompVideo, File: "v.mp4", Path: "/tmp/v.mp4"},
				{Type: sdk.CompFile, Name: "doc.pdf", URL: "https://e/d.pdf", File: "/tmp/d.pdf"},
				{Type: sdk.CompFace, ID: "1"},
				{Type: sdk.CompJson, Data: map[string]any{"app": "card", "n": float64(1), "ok": true}},
				{Type: sdk.CompForward, ID: "fwd1"},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			se := EventToSDKEvent(tc.ev)
			wire, err := proto.Marshal(se)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var se2 sdkv1.SDKEvent
			if err := proto.Unmarshal(wire, &se2); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			back := SDKEventToEvent(&se2)
			// 固定字段
			if back.Platform != tc.ev.Platform || back.MessageType != tc.ev.MessageType {
				t.Fatalf("platform/type mismatch: %+v", back)
			}
			if back.SelfID != tc.ev.SelfID || back.SenderID != tc.ev.SenderID ||
				back.SenderName != tc.ev.SenderName || back.ConvID != tc.ev.ConvID {
				t.Fatalf("identity fields mismatch: %+v", back)
			}
			if back.IsGroup != tc.ev.IsGroup || back.IsAtBot != tc.ev.IsAtBot || back.IsAdmin != tc.ev.IsAdmin {
				t.Fatalf("bool fields mismatch: %+v", back)
			}
			if back.MessageStr != tc.ev.MessageStr || back.Timestamp != tc.ev.Timestamp || back.MessageID != tc.ev.MessageID {
				t.Fatalf("msg fields mismatch: %+v", back)
			}
			// 链：类型与长度一一对应
			if len(back.Chain) != len(tc.ev.Chain) {
				t.Fatalf("chain len = %d want %d: %+v", len(back.Chain), len(tc.ev.Chain), back.Chain)
			}
			for i, c := range tc.ev.Chain {
				bc := back.Chain[i]
				if bc.Type != c.Type {
					t.Fatalf("chain[%d] type = %q want %q", i, bc.Type, c.Type)
				}
				if c.Type == sdk.CompJson {
					if bc.Data["app"] != c.Data["app"] {
						t.Fatalf("json data mismatch at %d: %v", i, bc.Data)
					}
				}
			}
		})
	}
}

// TestP1MetadataSemantics 验证 metadata 各类值的语义保持（含空值与原始 JSON）。
func TestP1MetadataSemantics(t *testing.T) {
	cases := []struct {
		name string
		md   map[string]any
	}{
		{name: "empty", md: map[string]any{}},
		{name: "scalar", md: map[string]any{"foo": "bar"}},
		{name: "nested", md: map[string]any{"a": map[string]any{"b": map[string]any{"c": "d"}}}},
		{name: "array", md: map[string]any{"arr": []any{"x", float64(2), true, nil}}},
		{name: "number", md: map[string]any{"n": float64(3.14)}},
		{name: "boolean", md: map[string]any{"ok": true, "no": false}},
		{name: "null", md: map[string]any{"nil": nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &sdk.Event{Type: "message", Platform: "aiocqhttp", MessageStr: "m", Metadata: tc.md}
			back := SDKEventToEvent(EventToSDKEvent(ev))
			if len(back.Metadata) != len(tc.md) {
				t.Fatalf("metadata len = %d want %d: %v", len(back.Metadata), len(tc.md), back.Metadata)
			}
			for k, v := range tc.md {
				bv, ok := back.Metadata[k]
				if !ok {
					t.Fatalf("missing metadata key %q", k)
				}
				if tc.name == "array" {
					arr := v.([]any)
					barr, ok := bv.([]any)
					if !ok || len(barr) != len(arr) {
						t.Fatalf("array mismatch: %v", bv)
					}
				}
			}
			// 空 metadata 不应有 metadata_json（wire 上 0 字节）
			se := EventToSDKEvent(ev)
			if len(tc.md) == 0 && len(se.MetadataJson) != 0 {
				t.Fatalf("empty metadata should produce no metadata_json, got %d bytes", len(se.MetadataJson))
			}
		})
	}
}

// TestReplyQuotedChainRoundTrip 验证 Reply 引用消息的扩展字段（被引用内容
// chain 与 sender 元数据）经 proto 往返不丢失——OneBot 引用消息 → 插件的
// 传输回归（对齐 Python Reply 语义）。
func TestReplyQuotedChainRoundTrip(t *testing.T) {
	reply := sdk.Component{
		Type: sdk.CompReply, ID: "r1", Text: "被引用文本",
		SenderID: "10001", SenderName: "阿明", SenderTime: 1788000123,
		Chain: []sdk.Component{
			{Type: sdk.CompPlain, Text: "被引用文本"},
			{Type: sdk.CompImage, URL: "https://example.com/q.png"},
		},
	}
	pc := componentsToProto([]sdk.Component{reply})
	if len(pc) != 1 || pc[0].SenderId != "10001" || pc[0].SenderName != "阿明" || pc[0].SenderTime != 1788000123 {
		t.Fatalf("proto reply sender mismatch: %+v", pc)
	}
	if len(pc[0].Chain) != 2 || pc[0].Chain[0].Text != "被引用文本" || pc[0].Chain[1].Url != "https://example.com/q.png" {
		t.Fatalf("proto reply chain mismatch: %+v", pc[0].Chain)
	}
	// wire 往返（marshal/unmarshal 后还原）。
	wire, err := proto.Marshal(&sdkv1.SDKEvent{Components: pc})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var se2 sdkv1.SDKEvent
	if err := proto.Unmarshal(wire, &se2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	back := protoToComponents(se2.Components)
	if len(back) != 1 {
		t.Fatalf("chain length = %d", len(back))
	}
	r := back[0]
	if r.Type != sdk.CompReply || r.ID != "r1" || r.Text != "被引用文本" {
		t.Fatalf("reply base mismatch: %+v", r)
	}
	if r.SenderID != "10001" || r.SenderName != "阿明" || r.SenderTime != 1788000123 {
		t.Fatalf("reply sender mismatch: %+v", r)
	}
	if len(r.Chain) != 2 || r.Chain[0].Type != sdk.CompPlain || r.Chain[0].Text != "被引用文本" ||
		r.Chain[1].Type != sdk.CompImage || r.Chain[1].URL != "https://example.com/q.png" {
		t.Fatalf("reply chain mismatch: %+v", r.Chain)
	}
}

// TestReplyChainDepthCap 验证深层嵌套 Reply 链在转换到 proto 时被深度上限
// 截断，不会无限递归（Go 值类型构造不出循环引用，用 60 层嵌套逼近上限）。
func TestReplyChainDepthCap(t *testing.T) {
	r := sdk.Component{Type: sdk.CompReply, ID: "leaf"}
	for i := 0; i < 60; i++ {
		r = sdk.Component{Type: sdk.CompReply, ID: strconv.Itoa(i), Chain: []sdk.Component{r}}
	}
	pc := componentsToProto([]sdk.Component{r})
	if len(pc) != 1 {
		t.Fatalf("chain length = %d", len(pc))
	}
	depth := 0
	for cur := pc[0]; len(cur.Chain) > 0; cur = cur.Chain[0] {
		depth++
		if depth > maxComponentDepth {
			t.Fatalf("depth cap exceeded: %d", depth)
		}
	}
	if depth != maxComponentDepth {
		t.Fatalf("depth = %d, want %d", depth, maxComponentDepth)
	}
}

func TestProtoToComponentsWithBlobResolvesFilePayload(t *testing.T) {
	comps := []*sdkv1.Component{{
		Type: "image",
		Payload: &sdkv1.BinaryPayload{
			Payload: &sdkv1.BinaryPayload_File{
				File: &sdkv1.FileReference{HandleId: "h1"},
			},
		},
	}}
	got, err := protoToComponentsWithBlob(comps, func(handleID string) ([]byte, error) {
		if handleID != "h1" {
			t.Fatalf("resolver got %q, want h1", handleID)
		}
		return []byte("blob-bytes"), nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	want := base64.StdEncoding.EncodeToString([]byte("blob-bytes"))
	if got[0].Base64 != want {
		t.Fatalf("Base64 = %q, want %q", got[0].Base64, want)
	}
	if got[0].File != "" {
		t.Fatalf("File = %q, want empty (file payload inlined)", got[0].File)
	}

	// nil resolver: file payload ignored, no panic.
	got2, err := protoToComponentsWithBlob(comps, nil)
	if err != nil || len(got2) != 1 {
		t.Fatalf("nil resolver: got (%v, %v), want (1 comp, nil)", got2, err)
	}

	// resolver error propagates.
	if _, err := protoToComponentsWithBlob(comps, func(string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}); err == nil {
		t.Fatal("expected resolver error to propagate")
	}
}
