package sdk

import (
	"encoding/json"
	"fmt"
	"sync"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
)

// Command is a message command a plugin accepts. The plugin author only has
// to fill in the descriptor fields and the Handler.
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	// Permission restricts who may run the command: "everyone" (default) or
	// "admin". The value is case-insensitive; anything else is normalized to
	// "everyone" when the command is registered (see registry.drain).
	Permission string
	// ParentGroup is the command group this command belongs to ("" = top-level).
	// When set, the command is treated as a sub-command of that group.
	ParentGroup string
	// IsSubCommand marks this command as a sub-command of ParentGroup.
	IsSubCommand bool
	Handler      func(e *Event, args []string) (string, error)
	// ChainHandler, when set, is preferred over Handler and lets the command
	// return a full message chain (text + image + file components).
	ChainHandler func(e *Event, args []string) ([]Component, error)
}

// Filter is an event filter. Returning false stops propagation of the event
// (mirrors the host's filter semantics).
type Filter struct {
	Name    string
	Handler func(e *Event) bool
}

// Hook is a lifecycle or pipeline hook (e.g. Event "startup", "on_message").
type Hook struct {
	Name    string
	Event   string
	Handler func(e *Event) error
}

// Config exposes the plugin's persisted config (plugins/<name>/config.json).
type Config struct {
	Data map[string]any
}

// Get returns a config value by key.
func (c *Config) Get(key string) (any, bool) {
	if c == nil || c.Data == nil {
		return nil, false
	}
	v, ok := c.Data[key]
	return v, ok
}

