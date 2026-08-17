package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// Handshake is the go-plugin handshake shared between the host and plugins.
// It must match exactly on both sides (defined here once, used by both).
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ASTRBOT_PLUGIN_MAGIC_COOKIE",
	MagicCookieValue: "astrbot-go-plugin-1",
}

// PluginMap is the go-plugin plugin map (single named service). Used by the
// HOST as the client-side map when launching a plugin process.
var PluginMap = map[string]plugin.Plugin{
	"plugin_service": &PluginServiceGRPCPlugin{},
}

// grpcServer is the plugin-side gRPC server factory. It raises the default
// 4MB message cap so large event_json/chain_json payloads (base64 images,
// long conversations) can be received/sent.
func grpcServer(opts []grpc.ServerOption) *grpc.Server {
	opts = append(opts,
		grpc.MaxRecvMsgSize(maxGRPCMessageSize),
		grpc.MaxSendMsgSize(maxGRPCMessageSize),
	)
	return plugin.DefaultGRPCServer(opts)
}

// Serve runs the plugin's main loop: it runs OnLoad, merges any handlers
// registered via RegisterCommand/RegisterFilter/RegisterHook, then registers
// with go-plugin and blocks until the host terminates the process. Call this
// from main().
func Serve(p *Plugin) {
	if p == nil {
		p = &Plugin{}
	}
	if p.OnLoad != nil {
		if err := p.OnLoad(); err != nil {
			fmt.Fprintf(os.Stderr, "astrbot plugin %s OnLoad failed: %v\n", p.Name, err)
			os.Exit(1)
		}
	}
	// Merge imperatively-registered handlers (from init()/OnLoad) into the
	// declarative struct so the Register RPC reports the full handler set.
	global.drain(p)

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "astrbot-plugin." + p.Name,
		Level:  hclog.Info,
		Output: os.Stderr,
	})
	setServiceLogger(logger)
	plugins := map[string]plugin.Plugin{
		"plugin_service": &PluginServiceGRPCPlugin{Impl: p},
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         plugins,
		GRPCServer:      grpcServer,
		Logger:          logger,
	})
}

// serviceLogger is the plugin's hclog logger created in Serve. SetLogLevel
// adjusts its level at runtime; guarded by logMu (Serve runs on the plugin's
// main goroutine, SetLogLevel on a gRPC worker).
var (
	logMu         sync.Mutex
	serviceLogger hclog.Logger
)

func setServiceLogger(l hclog.Logger) {
	logMu.Lock()
	serviceLogger = l
	logMu.Unlock()
}

// PluginServiceGRPCPlugin implements go-plugin's GRPCPlugin for the plugin
// service. The host obtains a *Client from it via GRPCClient.
type PluginServiceGRPCPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	Impl *Plugin
}

// GRPCServer registers the PluginService on the given gRPC server. It also
// captures the go-plugin broker so plugins can dial the host's HostService
// (reverse calls) lazily from handlers.
func (p *PluginServiceGRPCPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	// Store the broker for plugin->host reverse calls (HostService).
	if broker != nil {
		setBroker(broker)
		// go-plugin 的 broker ConnInfo 只在宿主 accept 后约 5s 内有效（内部
		// timeoutWait 会删除未消费的 pending）。插件若不在窗口内 Dial 9000
		// 并保持连接，后续反向调用（GetConfig/ChatLLM/SendMessage 等）会因
		// ConnInfo 过期而永久超时。启动后立即预连接并缓存 hostSvc。
		go func() {
			// 宿主 accept 9000 发生在 Dispense（插件 GRPCServer 之后），
			// 需稍候 ConnInfo 到达；循环重试直到缓存成功。
			for i := 0; i < 20; i++ {
				if _, err := hostServiceClient(); err == nil {
					return
				}
				time.Sleep(250 * time.Millisecond)
			}
		}()
	}
	sdkv1.RegisterPluginServiceServer(s, &serviceServer{impl: p.Impl})
	return nil
}

// GRPCClient wraps the gRPC connection in a typed *Client for the host. It
// also serves the HostService over the broker so plugins can call back into
// the host (CallAction / SendMessage / RecallMessage / GetConfig / SetConfig /
// ChatLLM); the server is stopped when the client is closed.
func (p *PluginServiceGRPCPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	client := NewClient(c)
	if broker != nil {
		srv, lis, err := acceptHostService(broker, HostServiceAppID)
		if err == nil {
			client.setHostServiceServer(srv, lis)
		} else {
			// 打 warn 日志：宿主端未能接受 HostService，插件将失去反向调用
			// 能力（CallAction/SendMessage/GetConfig 等全部不可用）。只记录
			// 错误本身，不含任何业务/敏感信息。
			name := ""
			if p.Impl != nil {
				name = p.Impl.Name
			}
			hclog.New(&hclog.LoggerOptions{
				Name:   "astrbot-plugin." + name,
				Level:  hclog.Info,
				Output: os.Stderr,
			}).Warn("acceptHostService 失败：插件将无法反向调用宿主 HostService", "err", err)
		}
	}
	return client, nil
}

