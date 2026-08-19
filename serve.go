package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
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
			// 与 python-sdk 的 STARTUP_ERROR 协议行一致：单行、可被宿主
			// startup_error.go 解析并在 dashboard 展示失败原因（否则宿主
			// 只能观察到子进程退出，无法定位）。
			fmt.Fprintf(os.Stderr, "[ASTRBOT] STARTUP_ERROR phase=plugin_load type=%T plugin=%s error=%s\n",
				err, p.Name, singleLine(err.Error()))
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

// singleLine collapses newlines/tabs in s so it can be embedded in the single
// line [ASTRBOT] STARTUP_ERROR protocol output (mirrors python-sdk).
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\t", " ")
}

func setServiceLogger(l hclog.Logger) {
	logMu.Lock()
	serviceLogger = l
	logMu.Unlock()
}

// logService returns the plugin's hclog logger (created in Serve) or a
// throwaway stderr logger when it is not yet installed, so diagnostic lines
// from serviceServer helpers always have somewhere to go.
func logService() hclog.Logger {
	logMu.Lock()
	defer logMu.Unlock()
	if serviceLogger != nil {
		return serviceLogger
	}
	return hclog.New(&hclog.LoggerOptions{
		Name:   "astrbot-plugin",
		Level:  hclog.Info,
		Output: os.Stderr,
	})
}

// safeErr runs fn and converts a handler panic into an error (instead of
// crashing the plugin process). Returns the panic value as an error with a
// stack trace; on normal completion it returns fn's error.
func safeErr(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
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
		srv, lis, server, err := acceptHostService(broker, HostServiceAppID)
		if err == nil {
			client.setHostServiceServer(srv, lis, server)
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
			var chain []Component
			var err error
			if cerr := safeErr(func() error {
				chain, err = c.ChainHandler(e, req.Args)
				return err
			}); cerr != nil {
				return nil, cerr
			}
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
		var text string
		var err error
		if cerr := safeErr(func() error {
			text, err = c.Handler(e, req.Args)
			return err
		}); cerr != nil {
			return nil, cerr
		}
		if err != nil {
			return nil, err
		}
		resp.Text = text
		return resp, nil
	}
	logService().Warn("HandleCommand: command not found", "name", req.Name)
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
		var allow bool
		if err := safeErr(func() error {
			allow = f.Handler(e)
			return nil
		}); err != nil {
			logService().Error("HandleFilter: handler panic", "name", req.Name, "error", err)
			// 安全降级：拒绝继续传播（允许插件拦截事件）。
			return &sdkv1.HandleFilterResponse{Allow: false, Result: eventResult(true, false)}, nil
		}
		return &sdkv1.HandleFilterResponse{Allow: allow, Result: eventResult(true, false)}, nil
	}
	return &sdkv1.HandleFilterResponse{Allow: true}, nil
}

