package sdk

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc"
)

// maxGRPCMessageSize raises the gRPC message cap above the 4MB default so
// large native payloads (base64 images, long conversations)
// can cross the wire between host and plugin.
const maxGRPCMessageSize = 128 << 20 // 128MB

// rpcCallOpts is attached to every host→plugin RPC to lift both send and
// receive limits for that call.
var rpcCallOpts = []grpc.CallOption{
	grpc.MaxCallRecvMsgSize(maxGRPCMessageSize),
	grpc.MaxCallSendMsgSize(maxGRPCMessageSize),
}

// defaultRPCTimeout bounds host→plugin RPC calls (HandleCommand/HandleTool/
// HandleHook/...) so a hung plugin cannot stall the host pipeline forever.
// Callers that pass an already-deadlined context keep their own deadline.
const defaultRPCTimeout = 30 * time.Second

// withTimeout returns ctx unchanged when it already carries a deadline;
// otherwise it returns a child context capped at defaultRPCTimeout. Always
// call the returned cancel func.
func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultRPCTimeout)
}

// logWarnf emits a WARNING line through the plugin's serviceLogger, falling
// back to a stderr hclog logger when Serve has not created one yet. Used on
// degraded-but-continue paths so failures are not silent.
func logWarnf(format string, args ...any) {
	logMu.Lock()
	l := serviceLogger
	logMu.Unlock()
	if l == nil {
		l = hclog.New(&hclog.LoggerOptions{
			Name:   "astrbot-plugin-sdk",
			Level:  hclog.Info,
			Output: os.Stderr,
		})
	}
	l.Warn(fmt.Sprintf(format, args...))
}

// Client is the host-side, typed wrapper around the plugin's gRPC service.
// The host obtains it from go-plugin's Client() (see PluginServiceGRPCPlugin.GRPCClient).
type Client struct {
	conn *grpc.ClientConn
	svc  sdkv1.PluginServiceClient

	// hostSrv/hostLis are the HostService gRPC server the host serves on the
	// broker for this plugin. They are stopped by Close() so reloading a plugin
	// does not leak the listener or its serving goroutine.
	hostSrv *grpc.Server
	hostLis net.Listener
	// hostSrvServer 是 accept 时创建的 per-connection hostServiceServer；
	// Close() 用它清理宿主侧的连接登记与限流表条目（26-3）。
	hostSrvServer *hostServiceServer
}

// NewClient wraps an existing gRPC connection.
func NewClient(conn *grpc.ClientConn) *Client {
	return &Client{
		conn: conn,
		svc:  sdkv1.NewPluginServiceClient(conn),
	}
}

// Register fetches the plugin's metadata and handler descriptors.
func (c *Client) Register(ctx context.Context) (*sdkv1.RegisterResponse, error) {
	resp, err := c.svc.Register(ctx, &sdkv1.RegisterRequest{ProtocolVersion: P1ProtocolVersion}, rpcCallOpts...)
	if err != nil {
		return nil, err
	}
	// P1 协商：插件（serviceServer.Register）已校验 Host 版本；这里校验插件
	// 上报的版本，不匹配明确失败。
	if resp.GetProtocolVersion() != P1ProtocolVersion {
		return nil, status.Errorf(codes.FailedPrecondition,
			"protocol version mismatch: SDK(plugin)=%d Host(P1)=%d; please upgrade the SDK or Host to the same protocol version",
			resp.GetProtocolVersion(), P1ProtocolVersion)
	}
	return resp, nil
}

// normalizeResult resolves a plugin's EventResult for the host: new plugin
// binaries fill `result` (which is returned as-is); OLD plugin binaries
// compiled against a proto without `result` leave it nil, so the legacy
// per-response bool fields (sent/stop/handled) are folded into an EventResult
// here. Host callers can therefore read from the returned (never-nil) result
// uniformly.
func normalizeResult(respResult *sdkv1.EventResult, legacySent, legacyStop, legacyHandled bool) *sdkv1.EventResult {
	if respResult != nil {
		return respResult
	}
	return &sdkv1.EventResult{
		Handled:         legacyHandled,
		Sent:            legacySent,
		StopPropagation: legacyStop,
	}
}

// HandleCommand invokes a command handler, returning its text reply plus an
// optional rich result chain (text + images + files). The *EventResult is
// never nil: `result.Sent` reports whether the plugin performed a send
// operation (legacy plugins fall back to the response's `sent` field).
func (c *Client) HandleCommand(ctx context.Context, name string, args []string, se *sdkv1.SDKEvent) (string, []Component, *sdkv1.EventResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := c.svc.HandleCommand(ctx, &sdkv1.HandleCommandRequest{
		Name:  name,
		Args:  args,
		Event: se,
	}, rpcCallOpts...)
	if err != nil {
		return "", nil, &sdkv1.EventResult{}, err
	}
	return resp.Text, protoToComponents(resp.Chain), normalizeResult(resp.Result, resp.Sent, resp.Stop, false), nil
}

