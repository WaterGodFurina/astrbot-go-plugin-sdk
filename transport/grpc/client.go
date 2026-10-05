package grpctransport

import (
	"context"
	"encoding/json"
	"net"
	"time"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1"
	sdkv1grpc "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// Client is the host-side, typed wrapper around the plugin's gRPC service.
// The host obtains it from go-plugin's Client() (see PluginServiceGRPCPlugin.GRPCClient).
var _ sdk.PluginClient = (*Client)(nil)

type Client struct {
	conn *grpc.ClientConn
	svc  sdkv1grpc.PluginServiceClient

	// pluginID 是多租户（python-shared 共享 Runtime）下本次调用目标插件的
	// plugin_id；单插件进程为空。经 ForPlugin(id) 派生 per-plugin 视图后，
	// 所有带 plugin_id 字段的 RPC 自动填充它，宿主调用点无需改动。
	pluginID string
	// ownsConn 标记本 Client 是否拥有底层连接（Close 时是否关闭）。ForPlugin
	// 派生的共享视图 ownsConn=false，避免单个插件卸载关闭整个共享 Runtime。
	ownsConn bool

	// hostSrv/hostLis are the HostService gRPC server the host serves on the
	// broker for this plugin. They are stopped by Close() so reloading a plugin
	// does not leak the listener or its serving goroutine.
	hostSrv *grpc.Server
	hostLis net.Listener
	// hostSrvServer 是 accept 时创建的 per-connection sdk.HostServiceServer；
	// Close() 用它清理宿主侧的连接登记与限流表条目（26-3）。
	hostSrvServer *sdk.HostServiceServer
}

// NewClient wraps an existing gRPC connection.
func NewClient(conn *grpc.ClientConn) *Client {
	return &Client{
		conn:     conn,
		svc:      sdkv1grpc.NewPluginServiceClient(conn),
		ownsConn: true,
	}
}

// ForPlugin 派生一个绑定 plugin_id 的共享视图（python-shared 多租户）：
// 复用同一 gRPC 连接，之后所有 PluginService 调用自动携带该 plugin_id，
// 宿主无需逐调用点改签名。Close() 不会关闭共享连接（ownsConn=false）。
func (c *Client) ForPlugin(pluginID string) sdk.PluginClient {
	if c == nil {
		return nil
	}
	if pluginID == "" {
		return c
	}
	return &Client{
		conn:     c.conn,
		svc:      c.svc,
		pluginID: pluginID,
		ownsConn: false,
	}
}

// PluginID 返回该 Client 视图绑定的 plugin_id（单插件进程为空）。
func (c *Client) PluginID() string {
	if c == nil {
		return ""
	}
	return c.pluginID
}

// Register fetches the plugin's metadata and handler descriptors.
func (c *Client) Register(ctx context.Context) (sdk.PluginInfo, error) {
	resp, err := c.svc.Register(ctx, &sdkv1.RegisterRequest{ProtocolVersion: sdk.P1ProtocolVersion, PluginId: c.pluginID}, rpcCallOpts...)
	if err != nil {
		return sdk.PluginInfo{}, err
	}
	// P1 协商：插件（serviceServer.Register）已校验 Host 版本；这里校验插件
	// 上报的版本，不匹配明确失败。
	if resp.GetProtocolVersion() != sdk.P1ProtocolVersion {
		return sdk.PluginInfo{}, status.Errorf(codes.FailedPrecondition,
			"protocol version mismatch: SDK(plugin)=%d Host(P1)=%d; please upgrade the SDK or Host to the same protocol version",
			resp.GetProtocolVersion(), sdk.P1ProtocolVersion)
	}
	return protoToPluginInfo(resp), nil
}

// normalizeResult resolves a plugin's EventResult for the host: new plugin
// binaries fill `result` (which is returned as-is); OLD plugin binaries
// compiled against a proto without `result` leave it nil, so the legacy
// per-response bool fields (sent/stop/handled) are folded into an EventResult
// here. Host callers can therefore read from the returned (never-nil) result
// uniformly.
func normalizeResult(respResult *sdkv1.EventResult, legacySent, legacyStop, legacyHandled bool) sdk.EventResult {
	if respResult != nil {
		return protoToEventResult(respResult)
	}
	return sdk.EventResult{
		Handled:         legacyHandled,
		Sent:            legacySent,
		StopPropagation: legacyStop,
	}
}

// HandleCommand invokes a command handler, returning its text reply plus an
// optional rich result chain (text + images + files).
func (c *Client) HandleCommand(ctx context.Context, name string, args []string, event *sdk.Event) (sdk.HandleCommandResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := c.svc.HandleCommand(ctx, &sdkv1.HandleCommandRequest{
		Name:     name,
		Args:     args,
		Event:    EventToSDKEvent(event),
		PluginId: c.pluginID,
	}, rpcCallOpts...)
	if err != nil {
		return sdk.HandleCommandResult{}, err
	}
	return sdk.HandleCommandResult{
		Text:   resp.Text,
		Chain:  protoToComponents(resp.Chain),
		Result: normalizeResult(resp.Result, resp.Sent, resp.Stop, false),
	}, nil
}

// HandleFilter invokes a filter handler, returning whether the event may
// continue.
func (c *Client) HandleFilter(ctx context.Context, name string, event *sdk.Event) (sdk.HandleFilterResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := c.svc.HandleFilter(ctx, &sdkv1.HandleFilterRequest{Name: name, Event: EventToSDKEvent(event), PluginId: c.pluginID}, rpcCallOpts...)
	if err != nil {
		return sdk.HandleFilterResult{Allow: true}, err
	}
	return sdk.HandleFilterResult{
		Allow:  resp.Allow,
		Result: normalizeResult(resp.Result, resp.Sent, false, false),
	}, nil
}

// HandleHook invokes a hook handler. For result-decoration hooks, chain is the
// current result chain and the (possibly decorated) chain is returned.
func (c *Client) HandleHook(ctx context.Context, name string, event *sdk.Event, chain []sdk.Component) (sdk.HandleHookResult, error) {
	return c.handleHook(ctx, name, event, chain, nil)
}

// HandleHookWithPayload invokes a payload-carrying hook handler (on_llm_response,
// on_using_llm_tool, on_llm_tool_respond, on_plugin_error, lifecycle hooks).
// payload is JSON-marshaled into the RPC; pass nil for event-only hooks.
func (c *Client) HandleHookWithPayload(ctx context.Context, name string, event *sdk.Event, chain []sdk.Component, payload any) (sdk.HandleHookResult, error) {
	return c.handleHook(ctx, name, event, chain, payload)
}

func (c *Client) handleHook(ctx context.Context, name string, event *sdk.Event, chain []sdk.Component, payload any) (sdk.HandleHookResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	var payloadJSON []byte
	if payload != nil {
		var err error
		if payloadJSON, err = json.Marshal(payload); err != nil {
			return sdk.HandleHookResult{Chain: chain}, err
		}
	}
	resp, err := c.svc.HandleHook(ctx, &sdkv1.HandleHookRequest{
		Name:        name,
		Event:       EventToSDKEvent(event),
		Chain:       componentsToProto(chain),
		PayloadJson: payloadJSON,
		PluginId:    c.pluginID,
	}, rpcCallOpts...)
	if err != nil {
		return sdk.HandleHookResult{Chain: chain}, err
	}
	if len(resp.Chain) > 0 {
		chain = protoToComponents(resp.Chain)
	}
	return sdk.HandleHookResult{
		Chain:  chain,
		Result: normalizeResult(resp.Result, resp.Sent, resp.Stop, resp.Handled),
	}, nil
}

// HandleLLMRequest invokes an on_llm_request hook, returning the (possibly
// modified) system prompt, the (possibly modified) user prompt, the stop flag,
// and the EventResult.
func (c *Client) HandleLLMRequest(ctx context.Context, name string, event *sdk.Event, systemPrompt, userPrompt string) (sdk.HandleLLMRequestResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := c.svc.HandleLLMRequest(ctx, &sdkv1.HandleLLMRequestRequest{
		Name:         name,
		Event:        EventToSDKEvent(event),
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		PluginId:     c.pluginID,
	}, rpcCallOpts...)
	if err != nil {
		return sdk.HandleLLMRequestResult{SystemPrompt: systemPrompt, UserPrompt: userPrompt}, err
	}
	result := normalizeResult(resp.Result, resp.Sent, resp.Stop, false)
	return sdk.HandleLLMRequestResult{
		SystemPrompt: resp.SystemPrompt,
		UserPrompt:   resp.UserPrompt,
		Stop:         result.StopPropagation,
		Result:       result,
	}, nil
}

// ListTools returns the plugin's current LLM function tools (pulled live:
// plugin tools are registered during instantiation, after Register).
func (c *Client) ListTools(ctx context.Context) ([]sdk.ToolDesc, error) {
	resp, err := c.svc.ListTools(ctx, &sdkv1.PluginRef{PluginId: c.pluginID}, rpcCallOpts...)
	if err != nil {
		return nil, err
	}
	out := make([]sdk.ToolDesc, 0, len(resp.GetTools()))
	for _, t := range resp.GetTools() {
		out = append(out, protoToToolDesc(t))
	}
	return out, nil
}

// ListWebApis returns the plugin's current Web API routes (pulled live:
// plugin routes may be registered during instantiation, after Register).
func (c *Client) ListWebApis(ctx context.Context) ([]sdk.WebAPIDesc, error) {
	resp, err := c.svc.ListWebApis(ctx, &sdkv1.PluginRef{PluginId: c.pluginID}, rpcCallOpts...)
	if err != nil {
		return nil, err
	}
	out := make([]sdk.WebAPIDesc, 0, len(resp.GetWebApis()))
	for _, w := range resp.GetWebApis() {
		out = append(out, protoToWebAPIDesc(w))
	}
	return out, nil
}

// GetConfigSchema returns the plugin's CURRENT config schema (JSON), which
// plugins may refresh at runtime. The host falls back to the Register snapshot
// when this RPC is unimplemented/empty.
func (c *Client) GetConfigSchema(ctx context.Context) ([]byte, error) {
	resp, err := c.svc.GetConfigSchema(ctx, &sdkv1.PluginRef{PluginId: c.pluginID}, rpcCallOpts...)
	if err != nil {
		return nil, err
	}
	return resp.GetSchemaJson(), nil
}

// HandleTool invokes a registered LLM function tool.
func (c *Client) HandleTool(ctx context.Context, name string, args map[string]any, event *sdk.Event) (sdk.HandleToolResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return sdk.HandleToolResult{}, err
	}
	resp, err := c.svc.HandleTool(ctx, &sdkv1.HandleToolRequest{
		Name:     name,
		ArgsJson: argsJSON,
		Event:    EventToSDKEvent(event),
		PluginId: c.pluginID,
	}, rpcCallOpts...)
	if err != nil {
		return sdk.HandleToolResult{}, err
	}
	return sdk.HandleToolResult{
		Text:    resp.Text,
		IsError: resp.IsError,
		Result:  normalizeResult(resp.Result, resp.Sent, false, false),
	}, nil
}

