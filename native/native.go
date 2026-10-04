// Package native is the in-process Native (.so/.dll) transport. It contains no
// gRPC, protobuf-RPC or go-plugin: the host loads the shared library with
// plugin.Open / LoadDLL and calls the exported entry to obtain a PluginService,
// which NativeClient invokes directly. Reverse calls run against an in-process
// host service, also without RPC.
package native

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk"
	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
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

// maxMsgSize is kept for symmetry with the gRPC transport; in-process calls
// need no message cap.
const defaultRPCTimeout = 30 * time.Second

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultRPCTimeout)
}

func normalizeResult(r *sdkv1.EventResult, legacySent, legacyStop, legacyHandled bool) *sdkv1.EventResult {
	if r != nil {
		return r
	}
	return &sdkv1.EventResult{Handled: legacyHandled, Sent: legacySent, StopPropagation: legacyStop}
}

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

func (c *NativeClient) Register(ctx context.Context) (*sdkv1.RegisterResponse, error) {
	defer c.enter()()
	resp, err := c.svc.Register(ctx, &sdkv1.RegisterRequest{ProtocolVersion: sdk.P1ProtocolVersion, PluginId: c.id})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *NativeClient) HandleCommand(ctx context.Context, name string, args []string, se *sdkv1.SDKEvent) (string, []sdk.Component, *sdkv1.EventResult, error) {
	defer c.enter()()
	resp, err := c.svc.HandleCommand(ctx, &sdkv1.HandleCommandRequest{Name: name, Args: args, Event: se, PluginId: c.id})
	if err != nil {
		return "", nil, &sdkv1.EventResult{}, err
	}
	return resp.Text, sdk.ProtoToComponents(resp.Chain), normalizeResult(resp.Result, resp.Sent, resp.Stop, false), nil
}

func (c *NativeClient) HandleFilter(ctx context.Context, name string, se *sdkv1.SDKEvent) (bool, *sdkv1.EventResult, error) {
	defer c.enter()()
	resp, err := c.svc.HandleFilter(ctx, &sdkv1.HandleFilterRequest{Name: name, Event: se, PluginId: c.id})
	if err != nil {
		return true, &sdkv1.EventResult{}, err
	}
	return resp.Allow, normalizeResult(resp.Result, resp.Sent, false, false), nil
}

func (c *NativeClient) HandleHook(ctx context.Context, name string, se *sdkv1.SDKEvent, chain []sdk.Component) ([]sdk.Component, bool, *sdkv1.EventResult, error) {
	return c.handleHook(ctx, name, se, chain, nil)
}

func (c *NativeClient) HandleHookWithPayload(ctx context.Context, name string, se *sdkv1.SDKEvent, chain []sdk.Component, payload any) ([]sdk.Component, bool, *sdkv1.EventResult, error) {
	return c.handleHook(ctx, name, se, chain, payload)
}

func (c *NativeClient) handleHook(ctx context.Context, name string, se *sdkv1.SDKEvent, chain []sdk.Component, payload any) ([]sdk.Component, bool, *sdkv1.EventResult, error) {
	defer c.enter()()
	var payloadJSON []byte
	if payload != nil {
		var err error
		if payloadJSON, err = json.Marshal(payload); err != nil {
			return chain, false, &sdkv1.EventResult{}, err
		}
	}
	resp, err := c.svc.HandleHook(ctx, &sdkv1.HandleHookRequest{Name: name, Event: se, Chain: sdk.ComponentsToProto(chain), PayloadJson: payloadJSON, PluginId: c.id})
	if err != nil {
		return chain, false, &sdkv1.EventResult{}, err
	}
	if len(resp.Chain) > 0 {
		chain = sdk.ProtoToComponents(resp.Chain)
	}
	res := normalizeResult(resp.Result, resp.Sent, resp.Stop, resp.Handled)
	return chain, res.StopPropagation, res, nil
}

func (c *NativeClient) HandleLLMRequest(ctx context.Context, name string, se *sdkv1.SDKEvent, systemPrompt, userPrompt string) (string, string, bool, *sdkv1.EventResult, error) {
	defer c.enter()()
	resp, err := c.svc.HandleLLMRequest(ctx, &sdkv1.HandleLLMRequestRequest{Name: name, Event: se, SystemPrompt: systemPrompt, UserPrompt: userPrompt, PluginId: c.id})
	if err != nil {
		return systemPrompt, userPrompt, false, &sdkv1.EventResult{}, err
	}
	result := normalizeResult(resp.Result, resp.Sent, resp.Stop, false)
	return resp.SystemPrompt, resp.UserPrompt, result.StopPropagation, result, nil
}