// GetString returns a string config value, or "" if missing/not a string.
func (c *Config) GetString(key string) string {
	v, ok := c.Get(key)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// GetBool returns a bool config value, or false.
func (c *Config) GetBool(key string) bool {
	v, ok := c.Get(key)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// Plugin is the declarative plugin definition. Fill it out and pass it to
// Serve() from your main function. Handlers can also be registered
// imperatively via RegisterCommand/RegisterFilter/RegisterHook; those are
// merged in at Serve time (after OnLoad runs).
type Plugin struct {
	Name         string
	Version      string
	Description  string
	Author       string
	ConfigSchema map[string]any
	Commands     []Command
	Filters      []Filter
	Hooks        []Hook
	// Tools are LLM function tools exposed to the model during chat (mirrors
	// Python AstrBot's @filter.llm_tool / register_llm_tool).
	Tools []Tool
	// LLMRequestHooks run before the LLM provider call (Event "on_llm_request"),
	// letting the plugin inspect/modify the system prompt.
	LLMRequestHooks []LLMRequestHook
	// ResultHooks decorate the outgoing reply chain (Event "on_decorating_result"
	// or "on_result_handling").
	ResultHooks []ResultHook

	// MessageHooks observe incoming messages (Event "on_message" /
	// "on_message_received" / "on_pre_process").
	MessageHooks []MessageHook
	// AfterMessageSentHooks fire after the bot's reply is sent
	// (Event "on_after_message_sent").
	AfterMessageSentHooks []AfterMessageSentHook
	// WaitingLLMRequestHooks fire before the LLM call queues
	// (Event "on_waiting_llm_request").
	WaitingLLMRequestHooks []WaitingLLMRequestHook
	// LLMResponseHooks fire after the LLM reply is produced
	// (Event "on_llm_response").
	LLMResponseHooks []LLMResponseHook
	// ToolCallHooks fire before an LLM function tool executes
	// (Event "on_using_llm_tool").
	ToolCallHooks []ToolCallHook
	// ToolRespondHooks fire after an LLM function tool executes
	// (Event "on_llm_tool_respond").
	ToolRespondHooks []ToolRespondHook
	// PluginErrorHooks fire when a plugin handler errors out
	// (Event "on_plugin_error").
	PluginErrorHooks []PluginErrorHook
	// AstrbotLoadedHooks fire after the host finishes loading
	// (Event "on_astrbot_loaded").
	AstrbotLoadedHooks []AstrbotLoadedHook
	// PlatformLoadedHooks fire after a platform adapter finishes loading
	// (Event "on_platform_loaded").
	PlatformLoadedHooks []PlatformLoadedHook
	// PluginLoadedHooks fire after a plugin finishes loading
	// (Event "on_plugin_loaded").
	PluginLoadedHooks []PluginLoadedHook
	// PluginUnloadedHooks fire after a plugin is unloaded
	// (Event "on_plugin_unloaded").
	PluginUnloadedHooks []PluginUnloadedHook
	// AgentBeginHooks fire when an agent run begins (Event "on_agent_begin").
	AgentBeginHooks []AgentBeginHook
	// AgentDoneHooks fire when an agent run finishes (Event "on_agent_done").
	AgentDoneHooks []AgentDoneHook
	// WebAPIs are dashboard Web UI API routes served by the host under
	// /api/plug/<plugin>/<route> (mirrors Python's context.register_web_api).
	WebAPIs []WebAPI
	// OnLoad runs inside the plugin process right before the RPC server starts.
	// Use it for setup / dynamic handler registration. A returned error aborts
	// plugin startup.
	OnLoad func() error
	// OnConfig 目前仅供声明，SDK 尚未在运行时主动调用它（宿主端配置变更
	// 暂不主动推送），属已知缺口（见 README/ROADMAP）。插件可通过
	// Host.GetConfig 主动读取配置。
	OnConfig func(cfg *Config) error
	OnUnload func() error

	// sessionWaitsMu 保护 sessionWaits 注册表。
	sessionWaitsMu sync.Mutex
	// sessionWaits 是按 unified_msg_origin 注册的会话等待。宿主收到该 umo
	// 的下一条消息时经 PluginService.FeedSessionWait 推送，匹配到即触发。
	sessionWaits map[string]*SessionWait
}

// Tool is an LLM function tool the plugin exposes to the model. When the model
// calls it, Handler runs inside the plugin process and its return text is fed
// back as the tool result message.
type Tool struct {
	Name        string
	Description string
	// ParamsSchema is the JSON object schema of the tool arguments
	// ({"type":"object","properties":{...},"required":[...]}).
	ParamsSchema map[string]any
	Handler      func(e *Event, args map[string]any) (string, error)
}

// WebAPI is a dashboard Web UI API route served by the host under
// /api/plug/<plugin>/<route>. Route supports dynamic <param> segments (e.g.
// "/emoji/<category>"), matched at request time and passed to Handler.
type WebAPI struct {
	Route   string   // e.g. "/emoji/<category>"
	Methods []string // e.g. []string{"GET"}
	Desc    string
	// Handler processes the proxied HTTP request. query/headers are multi-value
	// maps, pathParams holds the dynamic route values. Returns the HTTP status
	// code, response headers and response body.
	Handler func(method, path string, query, headers map[string][]string, body []byte, pathParams map[string]string) (int, map[string]string, []byte, error)
}

// ProviderRequest is a serializable view of an LLM request that on_llm_request
// hooks inspect and may modify. Only the fields a plugin commonly touches are
// typed; everything else is carried in Extra.
type ProviderRequest struct {
	SystemPrompt string         `json:"system_prompt"`
	UserPrompt   string         `json:"user_prompt"`
	Extra        map[string]any `json:"extra,omitempty"`
	// Stop, when true, tells the host to abort the LLM call for this message.
	Stop bool `json:"stop,omitempty"`
}

// LLMRequestHook runs before the LLM provider call (Event "on_llm_request").
// The host passes the assembled system prompt; the returned (or mutated) req
// is applied before the provider request is built.
type LLMRequestHook struct {
	Name    string
	Handler func(e *Event, req *ProviderRequest) (*ProviderRequest, error)
}

// ResultHook decorates the outgoing reply chain before it is sent
// (Event "on_decorating_result"). Handler receives the current result chain
// and returns the (possibly modified) chain.
type ResultHook struct {
	Name    string
	Event   string // "on_decorating_result" (default) / "on_result_handling"
	Handler func(e *Event, chain []Component) ([]Component, error)
	// Stop halts the pipeline after this hook runs.
	Stop bool
}

// SessionWait represents a pending wait for the next message on a unified
// message origin (umo). Registered via Plugin.RegisterSessionWait; the host
// pushes the next matching event via PluginService.FeedSessionWait, which
// triggers Handler once and removes the wait. Aligns with Python AstrBot's
// session_waiter.
type SessionWait struct {
	// UMO is the unified message origin this wait listens on
	// (e.g. "aiocqhttp:GroupMessage:123").
	UMO string
	// WaitID 是宿主 RegisterSessionWait 返回的凭据；Unregister 时回传，
	// 宿主据此做归属校验。空表示宿主不支持或注册失败。
	WaitID string
	// Handler consumes the matched event. Returning true marks the event as
	// handled (consumed); false lets it fall through to normal handling.
	Handler func(e *Event) bool
}

// RegisterSessionWait registers a wait for the next message on umo. The host
// (via its session-wait registry) is asked to push matching events; when the
// next event for umo arrives, Handler runs once and the wait is removed.
// timeoutSec sets the wait's timeout on the host side (<=0 means no timeout /
// host default). Returns the registered wait.
func (p *Plugin) RegisterSessionWait(umo string, timeoutSec int, handler func(e *Event) bool) *SessionWait {
	w := &SessionWait{UMO: umo, Handler: handler}
	p.sessionWaitsMu.Lock()
	if p.sessionWaits == nil {
		p.sessionWaits = map[string]*SessionWait{}
	}
	p.sessionWaits[umo] = w
	p.sessionWaitsMu.Unlock()

	// 反向告知宿主注册等待；失败时打 Warn（宿主不支持该特性时返回空 wait_id）。
	if svc, err := hostServiceClient(); err != nil {
		logService().Warn("RegisterSessionWait 无法连接宿主 HostService，等待可能永远不会触发（是否在 OnLoad 中过早注册？）",
			"umo", umo, "err", err)
	} else {
		ctx, cancel := hostRPCCtx()
		defer cancel()
		if resp, rerr := svc.RegisterSessionWait(ctx, &sdkv1.RegisterSessionWaitRequest{
			Umo:            umo,
			TimeoutSeconds: int32(timeoutSec),
		}); rerr != nil {
			logService().Warn("RegisterSessionWait 上报宿主失败", "umo", umo, "err", rerr)
		} else if resp != nil {
			p.sessionWaitsMu.Lock()
			w.WaitID = resp.GetWaitId()
			p.sessionWaitsMu.Unlock()
		}
	}
	return w
}

// UnregisterSessionWait removes a previously registered session wait for umo.
func (p *Plugin) UnregisterSessionWait(umo string) {
	p.sessionWaitsMu.Lock()
	w := p.sessionWaits[umo]
	delete(p.sessionWaits, umo)
	p.sessionWaitsMu.Unlock()
	// 优先回传宿主发放的 wait_id（归属校验凭据）；无凭据时回退 umo。
	waitID := umo
	if w != nil && w.WaitID != "" {
		waitID = w.WaitID
	}
	if svc, err := hostServiceClient(); err == nil {
		ctx, cancel := hostRPCCtx()
		defer cancel()
		if _, rerr := svc.UnregisterSessionWait(ctx, &sdkv1.UnregisterSessionWaitRequest{WaitId: waitID}); rerr != nil {
			logService().Warn("UnregisterSessionWait 上报宿主失败", "umo", umo, "err", rerr)
		}
	}
}

// takeSessionWait removes and returns the registered wait for umo (one-shot:
// a wait is consumed the first time it matches). Returns nil when no wait is
// registered for umo.
func (p *Plugin) takeSessionWait(umo string) *SessionWait {
	p.sessionWaitsMu.Lock()
	defer p.sessionWaitsMu.Unlock()
	if p.sessionWaits == nil {
		return nil
	}
	w := p.sessionWaits[umo]
	if w == nil {
		return nil
	}
	delete(p.sessionWaits, umo)
	return w
}

// putSessionWait 把被 takeSessionWait 取走的等待放回注册表（handler 未消费
// 该事件时恢复等待，继续匹配下一条）。
func (p *Plugin) putSessionWait(w *SessionWait) {
	if w == nil {
		return
	}
	p.sessionWaitsMu.Lock()
	if p.sessionWaits == nil {
		p.sessionWaits = map[string]*SessionWait{}
	}
	p.sessionWaits[w.UMO] = w
	p.sessionWaitsMu.Unlock()
}

// unifiedMsgOriginOf builds the plugin-facing unified message origin
// ("<platform_id>:<message_type>:<conv_id>") from an event, used to match
// session waits.
func (p *Plugin) unifiedMsgOriginOf(e *Event) string {
	if e == nil {
		return ""
	}
	platform := e.GetPlatformID()
	msgType := e.GetMessageType()
	conv := e.ConvID
	if platform == "" && msgType == "" && conv == "" {
		return ""
	}
	return platform + ":" + msgType + ":" + conv
}

// ── 技能（Skills，对齐 Python SDK astrbot.core.skills.SkillInfo 字段）──

// SkillSourceType 描述技能的来源（local_only / plugin / sandbox_only /
// workspace / both），与宿主 internal/skills 的 SourceType 及 Python SDK
// skill_manager 的 source_type 一致。
type SkillSourceType string

const (
	SkillSourceLocalOnly   SkillSourceType = "local_only"
	SkillSourcePlugin      SkillSourceType = "plugin"
	SkillSourceSandboxOnly SkillSourceType = "sandbox_only"
	SkillSourceWorkspace   SkillSourceType = "workspace"
	SkillSourceBoth        SkillSourceType = "both"
)

// SkillInfo 描述宿主技能管理器中的一个技能。字段名与 host
// internal/skills.SkillInfo 的 JSON 及 Python SDK SkillInfo 完全对齐，
// 供插件读取 ListSkills 结果 / 构造 SetSkillActive、DeleteSkill 入参。
type SkillInfo struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Path          string          `json:"path"`
	Active        bool            `json:"active"`
	SourceType    SkillSourceType `json:"source_type"`
	SourceLabel   string          `json:"source_label"`
	LocalExists   bool            `json:"local_exists"`
	SandboxExists bool            `json:"sandbox_exists"`
	PluginName    string          `json:"plugin_name"`
	Readonly      bool            `json:"readonly"`
	Preset        bool            `json:"preset"`
}

// FromMap 从宿主返回的 SkillInfo JSON map 还原强类型结构（对齐 Python SDK
// SkillInfo.from_dict）。未知/缺失字段安全取零值。
func (s *SkillInfo) FromMap(m map[string]any) {
	if s == nil || m == nil {
		return
	}
	s.Name = strAny(m["name"])
	s.Description = strAny(m["description"])
	s.Path = strAny(m["path"])
	s.Active = boolAny(m["active"])
	s.SourceType = SkillSourceType(strAnyDefault(m["source_type"], "local_only"))
	s.SourceLabel = strAnyDefault(m["source_label"], "local")
	s.LocalExists = boolDefault(m["local_exists"], true)
	s.SandboxExists = boolAny(m["sandbox_exists"])
	s.PluginName = strAny(m["plugin_name"])
	s.Readonly = boolAny(m["readonly"])
	s.Preset = boolAny(m["preset"])
}

// ── 平台消息历史（对齐 Python SDK PlatformMessageHistoryManager / 宿主 db
// platform_message_history 表字段）──

// PMHistoryRecord 描述宿主 db 中一条平台消息历史记录。
type PMHistoryRecord struct {
	ID              int64  `json:"id"`
	PlatformID      string `json:"platform_id"`
	UserID          string `json:"user_id"`
	SenderID        string `json:"sender_id"`
	Content         any    `json:"content"`
	LLMCheckpointID string `json:"llm_checkpoint_id"`
	CreatedAt       string `json:"created_at"`
}

// FromMap 从宿主返回的 PMHistoryRecord JSON map 还原强类型结构。
func (r *PMHistoryRecord) FromMap(m map[string]any) {
	if r == nil || m == nil {
		return
	}
	r.ID = int64Any(m["id"])
	r.PlatformID = strAny(m["platform_id"])
	r.UserID = strAny(m["user_id"])
	r.SenderID = strAny(m["sender_id"])
	r.Content = m["content"]
	r.LLMCheckpointID = strAny(m["llm_checkpoint_id"])
	r.CreatedAt = strAny(m["created_at"])
}

// ── map→强类型 helper（安全取字段，缺省零值，兼容 Python SDK 容错语义）──

func strAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strAnyDefault(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func boolAny(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func boolDefault(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func int64Any(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	case string:
		var i int64
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i
		}
	}
	return 0
}
