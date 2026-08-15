package sdk

// Command is a message command a plugin accepts. The plugin author only has
// to fill in the descriptor fields and the Handler.
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	Permission  string // "everyone" (default) or "admin"
	Handler     func(e *Event, args []string) (string, error)
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
	// OnLoad runs inside the plugin process right before the RPC server starts.
	// Use it for setup / dynamic handler registration. A returned error aborts
	// plugin startup.
	OnLoad func() error
	// OnConfig 目前仅供声明，SDK 尚未在运行时主动调用它（宿主端配置变更
	// 暂不主动推送）。插件可通过 Host.GetConfig 主动读取配置。
	OnConfig func(cfg *Config) error
	OnUnload func() error
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
