package sdk

import (
	"context"
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