func (c *NativeClient) ListWebApis(ctx context.Context, ref *sdkv1.PluginRef) ([]*sdkv1.WebApiDesc, error) {
	defer c.enter()()
	if ref == nil {
		ref = &sdkv1.PluginRef{PluginId: c.id}
	}
	resp, err := c.svc.ListWebApis(ctx, ref)
	if err != nil {
		return nil, err
	}
	return resp.GetWebApis(), nil
}

func (c *NativeClient) ListTools(ctx context.Context, ref *sdkv1.PluginRef) ([]*sdkv1.ToolDesc, error) {
	defer c.enter()()
	if ref == nil {
		ref = &sdkv1.PluginRef{PluginId: c.id}
	}
	resp, err := c.svc.ListTools(ctx, ref)
	if err != nil {
		return nil, err
	}
	return resp.GetTools(), nil
}

func (c *NativeClient) GetConfigSchema(ctx context.Context, ref *sdkv1.PluginRef) ([]byte, error) {
	defer c.enter()()
	if ref == nil {
		ref = &sdkv1.PluginRef{PluginId: c.id}
	}
	resp, err := c.svc.GetConfigSchema(ctx, ref)
	if err != nil {
		return nil, err
	}
	return resp.GetSchemaJson(), nil
}

func (c *NativeClient) HandleTool(ctx context.Context, name string, args map[string]any, se *sdkv1.SDKEvent) (string, bool, *sdkv1.EventResult, error) {
	defer c.enter()()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", false, &sdkv1.EventResult{}, err
	}
	resp, err := c.svc.HandleTool(ctx, &sdkv1.HandleToolRequest{Name: name, ArgsJson: argsJSON, Event: se, PluginId: c.id})
	if err != nil {
		return "", false, &sdkv1.EventResult{}, err
	}
	return resp.Text, resp.IsError, normalizeResult(resp.Result, resp.Sent, false, false), nil
}

func (c *NativeClient) HandleWebRequest(ctx context.Context, req *sdkv1.HandleWebRequestRequest) (*sdkv1.HandleWebRequestResponse, error) {
	defer c.enter()()
	if req != nil && req.PluginId == "" {
		req.PluginId = c.id
	}
	return c.svc.HandleWebRequest(ctx, req)
}

func (c *NativeClient) HealthCheck(ctx context.Context) (*sdkv1.HealthResponse, error) {
	defer c.enter()()
	return c.svc.HealthCheck(ctx, &sdkv1.Empty{})
}

func (c *NativeClient) Cleanup(ctx context.Context, ref *sdkv1.PluginRef) error {
	defer c.enter()()
	if ref == nil {
		ref = &sdkv1.PluginRef{PluginId: c.id}
	}
	_, err := c.svc.Cleanup(ctx, ref)
	return err
}

func (c *NativeClient) SetLogLevel(ctx context.Context, level string) error {
	defer c.enter()()
	_, err := c.svc.SetLogLevel(ctx, &sdkv1.SetLogLevelRequest{Level: level, PluginId: c.id})
	return err
}

func (c *NativeClient) FeedSessionWait(ctx context.Context, se *sdkv1.SDKEvent) (bool, error) {
	defer c.enter()()
	resp, err := c.svc.FeedSessionWait(ctx, &sdkv1.FeedSessionWaitRequest{Event: se, PluginId: c.id})
	if err != nil {
		return false, err
	}
	return resp.GetHandled(), nil
}

func (c *NativeClient) ManagePlugin(ctx context.Context, req *sdkv1.ManagePluginRequest) (*sdkv1.ManagePluginResponse, error) {
	defer c.enter()()
	if req != nil && req.PluginId == "" {
		req.PluginId = c.id
	}
	return c.svc.ManagePlugin(ctx, req)
}

func (c *NativeClient) FeedCronJob(ctx context.Context, req *sdkv1.FeedCronJobRequest) (*sdkv1.FeedCronJobResponse, error) {
	defer c.enter()()
	if req != nil && req.PluginId == "" {
		req.PluginId = c.id
	}
	return c.svc.FeedCronJob(ctx, req)
}