// decodePayload unmarshals a JSON payload into out, tolerating empty input.
// On corrupt (non-empty, non-decodable) input it logs a warning instead of
// silently producing a zero-value out.
func decodePayload(b []byte, out any) {
	if len(b) == 0 {
		return
	}
	if err := json.Unmarshal(b, out); err != nil {
		logService().Warn("decodePayload: invalid JSON payload, decoding to zero value", "error", err)
	}
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
		var handlerErr error
		if err := safeErr(func() error {
			chain, handlerErr = h.Handler(e, chain)
			return handlerErr
		}); err != nil {
			return nil, err
		}
		if handlerErr != nil {
			return nil, handlerErr
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
		if err := safeErr(func() error { return h.Handler(e, pl) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e, call) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e, call) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e, pe) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(name) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(payloadString(req.PayloadJson)) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(payloadString(req.PayloadJson)) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler() }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e, pl) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e) }); err != nil {
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
		if err := safeErr(func() error { return h.Handler(e) }); err != nil {
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
		var handlerErr error
		if err := safeErr(func() error {
			pr, handlerErr = h.Handler(e, pr)
			return handlerErr
		}); err != nil {
			return nil, err
		}
		if handlerErr != nil {
			return nil, handlerErr
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

// ListTools returns the plugin's current LLM function tools. Plugin tools are
// registered during instantiation (after Register), so this is pulled live on
// each call instead of being captured in the Register snapshot.
func (s *serviceServer) ListTools(context.Context, *sdkv1.Empty) (*sdkv1.ListToolsResponse, error) {
	resp := &sdkv1.ListToolsResponse{}
	if s.impl == nil {
		return resp, nil
	}
	for _, t := range s.impl.Tools {
		schema, err := json.Marshal(t.ParamsSchema)
		if err != nil {
			schema = []byte("{}")
		}
		resp.Tools = append(resp.Tools, &sdkv1.ToolDesc{
			Name:        t.Name,
			Description: t.Description,
			ParamsJson:  schema,
		})
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
		var text string
		var err error
		if cerr := safeErr(func() error {
			text, err = t.Handler(e, args)
			return err
		}); cerr != nil {
			resp.Text = "工具 " + t.Name + " 执行异常: " + cerr.Error()
			resp.IsError = true
			resp.Result = eventResult(true, false)
			return resp, nil
		}
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

// GetConfigSchema returns the plugin's CURRENT config schema (JSON). The host
// pulls it live (e.g. update_manager refreshes runtime schema) and falls back
// to the Register snapshot when this is empty/unimplemented.
func (s *serviceServer) GetConfigSchema(context.Context, *sdkv1.Empty) (*sdkv1.GetConfigSchemaResponse, error) {
	var schema []byte
	if s.impl != nil {
		if b, err := json.Marshal(s.impl.ConfigSchema); err == nil {
			schema = b
		}
	}
	return &sdkv1.GetConfigSchemaResponse{SchemaJson: schema}, nil
}

// FeedSessionWait pushes an inbound message event into the plugin so a
// registered session wait (SessionWait) for the event's unified message origin
// can consume it. Returns handled=true when a wait matched and consumed the
// event; otherwise false.
func (s *serviceServer) FeedSessionWait(_ context.Context, req *sdkv1.FeedSessionWaitRequest) (*sdkv1.FeedSessionWaitResponse, error) {
	if s.impl == nil {
		return &sdkv1.FeedSessionWaitResponse{Handled: false}, nil
	}
	e := eventFromJSON(req.EventJson)
	umo := s.impl.unifiedMsgOriginOf(e)
	if umo == "" {
		return &sdkv1.FeedSessionWaitResponse{Handled: false}, nil
	}
	if w := s.impl.takeSessionWait(umo); w != nil && w.Handler != nil {
		handled := false
		if err := safeErr(func() error {
			handled = w.Handler(e)
			return nil
		}); err != nil {
			logService().Error("FeedSessionWait: wait handler panic", "umo", umo, "error", err)
			return &sdkv1.FeedSessionWaitResponse{Handled: false}, nil
		}
		return &sdkv1.FeedSessionWaitResponse{Handled: handled}, nil
	}
	return &sdkv1.FeedSessionWaitResponse{Handled: false}, nil
}

// webRoutePattern converts a plugin route like "/emoji/<category>" into a
// regex with named groups; plain segments match exactly. Only a whole segment
// of the form "<name>" is treated as a placeholder; a segment like "<a>x" or
// "a>b" is NOT a placeholder and is treated as a literal (aligned with
// python-sdk). Such malformed segments are logged as a warning so the author
// can spot the mistake.
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
		// 整段必须是纯占位符 "<name>"（即 ^<[^>]+>$）才视为动态段；否则按
		// 字面量转义，避免 <a>x 之类畸形段产生意外匹配。
		placeholderRe := regexp.MustCompile(`^<[^>]+>$`)
		if placeholderRe.MatchString(c) {
			m := re.FindStringSubmatch(c)
			if len(m) == 2 && m[1] != "" {
				names = append(names, m[1])
				pattern += "/([^/]+)"
				continue
			}
			logService().Warn("webRoutePattern: 畸形占位符段，按字面量处理", "segment", c)
			pattern += "/" + regexp.QuoteMeta(c)
			continue
		}
		pattern += "/" + regexp.QuoteMeta(c)
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
		var status int
		var respHeaders map[string]string
		var body []byte
		var handlerErr error
		if cerr := safeErr(func() error {
			status, respHeaders, body, handlerErr = w.Handler(method, path, query, headers, req.Body, pathParams)
			return handlerErr
		}); cerr != nil {
			handlerErr = cerr
		}
		if handlerErr != nil {
			errBody, _ := json.Marshal(map[string]string{"status": "error", "message": handlerErr.Error()})
			return &sdkv1.HandleWebRequestResponse{
				StatusCode: 500,
				Body:       errBody,
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
		logService().Warn("eventFromJSON: invalid event JSON, decoding to zero value", "error", err)
		return &Event{}
	}
	return &e
}
