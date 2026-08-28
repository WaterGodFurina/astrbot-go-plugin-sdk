// proto_bench_test.go — P1 native data plane benchmarks.
//
// Spec (P1 Protocol 2): Event JSON Marshal/Unmarshal must be 0 in the Event
// RPC hot path. These benchmarks cover the native Event→SDKEvent / SDKEvent→
// Event conversions, the proto wire (Marshal/Unmarshal) and repeated Component
// conversion for 100 KB and 1 MB events, plus a legacy JSON reference for
// comparison (showing why the JSON path was removed).
package sdk

import (
	"encoding/json"
	"testing"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"google.golang.org/protobuf/proto"
)

// makeEvent builds a representative event whose proto wire size is roughly
// targetBytes (long text body + a plain text chain). The body appears in
// several fields (MessageStr/PlainText/RawMessage + chain Plain), so it is
// sized iteratively to converge on the target.
func makeEvent(targetBytes int) *Event {
	const unit = "这是一段用于 P1 数据面基准测试的长文本回复内容。"
	body := unit
	for {
		ev := buildEvent(body)
		sz := proto.Size(EventToSDKEvent(ev))
		if sz >= targetBytes || len(body) > targetBytes*16 {
			break
		}
		body += unit
	}
	return buildEvent(body)
}

func buildEvent(body string) *Event {
	return &Event{
		Type:        "GroupMessage",
		Platform:    "aiocqhttp",
		PlatformID:  "1234567890",
		MessageType: "GroupMessage",
		SelfID:      "10001",
		SenderID:    "20002",
		SenderName:  "测试用户",
		ConvID:      "30003",
		GroupName:   "测试群",
		IsGroup:     true,
		IsAtBot:     true,
		IsAdmin:     true,
		MessageStr:  body,
		PlainText:   body,
		RawMessage:  body,
		MessageID:   "msg-abcdef-123456",
		Timestamp:   1756000000,
		Metadata: map[string]any{
			"role":       "admin",
			"is_wake":    true,
			"call_llm":   false,
			"count":      int64(42),
			"nested":     map[string]any{"a": "b", "c": []any{1, 2, 3}},
			"flag":       true,
			"null_field": nil,
		},
		Chain: []Component{
			{Type: "At", TargetID: "20002", Name: "测试用户"},
			{Type: "Plain", Text: body},
		},
	}
}

// eventWireSize reports the proto wire size of a SDKEvent derived from e.
func eventWireSize(e *Event) int {
	return proto.Size(EventToSDKEvent(e))
}

func benchNativeEvent(b *testing.B, targetBytes int) {
	e := makeEvent(targetBytes)
	sz := eventWireSize(e)
	b.ReportAllocs()
	b.SetBytes(int64(sz))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EventToSDKEvent(e)
	}
}

func benchNativeEventReverse(b *testing.B, targetBytes int) {
	se := EventToSDKEvent(makeEvent(targetBytes))
	sz := proto.Size(se)
	b.ReportAllocs()
	b.SetBytes(int64(sz))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SDKEventToEvent(se)
	}
}

func benchProtoMarshal(b *testing.B, targetBytes int) {
	se := EventToSDKEvent(makeEvent(targetBytes))
	sz := proto.Size(se)
	b.ReportAllocs()
	b.SetBytes(int64(sz))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := proto.Marshal(se); err != nil {
			b.Fatal(err)
		}
	}
}

func benchProtoUnmarshal(b *testing.B, targetBytes int) {
	raw, err := proto.Marshal(EventToSDKEvent(makeEvent(targetBytes)))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var se sdkv1.SDKEvent
		if err := proto.Unmarshal(raw, &se); err != nil {
			b.Fatal(err)
		}
	}
}

// benchJSONEventReference is the (removed) legacy path: whole Event struct →
// JSON bytes. Kept as a comparison baseline to justify the native data plane.
func benchJSONEventReference(b *testing.B, targetBytes int) {
	e := makeEvent(targetBytes)
	sz := eventWireSize(e)
	b.ReportAllocs()
	b.SetBytes(int64(sz))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEventToSDKEvent100KB(b *testing.B)   { benchNativeEvent(b, 100<<10) }
func BenchmarkEventToSDKEvent1MB(b *testing.B)     { benchNativeEvent(b, 1<<20) }
func BenchmarkSDKEventToEvent100KB(b *testing.B)   { benchNativeEventReverse(b, 100<<10) }
func BenchmarkSDKEventToEvent1MB(b *testing.B)     { benchNativeEventReverse(b, 1<<20) }
func BenchmarkProtoMarshalSDKEvent100KB(b *testing.B) { benchProtoMarshal(b, 100<<10) }
func BenchmarkProtoMarshalSDKEvent1MB(b *testing.B)   { benchProtoMarshal(b, 1<<20) }
func BenchmarkProtoUnmarshalSDKEvent100KB(b *testing.B) { benchProtoUnmarshal(b, 100<<10) }
func BenchmarkProtoUnmarshalSDKEvent1MB(b *testing.B)   { benchProtoUnmarshal(b, 1<<20) }
func BenchmarkEventJSONReference100KB(b *testing.B) { benchJSONEventReference(b, 100<<10) }
func BenchmarkEventJSONReference1MB(b *testing.B)   { benchJSONEventReference(b, 1<<20) }

func BenchmarkComponentsToProto(b *testing.B) {
	chain := make([]Component, 0, 64)
	for i := 0; i < 64; i++ {
		chain = append(chain, Component{Type: "Plain", Text: "第 N 行文本，用于组件转换基准。"})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = componentsToProto(chain)
	}
}

func BenchmarkProtoToComponents(b *testing.B) {
	chain := make([]Component, 0, 64)
	for i := 0; i < 64; i++ {
		chain = append(chain, Component{Type: "Plain", Text: "第 N 行文本，用于组件转换基准。"})
	}
	pc := componentsToProto(chain)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = protoToComponents(pc)
	}
}

// TestBenchmarkEventSizes sanity-checks that the synthetic events are actually
// in the 100 KB / 1 MB ballpark (so the benchmark numbers are meaningful).
func TestBenchmarkEventSizes(t *testing.T) {
	for _, target := range []int{100 << 10, 1 << 20} {
		e := makeEvent(target)
		wire := eventWireSize(e)
		if wire < target/3 || wire > target*3 {
			t.Fatalf("event wire size %d outside expected range for target %d", wire, target)
		}
	}
}