// serviceServer implements sdkv1.PluginServiceServer, dispatching RPCs to the
// plugin author's declared handlers.
type serviceServer struct {
	sdkv1.UnimplementedPluginServiceServer
	impl *Plugin
}

// Register returns the plugin's metadata and handler descriptors.
func (s *serviceServer) Register(context.Context, *sdkv1.RegisterRequest) (*sdkv1.RegisterResponse, error) {
	if s.impl == nil {
		return &sdkv1.RegisterResponse{}, nil
	}
	schema, err := json.Marshal(s.impl.ConfigSchema)
	if err != nil {
		schema = []byte("{}")
	}
	resp := &sdkv1.RegisterResponse{
		Name:             s.impl.Name,
		Version:          s.impl.Version,
		Description:      s.impl.Description,
		Author:           s.impl.Author,
		ConfigSchemaJson: schema,
	}
	for _, c := range s.impl.Commands {
		resp.Commands = append(resp.Commands, &sdkv1.CommandDesc{
			Name:        c.Name,
			Aliases:     c.Aliases,
			Description: c.Description,
			Usage:       c.Usage,
			Permission:  c.Permission,
		})
	}
	for _, f := range s.impl.Filters {
		resp.Filters = append(resp.Filters, &sdkv1.FilterDesc{Name: f.Name})
	}
	for _, h := range s.impl.Hooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: h.Event})
	}
	for _, h := range s.impl.LLMRequestHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: "on_llm_request"})
	}
	for _, h := range s.impl.ResultHooks {
		ev := h.Event
		if ev == "" {
			ev = "on_decorating_result"
		}
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: ev})
	}
	for _, h := range s.impl.MessageHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: h.hookEventName()})
	}
	for _, h := range s.impl.AfterMessageSentHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnAfterMessageSent})
	}
	for _, h := range s.impl.WaitingLLMRequestHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnWaitingLLMRequest})
	}
	for _, h := range s.impl.LLMResponseHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnLLMResponse})
	}
	for _, h := range s.impl.ToolCallHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnUsingLLMTool})
	}
	for _, h := range s.impl.ToolRespondHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnLLMToolRespond})
	}
	for _, h := range s.impl.PluginErrorHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnPluginError})
	}
	for _, h := range s.impl.AstrbotLoadedHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnAstrbotLoaded})
	}
	for _, h := range s.impl.PlatformLoadedHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnPlatformLoaded})
	}
	for _, h := range s.impl.PluginLoadedHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnPluginLoaded})
	}
	for _, h := range s.impl.PluginUnloadedHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnPluginUnloaded})
	}
	for _, h := range s.impl.AgentBeginHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnAgentBegin})
	}
	for _, h := range s.impl.AgentDoneHooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: EventOnAgentDone})
	}
	for _, t := range s.impl.Tools {
		params, err := json.Marshal(t.ParamsSchema)
		if err != nil {
			params = []byte("{}")
		}
		resp.Tools = append(resp.Tools, &sdkv1.ToolDesc{
			Name:        t.Name,
			Description: t.Description,
			ParamsJson:  params,
		})
	}
	for _, w := range s.impl.WebAPIs {
		resp.WebApis = append(resp.WebApis, &sdkv1.WebApiDesc{
			Route:       w.Route,
			Methods:     w.Methods,
			Description: w.Desc,
		})
	}
	return resp, nil
}

// eventResult builds the EventResult sub-message attached to every handler
// response. The Go SDK has no send-operation tracking (unlike the Python
// _has_send_oper), so Sent stays false; StopPropagation mirrors the legacy
// `stop` flag and Handled marks "the handler produced a result". New hosts
// read from `result`; old hosts keep reading the legacy fields, which the
// call sites below still set.
func eventResult(handled, stop bool) *sdkv1.EventResult {
	return &sdkv1.EventResult{
		Handled:         handled,
		StopPropagation: stop,
	}
}

