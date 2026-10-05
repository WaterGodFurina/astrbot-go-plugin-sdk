// Package native is the in-process Native (.so/.dll) transport. It contains no
// gRPC, protobuf-RPC or go-plugin: the host loads the shared library with
// plugin.Open / LoadDLL and calls the exported entry to obtain a PluginService,
// which NativeClient invokes directly. Reverse calls run against an in-process
// host service, also without RPC.
//
// The Native transport passes native Go values straight across the boundary —
// no protobuf, no serialization. (The Windows C-ABI bridge in cabi_*.go uses
// encoding/json only because a C ABI cannot carry Go values.)
package native

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
)

// Plugin is the value returned by the injected Native entry to the host: the
// prepared plugin service plus the in-process reverse-call surface bound to
// this plugin's identity.
type Plugin struct {
	Service sdk.PluginService
	Host    sdk.HostService
}

// Serve prepares p (OnLoad + registry drain) and returns its direct-call
// service together with the in-process host service. It is called by the
// host-injected native_entry.go, not by plugin authors.
func Serve(p *sdk.Plugin, pluginID string) (Plugin, error) {
	hs := sdk.NewHostServiceServer(pluginID, pluginID)
	sdk.SetHostCallerFunc(func() (sdk.HostService, error) { return hs, nil })
	svc := sdk.NewPluginService(p)
	return Plugin{Service: svc, Host: hs}, nil
}

// defaultRPCTimeout bounds a Native call the same way the gRPC transport does;
// in-process calls need no message cap.
const defaultRPCTimeout = 30 * time.Second

// NativeClient is the host-side client for an in-process Native plugin. It
// implements sdk.PluginClient, so host call sites are transport-agnostic.
type NativeClient struct {
	id   string
	svc  sdk.PluginService
	host sdk.HostService
}

var _ sdk.PluginClient = (*NativeClient)(nil)

// bindMu serializes binding the SDK's process-global reverse-call target around
// each host->plugin call. Native plugins share one process (and therefore one
// SDK global), so calls are serialized to keep each plugin's Host identity
// correct.
//
// ponytail: serializes all Native plugin handler calls; per-goroutine identity
// is impossible in Go without API changes. Fine unless many Native plugins run
// hot concurrently.
var bindMu sync.Mutex

func (c *NativeClient) enter() func() {
	bindMu.Lock()
	sdk.SetHostCallerFunc(func() (sdk.HostService, error) { return c.host, nil })
	return bindMu.Unlock
}

// NewClient wraps a prepared Native plugin.
func NewClient(pluginID string, p Plugin) *NativeClient {
	return &NativeClient{id: pluginID, svc: p.Service, host: p.Host}
}

func (c *NativeClient) PluginID() string                  { return c.id }
func (c *NativeClient) ForPlugin(string) sdk.PluginClient { return c }
func (c *NativeClient) Close() error                      { return nil }

func (c *NativeClient) Register(ctx context.Context) (sdk.PluginInfo, error) {
	defer c.enter()()
	return c.svc.Register(ctx, sdk.P1ProtocolVersion)
}

func (c *NativeClient) HandleCommand(ctx context.Context, name string, args []string, event *sdk.Event) (sdk.HandleCommandResult, error) {
	defer c.enter()()
	return c.svc.HandleCommand(ctx, name, args, event)
}

func (c *NativeClient) HandleFilter(ctx context.Context, name string, event *sdk.Event) (sdk.HandleFilterResult, error) {
	defer c.enter()()
	return c.svc.HandleFilter(ctx, name, event)
}

func (c *NativeClient) HandleHook(ctx context.Context, name string, event *sdk.Event, chain []sdk.Component) (sdk.HandleHookResult, error) {
	return c.handleHook(ctx, name, event, chain, nil)
}

func (c *NativeClient) HandleHookWithPayload(ctx context.Context, name string, event *sdk.Event, chain []sdk.Component, payload any) (sdk.HandleHookResult, error) {
	return c.handleHook(ctx, name, event, chain, payload)
}

func (c *NativeClient) handleHook(ctx context.Context, name string, event *sdk.Event, chain []sdk.Component, payload any) (sdk.HandleHookResult, error) {
	defer c.enter()()
	var payloadJSON []byte
	if payload != nil {
		var err error
		if payloadJSON, err = json.Marshal(payload); err != nil {
			return sdk.HandleHookResult{Chain: chain}, err
		}
	}
	return c.svc.HandleHook(ctx, name, event, chain, payloadJSON)
}

func (c *NativeClient) HandleLLMRequest(ctx context.Context, name string, event *sdk.Event, systemPrompt, userPrompt string) (sdk.HandleLLMRequestResult, error) {
	defer c.enter()()
	return c.svc.HandleLLMRequest(ctx, name, event, systemPrompt, userPrompt)
}

func (c *NativeClient) ListWebApis(ctx context.Context) ([]sdk.WebAPIDesc, error) {
	defer c.enter()()
	return c.svc.ListWebApis(ctx)
}

func (c *NativeClient) ListTools(ctx context.Context) ([]sdk.ToolDesc, error) {
	defer c.enter()()
	return c.svc.ListTools(ctx)
}

func (c *NativeClient) GetConfigSchema(ctx context.Context) ([]byte, error) {
	defer c.enter()()
	return c.svc.GetConfigSchema(ctx)
}

func (c *NativeClient) HandleTool(ctx context.Context, name string, args map[string]any, event *sdk.Event) (sdk.HandleToolResult, error) {
	defer c.enter()()
	return c.svc.HandleTool(ctx, name, args, event)
}

func (c *NativeClient) HandleWebRequest(ctx context.Context, req sdk.HandleWebRequest) (sdk.HandleWebResponse, error) {
	defer c.enter()()
	if req.PluginID == "" {
		req.PluginID = c.id
	}
	return c.svc.HandleWebRequest(ctx, req)
}

func (c *NativeClient) HealthCheck(ctx context.Context) (sdk.HealthInfo, error) {
	defer c.enter()()
	return c.svc.HealthCheck(ctx)
}

func (c *NativeClient) Cleanup(ctx context.Context) error {
	defer c.enter()()
	return c.svc.Cleanup(ctx)
}

func (c *NativeClient) SetLogLevel(ctx context.Context, level string) error {
	defer c.enter()()
	return c.svc.SetLogLevel(ctx, level)
}

func (c *NativeClient) FeedSessionWait(ctx context.Context, event *sdk.Event) (bool, error) {
	defer c.enter()()
	res, err := c.svc.FeedSessionWait(ctx, event)
	if err != nil {
		return false, err
	}
	return res.Handled, nil
}

// ManagePlugin / FeedCronJob are python-shared runtime control calls; a Go
// Native plugin does not implement them.
func (c *NativeClient) ManagePlugin(context.Context, sdk.ManagePluginRequest) (sdk.ManagePluginResponse, error) {
	return sdk.ManagePluginResponse{}, sdk.Errorf(sdk.CodeUnimplemented, "ManagePlugin is not implemented by the Go plugin SDK")
}

func (c *NativeClient) FeedCronJob(context.Context, sdk.FeedCronJobRequest) (sdk.FeedCronJobResponse, error) {
	return sdk.FeedCronJobResponse{}, sdk.Errorf(sdk.CodeUnimplemented, "FeedCronJob is not implemented by the Go plugin SDK")
}