// HandleFilter invokes a filter handler, returning whether the event may
// continue. The *EventResult is never nil: `result.Sent` reports whether the
// plugin sent a message while running the filter (legacy fallback included).
func (c *Client) HandleFilter(ctx context.Context, name string, se *sdkv1.SDKEvent) (bool, *sdkv1.EventResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := c.svc.HandleFilter(ctx, &sdkv1.HandleFilterRequest{Name: name, Event: se}, rpcCallOpts...)
	if err != nil {
		return true, &sdkv1.EventResult{}, err
	}
	return resp.Allow, normalizeResult(resp.Result, resp.Sent, false, false), nil
}

// HandleHook invokes a hook handler. For result-decoration hooks, chain is the
// current result chain and the (possibly decorated) chain is returned. stop is
// the (legacy-derived) pipeline-stop flag; the *EventResult is never nil and
// `result.Sent` reports whether the plugin sent a message while running the
// hook (legacy fallback included).
func (c *Client) HandleHook(ctx context.Context, name string, se *sdkv1.SDKEvent, chain []Component) ([]Component, bool, *sdkv1.EventResult, error) {
	return c.handleHook(ctx, name, se, chain, nil)
}

// HandleHookWithPayload invokes a payload-carrying hook handler (on_llm_response,
// on_using_llm_tool, on_llm_tool_respond, on_plugin_error, lifecycle hooks).
// payload is JSON-marshaled into the RPC; pass nil for event-only hooks.
func (c *Client) HandleHookWithPayload(ctx context.Context, name string, se *sdkv1.SDKEvent, chain []Component, payload any) ([]Component, bool, *sdkv1.EventResult, error) {
	return c.handleHook(ctx, name, se, chain, payload)
}

func (c *Client) handleHook(ctx context.Context, name string, se *sdkv1.SDKEvent, chain []Component, payload any) ([]Component, bool, *sdkv1.EventResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	var payloadJSON []byte
	if payload != nil {
		var err error
		if payloadJSON, err = json.Marshal(payload); err != nil {
			return chain, false, &sdkv1.EventResult{}, err
		}
	}
	resp, err := c.svc.HandleHook(ctx, &sdkv1.HandleHookRequest{Name: name, Event: se, Chain: componentsToProto(chain), PayloadJson: payloadJSON}, rpcCallOpts...)
	if err != nil {
		return chain, false, &sdkv1.EventResult{}, err
	}
	if len(resp.Chain) > 0 {
		chain = protoToComponents(resp.Chain)
	}
	res := normalizeResult(resp.Result, resp.Sent, resp.Stop, resp.Handled)
	return chain, res.StopPropagation, res, nil
}

// HandleLLMRequest invokes an on_llm_request hook, returning the (possibly
// modified) system prompt, the (possibly modified) user prompt, the stop
// flag, and the EventResult (never nil; `result.Sent` reports plugin sends,
// with legacy fallback).
func (c *Client) HandleLLMRequest(ctx context.Context, name string, se *sdkv1.SDKEvent, systemPrompt, userPrompt string) (system, user string, stop bool, res *sdkv1.EventResult, err error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := c.svc.HandleLLMRequest(ctx, &sdkv1.HandleLLMRequestRequest{
		Name:         name,
		Event:        se,
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
	}, rpcCallOpts...)
	if err != nil {
		return systemPrompt, userPrompt, false, &sdkv1.EventResult{}, err
	}
	result := normalizeResult(resp.Result, resp.Sent, resp.Stop, false)
	return resp.SystemPrompt, resp.UserPrompt, result.StopPropagation, result, nil
}

// ListWebApis returns the plugin's current Web API routes (pulled live:
// plugin routes may be registered during instantiation, after Register).
func (c *Client) ListWebApis(ctx context.Context) ([]*sdkv1.WebApiDesc, error) {
	resp, err := c.svc.ListWebApis(ctx, &sdkv1.Empty{}, rpcCallOpts...)
	if err != nil {
		return nil, err
	}
	return resp.GetWebApis(), nil
}

// ListTools returns the plugin's current LLM function tools (pulled live:
// plugin tools are registered during instantiation, after Register).
func (c *Client) ListTools(ctx context.Context) ([]*sdkv1.ToolDesc, error) {
	resp, err := c.svc.ListTools(ctx, &sdkv1.Empty{}, rpcCallOpts...)
	if err != nil {
		return nil, err
	}
	return resp.GetTools(), nil
}