// HandleCommand dispatches to a command handler by name.
func (s *serviceServer) HandleCommand(_ context.Context, req *sdkv1.HandleCommandRequest) (*sdkv1.HandleCommandResponse, error) {
	if s.impl == nil {
		return &sdkv1.HandleCommandResponse{}, nil
	}
	e := eventFromJSON(req.EventJson)
	for _, c := range s.impl.Commands {
		if c.Name != req.Name {
			continue
		}
		resp := &sdkv1.HandleCommandResponse{Result: eventResult(true, false)}
		if c.ChainHandler != nil {
			chain, err := c.ChainHandler(e, req.Args)
			if err != nil {
				return nil, err
			}
			chainJSON, err := json.Marshal(chain)
			if err != nil {
				return nil, err
			}
			resp.ChainJson = chainJSON
			return resp, nil
		}
		if c.Handler == nil {
			return &sdkv1.HandleCommandResponse{}, nil
		}
		text, err := c.Handler(e, req.Args)
		if err != nil {
			return nil, err
		}
		resp.Text = text
		return resp, nil
	}
	return &sdkv1.HandleCommandResponse{}, nil
}

// HandleFilter dispatches to a filter handler by name.
func (s *serviceServer) HandleFilter(_ context.Context, req *sdkv1.HandleFilterRequest) (*sdkv1.HandleFilterResponse, error) {
	if s.impl == nil {
		return &sdkv1.HandleFilterResponse{Allow: true}, nil
	}
	e := eventFromJSON(req.EventJson)
	for _, f := range s.impl.Filters {
		if f.Name != req.Name {
			continue
		}
		if f.Handler == nil {
			return &sdkv1.HandleFilterResponse{Allow: true}, nil
		}
		return &sdkv1.HandleFilterResponse{Allow: f.Handler(e), Result: eventResult(true, false)}, nil
	}
	return &sdkv1.HandleFilterResponse{Allow: true}, nil
}

// decodePayload unmarshals a JSON payload into out, tolerating empty input.
func decodePayload(b []byte, out any) {
	if len(b) == 0 {
		return
	}
	_ = json.Unmarshal(b, out)
}

// HandleHook dispatches to a hook handler by name. Result hooks
// (on_decorating_result / on_result_handling) receive the current result
// chain and may return a decorated one; payload-carrying hooks (on_llm_response,
// on_using_llm_tool, on_llm_tool_respond, on_plugin_error) receive their typed
// payload; generic hooks just run.
func (s *serviceServer) HandleHook(_ context.Context, req *sdkv1.HandleHookRequest) (*sdkv1.HookResponse, error) {
	resp := &sdkv1.HookResponse{Handled: false}
	// markHandled flags "a handler produced a result" on both the legacy
	// `handled` field and the new EventResult sub-message (stop mirrors the
	// legacy `stop` field). Must be called AFTER resp.Stop is finalized.
	markHandled := func() {
		resp.Handled = true
		resp.Result = eventResult(true, resp.Stop)
	}
	if s.impl == nil {
		return resp, nil
	}
	e := eventFromJSON(req.EventJson)

	// Result hooks first (decorate the outgoing chain).
	for _, h := range s.impl.ResultHooks {
		if h.Name != req.Name {
			continue
		}
		ev := h.Event
		if ev == "" {
			ev = "on_decorating_result"
		}
		if ev != "on_decorating_result" && ev != "on_result_handling" {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		var chain []Component
		if len(req.ChainJson) > 0 {
			_ = json.Unmarshal(req.ChainJson, &chain)
		}
		chain, err := h.Handler(e, chain)
		if err != nil {
			return nil, err
		}
		chainJSON, err := json.Marshal(chain)
		if err != nil {
			return nil, err
		}
		resp.ChainJson = chainJSON
		resp.Stop = h.Stop
		markHandled()
		return resp, nil
	}

	// LLM response hooks (payload: LLMResponse).
	for _, h := range s.impl.LLMResponseHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		pl := &LLMResponse{}
		decodePayload(req.PayloadJson, pl)
		if err := h.Handler(e, pl); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}

	// Tool call hooks (payload: ToolCall).
	for _, h := range s.impl.ToolCallHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		call := &ToolCall{}
		decodePayload(req.PayloadJson, call)
		if err := h.Handler(e, call); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}

	// Tool respond hooks (payload: ToolCall).
	for _, h := range s.impl.ToolRespondHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		call := &ToolCall{}
		decodePayload(req.PayloadJson, call)
		if err := h.Handler(e, call); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}

	// Plugin error hooks (payload: PluginError).
	for _, h := range s.impl.PluginErrorHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		pe := &PluginError{}
		decodePayload(req.PayloadJson, pe)
		if err := h.Handler(e, pe); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}

	// Lifecycle hooks with a string payload (platform / plugin name).
	for _, h := range s.impl.PlatformLoadedHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		name := payloadString(req.PayloadJson)
		if err := h.Handler(name); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}
	for _, h := range s.impl.PluginLoadedHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		if err := h.Handler(payloadString(req.PayloadJson)); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}
	for _, h := range s.impl.PluginUnloadedHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		if err := h.Handler(payloadString(req.PayloadJson)); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}
	for _, h := range s.impl.AstrbotLoadedHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		if err := h.Handler(); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}

	// Agent hooks (on_agent_begin / on_agent_done).
	for _, h := range s.impl.AgentBeginHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		if err := h.Handler(e); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}
	for _, h := range s.impl.AgentDoneHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		pl := &LLMResponse{}
		decodePayload(req.PayloadJson, pl)
		if err := h.Handler(e, pl); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}

	// Event-only message hooks (on_message / on_message_received / on_pre_process).
	for _, h := range s.impl.MessageHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		if err := h.Handler(e); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}

	// After-message-sent / waiting-llm-request hooks (event-only).
	for _, h := range s.impl.AfterMessageSentHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		if err := h.Handler(e); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}
	for _, h := range s.impl.WaitingLLMRequestHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		if err := h.Handler(e); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}

	for _, h := range s.impl.Hooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		if err := h.Handler(e); err != nil {
			return nil, err
		}
		markHandled()
		return resp, nil
	}
	return resp, nil
}