// HandleWebRequest dispatches a dashboard HTTP request to a plugin-registered
// Web API (the host proxies /api/plug/<plugin>/<path> here). Returns the
// response status, headers and body.
func (c *Client) HandleWebRequest(ctx context.Context, req sdk.HandleWebRequest) (sdk.HandleWebResponse, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	pluginID := req.PluginID
	if pluginID == "" {
		pluginID = c.pluginID
	}
	resp, err := c.svc.HandleWebRequest(ctx, &sdkv1.HandleWebRequestRequest{
		PluginId: pluginID,
		Method:   req.Method,
		Path:     req.Path,
		Query:    mapToWebKV(req.Query),
		Headers:  mapToWebKV(req.Headers),
		Body:     req.Body,
		Files:    webUploadFilesToProto(req.Files),
	}, rpcCallOpts...)
	if err != nil {
		return sdk.HandleWebResponse{}, err
	}
	return sdk.HandleWebResponse{
		StatusCode: int(resp.GetStatusCode()),
		Headers:    webKVToStrMap(resp.GetHeaders()),
		Body:       resp.GetBody(),
	}, nil
}

// HealthCheck probes the plugin's liveness.
func (c *Client) HealthCheck(ctx context.Context) (sdk.HealthInfo, error) {
	resp, err := c.svc.HealthCheck(ctx, &sdkv1.Empty{}, rpcCallOpts...)
	if err != nil {
		return sdk.HealthInfo{}, err
	}
	return healthFromProto(resp), nil
}

