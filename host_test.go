package sdk

import (
	"context"
	"encoding/base64"
	"testing"
)

// TestHostServiceIdentityIsolation 验证 HostService 反向调用的身份隔离：
// 插件只能 GetConfig/SetConfig 自己的配置，跨插件访问被拒；ChatLLM 受限流。
func TestHostServiceIdentityIsolation(t *testing.T) {
	srv := &HostServiceServer{pluginID: "test_plugin_a"}

	// 跨插件读取被拒
	_, err := srv.GetConfig(context.Background(), "test_plugin_b")
	if CodeOf(err) != CodePermissionDenied {
		t.Fatalf("cross-plugin GetConfig: want PermissionDenied, got %v", err)
	}

	// 自己名字放行（hooks 未设置 → 返回空配置而非错误）
	if _, err = srv.GetConfig(context.Background(), "test_plugin_a"); err != nil {
		t.Fatalf("self GetConfig should pass, got %v", err)
	}

	// 跨插件写入被拒
	err = srv.SetConfig(context.Background(), "test_plugin_b", nil)
	if CodeOf(err) != CodePermissionDenied {
		t.Fatalf("cross-plugin SetConfig: want PermissionDenied, got %v", err)
	}

	// ChatLLM 限流：第 maxChatLLMPerMinute+1 次被拒
	// 用一个独立身份避免污染其他测试
	lim := &HostServiceServer{pluginID: "test_limiter"}
	for i := 0; i < maxChatLLMPerMinute; i++ {
		if _, err := lim.ChatLLM(context.Background(), &ChatLLMRequest{}); err != nil {
			t.Fatalf("call %d should pass, got %v", i+1, err)
		}
	}
	_, err = lim.ChatLLM(context.Background(), &ChatLLMRequest{})
	if CodeOf(err) != CodeResourceExhausted {
		t.Fatalf("over-limit ChatLLM: want ResourceExhausted, got %v", err)
	}
}

// TestBindHostServiceName 验证 Register 后绑定注册名后，跨名校验按注册名放行。
func TestBindHostServiceName(t *testing.T) {
	// 模拟宿主：accept 时绑定 manifest id
	server := &HostServiceServer{pluginID: "astrbot_plugin_jm_cosmos"}
	hostServersMu.Lock()
	hostServers["astrbot_plugin_jm_cosmos"] = server
	hostServersMu.Unlock()
	BindHostServiceName("astrbot_plugin_jm_cosmos", "jm_cosmos")

	// 插件用注册名 jm_cosmos 访问自己 → 放行
	if _, err := server.GetConfig(context.Background(), "jm_cosmos"); err != nil {
		t.Fatalf("self access with registered name should pass, got %v", err)
	}
	// 其他插件名仍被拒
	_, err := server.GetConfig(context.Background(), "other")
	if CodeOf(err) != CodePermissionDenied {
		t.Fatalf("cross-plugin after bind: want PermissionDenied, got %v", err)
	}

	hostServersMu.Lock()
	delete(hostServers, "astrbot_plugin_jm_cosmos")
	hostServersMu.Unlock()
}

// TestRegisterBridgeHookAnonymousRejected 验证匿名（无绑定身份）插件注册
// 桥接钩子被拒。
func TestRegisterBridgeHookAnonymousRejected(t *testing.T) {
	srv := &HostServiceServer{pluginID: ""}
	err := srv.RegisterBridgeHook(context.Background(), "", "hook")
	if CodeOf(err) != CodeFailedPrecondition {
		t.Fatalf("anonymous RegisterBridgeHook: want FailedPrecondition, got %v", err)
	}
}

// TestSendMessageComponentsPayload 验证原生 SendMessage 把组件链原样交给宿主
// hook（含 Base64 媒体二进制）。
//
// 注：旧实现里 proto SendMessageRequest.ChainComponents 的 BinaryPayload
// （inline_data / file→ReadBlob）解码发生在核心 HostServiceServer；native
// 重构后核心只接收 []Component，proto→native 的组件解码（含 inline_data）
// 移至 transport/grpc 边界，file 型 payload 的 blob 解析由宿主 hook 侧负责。
func TestSendMessageComponentsPayload(t *testing.T) {
	fileData := []byte("0123456789abcdef")
	wantB64 := base64.StdEncoding.EncodeToString(fileData)
	var got []Component
	SetHostHooks(HostServiceHooks{
		SendMessage: func(platform, sessionID string, chain []Component) error {
			got = chain
			return nil
		},
	})

	chain := []Component{
		{Type: CompPlain, Text: "hi"},
		{Type: CompImage, Base64: wantB64},
	}
	srv := &HostServiceServer{pluginID: "p"}
	if err := srv.SendMessage(context.Background(), "aiocqhttp", "g:1", chain); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 components, got %d", len(got))
	}
	if got[0].Type != CompPlain || got[0].Text != "hi" {
		t.Fatalf("plain comp mismatch: %#v", got[0])
	}
	if got[1].Type != CompImage || got[1].Base64 != wantB64 {
		t.Fatalf("image comp mismatch: %#v (want b64=%s)", got[1], wantB64)
	}
}
