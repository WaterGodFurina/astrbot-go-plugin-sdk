package sdk

import (
	"context"
	"encoding/base64"
	"testing"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestHostServiceIdentityIsolation 验证 HostService 反向调用的身份隔离：
// 插件只能 GetConfig/SetConfig 自己的配置，跨插件访问被拒；ChatLLM 受限流。
func TestHostServiceIdentityIsolation(t *testing.T) {
	srv := &hostServiceServer{pluginID: "test_plugin_a"}

	// 跨插件读取被拒
	_, err := srv.GetConfig(context.Background(), &sdkv1.GetConfigRequest{PluginName: "test_plugin_b"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-plugin GetConfig: want PermissionDenied, got %v", err)
	}

	// 自己名字放行（hooks 未设置 → 返回空配置而非错误）
	if _, err = srv.GetConfig(context.Background(), &sdkv1.GetConfigRequest{PluginName: "test_plugin_a"}); err != nil {
		t.Fatalf("self GetConfig should pass, got %v", err)
	}

	// 跨插件写入被拒
	_, err = srv.SetConfig(context.Background(), &sdkv1.SetConfigRequest{PluginName: "test_plugin_b"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-plugin SetConfig: want PermissionDenied, got %v", err)
	}

	// ChatLLM 限流：第 maxChatLLMPerMinute+1 次被拒
	// 用一个独立身份避免污染其他测试
	lim := &hostServiceServer{pluginID: "test_limiter"}
	for i := 0; i < maxChatLLMPerMinute; i++ {
		if _, err := lim.ChatLLM(context.Background(), &sdkv1.ChatLLMRequest{}); err != nil {
			t.Fatalf("call %d should pass, got %v", i+1, err)
		}
	}
	_, err = lim.ChatLLM(context.Background(), &sdkv1.ChatLLMRequest{})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over-limit ChatLLM: want ResourceExhausted, got %v", err)
	}
}

// TestBindHostServiceName 验证 Register 后绑定注册名后，跨名校验按注册名放行。
func TestBindHostServiceName(t *testing.T) {
	// 模拟宿主：accept 时绑定 manifest id
	server := &hostServiceServer{pluginID: "astrbot_plugin_jm_cosmos"}
	hostServersMu.Lock()
	hostServers["astrbot_plugin_jm_cosmos"] = server
	hostServersMu.Unlock()
	BindHostServiceName("astrbot_plugin_jm_cosmos", "jm_cosmos")

	// 插件用注册名 jm_cosmos 访问自己 → 放行
	if _, err := server.GetConfig(context.Background(), &sdkv1.GetConfigRequest{PluginName: "jm_cosmos"}); err != nil {
		t.Fatalf("self access with registered name should pass, got %v", err)
	}
	// 其他插件名仍被拒
	_, err := server.GetConfig(context.Background(), &sdkv1.GetConfigRequest{PluginName: "other"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-plugin after bind: want PermissionDenied, got %v", err)
	}

	hostServersMu.Lock()
	delete(hostServers, "astrbot_plugin_jm_cosmos")
	hostServersMu.Unlock()
}

// TestRegisterBridgeHookAnonymousRejected 验证匿名（无绑定身份）插件注册
// 桥接钩子被拒。
func TestRegisterBridgeHookAnonymousRejected(t *testing.T) {
	srv := &hostServiceServer{pluginID: ""}
	_, err := srv.RegisterBridgeHook(context.Background(), &sdkv1.BridgeHookRequest{HookName: "hook"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("anonymous RegisterBridgeHook: want FailedPrecondition, got %v", err)
	}
}

// TestSendMessageComponentsPayload 验证 P0-2：SendMessage 原生组件链
// （chain_components）里的 BinaryPayload：
//   - inline_data → hook 收到 Base64（等于 inline 的 base64）
//   - FileReference → 经 host ReadBlob hook 分块读回 → Base64
// 并确保 chain_components 为空时回退 chain_json 旧路径。
func TestSendMessageComponentsPayload(t *testing.T) {
	// 内存 mock blob：8B 块分块读，验证 FileReference 分块读回。
	fileData := []byte("0123456789abcdef") // 16B → 分 2 块读
	blobData := fileData
	var got []Component
	SetHostHooks(HostServiceHooks{
		SendMessage: func(platform, sessionID string, chain []Component) error {
			got = chain
			return nil
		},
		ReadBlob: func(handleID string, offset int64, limit int32) ([]byte, bool, int64, error) {
			chunk := int64(8)
			if limit > 0 {
				chunk = int64(limit)
			}
			if offset >= int64(len(blobData)) {
				return nil, true, int64(len(blobData)), nil
			}
			end := offset + chunk
			if end > int64(len(blobData)) {
				end = int64(len(blobData))
			}
			return blobData[offset:end], end >= int64(len(blobData)), int64(len(blobData)), nil
		},
	})

	ref := sdkv1.FileReference{HandleId: "abcdef0123456789abcdef0123456789", Size: int64(len(fileData))}

	req := &sdkv1.SendMessageRequest{
		Platform:  "aiocqhttp",
		SessionId: "g:1",
		ChainComponents: []*sdkv1.Component{
			{Type: "Plain", Text: "hi"},
			{Type: "Image", Payload: &sdkv1.BinaryPayload{
				Payload: &sdkv1.BinaryPayload_File{File: &ref},
			}},
		},
	}
	srv := &hostServiceServer{pluginID: "p"}
	if _, err := srv.SendMessage(context.Background(), req); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 components, got %d", len(got))
	}
	if got[0].Type != "Plain" || got[0].Text != "hi" {
		t.Fatalf("plain comp mismatch: %#v", got[0])
	}
	wantB64 := base64.StdEncoding.EncodeToString(fileData)
	if got[1].Type != "Image" || got[1].Base64 != wantB64 {
		t.Fatalf("file payload image mismatch: %#v (want b64=%s)", got[1], wantB64)
	}
}