// GetConfigSchema returns the plugin's CURRENT config schema (JSON), which
// plugins may refresh at runtime. The host falls back to the Register snapshot
// when this RPC is unimplemented/empty.
func (c *Client) GetConfigSchema(ctx context.Context) ([]byte, error) {
	resp, err := c.svc.GetConfigSchema(ctx, &sdkv1.Empty{}, rpcCallOpts...)
	if err != nil {
		return nil, err
	}
	return resp.GetSchemaJson(), nil
}

// HandleTool invokes a registered LLM function tool. The *EventResult is never
// nil: `result.Sent` reports whether the plugin sent a message while running
// the tool (legacy fallback included).
func (c *Client) HandleTool(ctx context.Context, name string, args map[string]any, se *sdkv1.SDKEvent) (string, bool, *sdkv1.EventResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", false, &sdkv1.EventResult{}, err
	}
	resp, err := c.svc.HandleTool(ctx, &sdkv1.HandleToolRequest{
		Name:     name,
		ArgsJson: argsJSON,
		Event:    se,
	}, rpcCallOpts...)
	if err != nil {
		return "", false, &sdkv1.EventResult{}, err
	}
	return resp.Text, resp.IsError, normalizeResult(resp.Result, resp.Sent, false, false), nil
}

// HandleWebRequest dispatches a dashboard HTTP request to a plugin-registered
// Web API (the host proxies /api/plug/<plugin>/<path> here). Returns the
// response status, headers and body.
func (c *Client) HandleWebRequest(ctx context.Context, req *sdkv1.HandleWebRequestRequest) (*sdkv1.HandleWebRequestResponse, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	return c.svc.HandleWebRequest(ctx, req, rpcCallOpts...)
}

// HealthCheck probes the plugin's liveness.
func (c *Client) HealthCheck(ctx context.Context) (*sdkv1.HealthResponse, error) {
	return c.svc.HealthCheck(ctx, &sdkv1.Empty{}, rpcCallOpts...)
}

// Cleanup tells the plugin to run its unload hook.
func (c *Client) Cleanup(ctx context.Context) error {
	_, err := c.svc.Cleanup(ctx, &sdkv1.Empty{}, rpcCallOpts...)
	return err
}

// SetLogLevel adjusts the plugin's log level at runtime: the host's per-plugin
// override (DEBUG/INFO/WARNING/ERROR/CRITICAL), or "" to follow the host's
// global level. Old plugin binaries (compiled against a proto without this
// RPC) return UNIMPLEMENTED — the caller should treat that as success.
func (c *Client) SetLogLevel(ctx context.Context, level string) error {
	_, err := c.svc.SetLogLevel(ctx, &sdkv1.SetLogLevelRequest{Level: level}, rpcCallOpts...)
	return err
}

// FeedSessionWait pushes an inbound event to the plugin so a registered
// session wait (session_waiter) can consume it. Returns handled=true when a
// wait consumed the event. Old plugin binaries return UNIMPLEMENTED; the
// caller should treat that as handled=false (no wait registered).
func (c *Client) FeedSessionWait(ctx context.Context, se *sdkv1.SDKEvent) (bool, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := c.svc.FeedSessionWait(ctx, &sdkv1.FeedSessionWaitRequest{Event: se}, rpcCallOpts...)
	if err != nil {
		return false, err
	}
	return resp.GetHandled(), nil
}

// Close releases the underlying gRPC connection and stops the HostService
// server this client served on the broker (if any). Call it after the plugin
// process has been killed so reloads do not leak connections/goroutines.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	// 先 Stop 再清宿主侧状态：Stop 之前在途 RPC（如 Cleanup 钩子里的
	// GetConfig/SetConfig）不会命中"登记已删"状态。
	if c.hostSrv != nil {
		c.hostSrv.Stop()
		c.hostSrv = nil
	}
	if c.hostLis != nil {
		_ = c.hostLis.Close()
		c.hostLis = nil
	}
	if c.hostSrvServer != nil {
		// 清理该连接遗留的宿主侧状态（hostServers 登记 + 限流窗口），
		// 避免表只增不减（26-3）。传 server 本身做归属比对，防止重载
		// 竞态下误删后继连接的登记。
		dropPluginHostState(c.hostSrvServer.connKey, c.hostSrvServer)
		c.hostSrvServer = nil
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// setHostServiceServer records the HostService gRPC server + listener served
// for this client so Close() can release them, plus the per-connection server
// so Close() can drop the plugin's host-side state (26-3).
func (c *Client) setHostServiceServer(srv *grpc.Server, lis net.Listener, server *hostServiceServer) {
	c.hostSrv = srv
	c.hostLis = lis
	c.hostSrvServer = server
}

// ConnTarget returns the gRPC connection target address (diagnostics).
func (c *Client) ConnTarget() string {
	if c == nil || c.conn == nil {
		return ""
	}
	return c.conn.Target()
}