// payloadString decodes a payload that is either a bare JSON string or an
// object with a "name"/"platform"/"plugin_name" string field, returning "" when
// empty.
func payloadString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	for _, k := range []string{"plugin_name", "platform", "name"} {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return ""
}

// HandleLLMRequest invokes an on_llm_request hook, letting the plugin modify
// the LLM system prompt before the provider call.
func (s *serviceServer) HandleLLMRequest(_ context.Context, req *sdkv1.HandleLLMRequestRequest) (*sdkv1.HandleLLMRequestResponse, error) {
	resp := &sdkv1.HandleLLMRequestResponse{SystemPrompt: req.SystemPrompt}
	if s.impl == nil {
		return resp, nil
	}
	e := eventFromJSON(req.EventJson)
	for _, h := range s.impl.LLMRequestHooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return resp, nil
		}
		pr := &ProviderRequest{
			SystemPrompt: req.SystemPrompt,
			UserPrompt:   req.UserPrompt,
			Extra:        e.Metadata,
		}
		pr, err := h.Handler(e, pr)
		if err != nil {
			return nil, err
		}
		if pr != nil {
			resp.SystemPrompt = pr.SystemPrompt
			resp.Stop = pr.Stop
		}
		resp.Result = eventResult(true, resp.Stop)
		return resp, nil
	}
	return resp, nil
}

// HandleTool invokes a registered LLM function tool.
func (s *serviceServer) HandleTool(_ context.Context, req *sdkv1.HandleToolRequest) (*sdkv1.HandleToolResponse, error) {
	resp := &sdkv1.HandleToolResponse{}
	if s.impl == nil {
		return resp, nil
	}
	e := eventFromJSON(req.EventJson)
	args := map[string]any{}
	if len(req.ArgsJson) > 0 {
		if err := json.Unmarshal(req.ArgsJson, &args); err != nil {
			resp.Text = "工具参数解析失败: " + err.Error()
			resp.IsError = true
			resp.Result = eventResult(true, false)
			return resp, nil
		}
	}
	for _, t := range s.impl.Tools {
		if t.Name != req.Name {
			continue
		}
		if t.Handler == nil {
			return resp, nil
		}
		text, err := t.Handler(e, args)
		if err != nil {
			resp.Text = "工具 " + t.Name + " 执行失败: " + err.Error()
			resp.IsError = true
			resp.Result = eventResult(true, false)
			return resp, nil
		}
		resp.Text = text
		resp.Result = eventResult(true, false)
		return resp, nil
	}
	resp.Text = "工具 " + req.Name + " 未找到"
	resp.IsError = true
	return resp, nil
}

// HealthCheck reports the plugin's liveness.
func (s *serviceServer) HealthCheck(context.Context, *sdkv1.Empty) (*sdkv1.HealthResponse, error) {
	resp := &sdkv1.HealthResponse{Ok: true}
	if s.impl != nil {
		resp.Version = s.impl.Version
	}
	return resp, nil
}

