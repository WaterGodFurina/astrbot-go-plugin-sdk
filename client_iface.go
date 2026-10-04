package sdk

import (
	"context"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
)

// PluginClient is the host-side, transport-agnostic view of a plugin. The gRPC
// transport implements it over a go-plugin connection; the Native transport
// implements it by calling the in-process PluginService directly. Host call
// sites depend on this interface, so gRPC and Native plugins are handled
// uniformly.
type PluginClient interface {
	Register(ctx context.Context) (*sdkv1.RegisterResponse, error)
	HandleCommand(ctx context.Context, name string, args []string, se *sdkv1.SDKEvent) (string, []Component, *sdkv1.EventResult, error)
	HandleFilter(ctx context.Context, name string, se *sdkv1.SDKEvent) (bool, *sdkv1.EventResult, error)
	HandleHook(ctx context.Context, name string, se *sdkv1.SDKEvent, chain []Component) ([]Component, bool, *sdkv1.EventResult, error)
	HandleHookWithPayload(ctx context.Context, name string, se *sdkv1.SDKEvent, chain []Component, payload any) ([]Component, bool, *sdkv1.EventResult, error)
	HandleLLMRequest(ctx context.Context, name string, se *sdkv1.SDKEvent, systemPrompt, userPrompt string) (system, user string, stop bool, res *sdkv1.EventResult, err error)
	ListWebApis(ctx context.Context, ref *sdkv1.PluginRef) ([]*sdkv1.WebApiDesc, error)
	ListTools(ctx context.Context, ref *sdkv1.PluginRef) ([]*sdkv1.ToolDesc, error)
	GetConfigSchema(ctx context.Context, ref *sdkv1.PluginRef) ([]byte, error)
	HandleTool(ctx context.Context, name string, args map[string]any, se *sdkv1.SDKEvent) (string, bool, *sdkv1.EventResult, error)
	HandleWebRequest(ctx context.Context, req *sdkv1.HandleWebRequestRequest) (*sdkv1.HandleWebRequestResponse, error)
	HealthCheck(ctx context.Context) (*sdkv1.HealthResponse, error)
	Cleanup(ctx context.Context, ref *sdkv1.PluginRef) error
	SetLogLevel(ctx context.Context, level string) error
	FeedSessionWait(ctx context.Context, se *sdkv1.SDKEvent) (bool, error)
	ManagePlugin(ctx context.Context, req *sdkv1.ManagePluginRequest) (*sdkv1.ManagePluginResponse, error)
	FeedCronJob(ctx context.Context, req *sdkv1.FeedCronJobRequest) (*sdkv1.FeedCronJobResponse, error)
	// ForPlugin returns a per-plugin view (python-shared multi-tenant); Native
	// and single-tenant gRPC clients return themselves.
	ForPlugin(pluginID string) PluginClient
	PluginID() string
	Close() error
}