// healthFromProto maps the wire HealthResponse (incl. runtime-reported plugin
// status) to the native HealthInfo.
func healthFromProto(resp *sdkv1.HealthResponse) sdk.HealthInfo {
	out := sdk.HealthInfo{
		OK:               resp.GetOk(),
		Load:             resp.GetLoad(),
		Version:          resp.GetVersion(),
		RuntimeHeartbeat: resp.GetRuntimeHeartbeat(),
	}
	for _, ps := range resp.GetPlugins() {
		out.Plugins = append(out.Plugins, sdk.PluginStatus{
			PluginID:     ps.GetPluginId(),
			PluginName:   ps.GetPluginName(),
			State:        ps.GetState(),
			Health:       ps.GetHealth(),
			LastActivity: ps.GetLastActivity(),
			Error:        ps.GetError(),
			Generation:   ps.GetGeneration(),
		})
	}
	return out
}

// Cleanup tells the plugin to run its unload hook.
func (c *Client) Cleanup(ctx context.Context) error {
	_, err := c.svc.Cleanup(ctx, &sdkv1.PluginRef{PluginId: c.pluginID}, rpcCallOpts...)
	return err
}

// SetLogLevel adjusts the plugin's log level at runtime: the host's per-plugin
// override (DEBUG/INFO/WARNING/ERROR/CRITICAL), or "" to follow the host's
// global level. Old plugin binaries (compiled against a proto without this
// RPC) return UNIMPLEMENTED — the caller should treat that as success.
func (c *Client) SetLogLevel(ctx context.Context, level string) error {
	_, err := c.svc.SetLogLevel(ctx, &sdkv1.SetLogLevelRequest{Level: level, PluginId: c.pluginID}, rpcCallOpts...)
	return err
}