// SetLogLevel adjusts the plugin's logger level at runtime. The plugin's
// hclog logger (created in Serve) is tuned so DEBUG/INFO/WARNING/ERROR lines
// filter accordingly; "" (or an unknown name) falls back to Info, mirroring
// the host's global level. CRITICAL (an AstrBot level hclog lacks) maps to
// Error, the most restrictive hclog level.
func (s *serviceServer) SetLogLevel(_ context.Context, req *sdkv1.SetLogLevelRequest) (*sdkv1.Empty, error) {
	name := strings.ToUpper(strings.TrimSpace(req.Level))
	if name == "CRITICAL" {
		name = "ERROR"
	}
	lvl := hclog.LevelFromString(name)
	if lvl == hclog.NoLevel {
		lvl = hclog.Info
	}
	logMu.Lock()
	if serviceLogger != nil {
		serviceLogger.SetLevel(lvl)
	}
	logMu.Unlock()
	return &sdkv1.Empty{}, nil
}

// webRoutePattern converts a plugin route like "/emoji/<category>" into a
// regex with named groups; plain segments match exactly.
func webRoutePattern(route string) (*regexp.Regexp, []string, error) {
	normalized := route
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	re := regexp.MustCompile(`<([^>]+)>`)
	var names []string
	chunks := strings.Split(normalized, "/")
	pattern := ""
	for _, c := range chunks {
		if c == "" {
			continue
		}
		m := re.FindStringSubmatch(c)
		if len(m) == 2 {
			name := m[1]
			names = append(names, name)
			pattern += "/" + regexp.QuoteMeta(re.ReplaceAllString(c, "")) + "([^/]+)"
		} else {
			pattern += "/" + regexp.QuoteMeta(c)
		}
	}
	if pattern == "" {
		pattern = "/"
	}
	re, err := regexp.Compile("^" + pattern + "$")
	return re, names, err
}

// HandleWebRequest dispatches a proxied dashboard HTTP request to a
// plugin-registered Web API (WebAPIs). Returns 404 when no route matches.
func (s *serviceServer) HandleWebRequest(_ context.Context, req *sdkv1.HandleWebRequestRequest) (*sdkv1.HandleWebRequestResponse, error) {
	resp := &sdkv1.HandleWebRequestResponse{StatusCode: 404}
	if s.impl == nil {
		return resp, nil
	}
	path := req.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	method := strings.ToUpper(req.Method)
	for _, w := range s.impl.WebAPIs {
		match := false
		for _, m := range w.Methods {
			if strings.EqualFold(m, method) || strings.EqualFold(m, "ANY") {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		re, _, err := webRoutePattern(w.Route)
		if err != nil {
			continue
		}
		sm := re.FindStringSubmatch(path)
		if sm == nil {
			continue
		}
		query := map[string][]string{}
		for _, kv := range req.Query {
			query[kv.Key] = append(query[kv.Key], kv.Value)
		}
		headers := map[string][]string{}
		for _, kv := range req.Headers {
			headers[kv.Key] = append(headers[kv.Key], kv.Value)
		}
		pathParams := map[string]string{}
		names := re.SubexpNames()
		for i, n := range names {
			if i > 0 && n != "" {
				pathParams[n] = sm[i]
			}
		}
		if w.Handler == nil {
			return resp, nil
		}
		status, respHeaders, body, err := w.Handler(method, path, query, headers, req.Body, pathParams)
		if err != nil {
			return &sdkv1.HandleWebRequestResponse{
				StatusCode: 500,
				Body:       []byte(`{"status":"error","message":"` + err.Error() + `"}`),
			}, nil
		}
		out := &sdkv1.HandleWebRequestResponse{StatusCode: int32(status), Body: body}
		for k, v := range respHeaders {
			out.Headers = append(out.Headers, &sdkv1.WebKV{Key: k, Value: v})
		}
		return out, nil
	}
	return resp, nil
}

// Cleanup invokes the plugin's OnUnload hook.
func (s *serviceServer) Cleanup(context.Context, *sdkv1.Empty) (*sdkv1.Empty, error) {
	if s.impl != nil && s.impl.OnUnload != nil {
		return &sdkv1.Empty{}, s.impl.OnUnload()
	}
	return &sdkv1.Empty{}, nil
}

// eventFromJSON decodes a serialized Event, tolerating empty/invalid payloads.
func eventFromJSON(b []byte) *Event {
	if len(b) == 0 {
		return &Event{}
	}
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return &Event{}
	}
	return &e
}
