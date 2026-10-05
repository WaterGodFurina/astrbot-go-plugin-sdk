package sdk

import "context"

// client_iface.go — the host-side, transport-agnostic view of a plugin. This is
// the SOURCE OF TRUTH (hand-written): the gRPC transport implements it over a
// go-plugin connection, the Native transport by calling the in-process
// PluginService directly. No protobuf type may appear here.

// PluginClient is how the host drives a plugin regardless of transport.
type PluginClient interface {
	Register(ctx context.Context) (PluginInfo, error)
	HandleCommand(ctx context.Context, name string, args []string, event *Event) (HandleCommandResult, error)
	HandleFilter(ctx context.Context, name string, event *Event) (HandleFilterResult, error)
	HandleHook(ctx context.Context, name string, event *Event, chain []Component) (HandleHookResult, error)
	HandleHookWithPayload(ctx context.Context, name string, event *Event, chain []Component, payload any) (HandleHookResult, error)
	HandleLLMRequest(ctx context.Context, name string, event *Event, systemPrompt, userPrompt string) (HandleLLMRequestResult, error)
	ListTools(ctx context.Context) ([]ToolDesc, error)
	ListWebApis(ctx context.Context) ([]WebAPIDesc, error)
	GetConfigSchema(ctx context.Context) ([]byte, error)
	HandleTool(ctx context.Context, name string, args map[string]any, event *Event) (HandleToolResult, error)
	HandleWebRequest(ctx context.Context, req HandleWebRequest) (HandleWebResponse, error)
	HealthCheck(ctx context.Context) (HealthInfo, error)
	Cleanup(ctx context.Context) error
	SetLogLevel(ctx context.Context, level string) error
	FeedSessionWait(ctx context.Context, event *Event) (bool, error)
	// ManagePlugin / FeedCronJob are python-shared runtime control calls; Go
	// plugins do not implement them (the Go-side PluginService omits them).
	ManagePlugin(ctx context.Context, req ManagePluginRequest) (ManagePluginResponse, error)
	FeedCronJob(ctx context.Context, req FeedCronJobRequest) (FeedCronJobResponse, error)

	// ForPlugin returns a per-plugin view (python-shared multi-tenant); Native
	// and single-tenant gRPC clients return themselves.
	ForPlugin(pluginID string) PluginClient
	PluginID() string
	Close() error
}