// FeedSessionWait pushes an inbound event to the plugin so a registered
// session wait (session_waiter) can consume it. Returns handled=true when a
// wait consumed the event. Old plugin binaries return UNIMPLEMENTED; the
// caller should treat that as handled=false (no wait registered).
func (c *Client) FeedSessionWait(ctx context.Context, event *sdk.Event) (bool, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := c.svc.FeedSessionWait(ctx, &sdkv1.FeedSessionWaitRequest{Event: EventToSDKEvent(event), PluginId: c.pluginID}, rpcCallOpts...)
	if err != nil {
		return false, err
	}
	return resp.GetHandled(), nil
}

// ManagePlugin 管理共享 Runtime（python-shared）内的插件成员：action="load"
// 加载/登记插件（随后对同一 plugin_id 调 Register 触发实例化），
// action="unload" 卸载单个插件（不杀共享 Runtime 进程）。单插件进程返回
// UNIMPLEMENTED；宿主应只对共享 Runtime 调用。
func (c *Client) ManagePlugin(ctx context.Context, req sdk.ManagePluginRequest) (sdk.ManagePluginResponse, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	pluginID := req.PluginID
	if pluginID == "" {
		pluginID = c.pluginID
	}
	resp, err := c.svc.ManagePlugin(ctx, &sdkv1.ManagePluginRequest{
		Action:     req.Action,
		PluginId:   pluginID,
		PluginDir:  req.PluginDir,
		PluginName: req.PluginName,
		Version:    req.Version,
	}, rpcCallOpts...)
	if err != nil {
		return sdk.ManagePluginResponse{}, err
	}
	return sdk.ManagePluginResponse{OK: resp.GetOk(), Error: resp.GetError()}, nil
}

// FeedCronJob pushes a cron trigger to the plugin so the handler of a basic
// job registered via CronCreate (payload tagged with _plugin_id) runs in the
// plugin process. Returns handled=false when the plugin has no matching
// handler. Old plugin binaries return UNIMPLEMENTED; the caller should treat
// that as handled=false (job fired without a plugin handler).
func (c *Client) FeedCronJob(ctx context.Context, req sdk.FeedCronJobRequest) (sdk.FeedCronJobResponse, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	pluginID := req.PluginID
	if pluginID == "" {
		pluginID = c.pluginID
	}
	resp, err := c.svc.FeedCronJob(ctx, &sdkv1.FeedCronJobRequest{
		JobId:       req.JobID,
		JobName:     req.JobName,
		PayloadJson: req.PayloadJSON,
		RunAt:       req.RunAt,
		PluginId:    pluginID,
	}, rpcCallOpts...)
	if err != nil {
		return sdk.FeedCronJobResponse{}, err
	}
	return sdk.FeedCronJobResponse{Handled: resp.GetHandled()}, nil
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
		sdk.DropPluginHostState(c.hostSrvServer.ConnKey(), c.hostSrvServer)
		c.hostSrvServer = nil
	}
	if c.ownsConn && c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// setHostServiceServer records the HostService gRPC server + listener served
// for this client so Close() can release them, plus the per-connection server
// so Close() can drop the plugin's host-side state (26-3).
func (c *Client) setHostServiceServer(srv *grpc.Server, lis net.Listener, server *sdk.HostServiceServer) {
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
