package sdk

import (
	"strings"
	"sync"
)

// Package-level handler registry enabling imperative (non-struct) handler
// registration. Handlers registered here are merged into the Plugin passed to
// Serve() at startup, so complex plugins can register handlers from init() or
// from OnLoad() instead of building one giant struct literal.
//
// Handlers MUST be registered before the RPC server starts (i.e. in init(),
// package-level var initializers, or OnLoad). The host reads the handler set
// once via Register(); handlers added after Serve starts serving are not
// visible to the host.
var global = &registry{}

type registry struct {
	// mu 保护以下切片：注册函数（RegisterXxx）与 Serve 时的 drain 读取
	// 可能来自不同 goroutine，并发 append 会造成数据竞争。
	mu sync.Mutex

	commands        []Command
	filters         []Filter
	hooks           []Hook
	tools           []Tool
	llmRequestHooks []LLMRequestHook
	resultHooks     []ResultHook

	messageHooks           []MessageHook
	afterMessageSentHooks  []AfterMessageSentHook
	waitingLLMRequestHooks []WaitingLLMRequestHook
	llmResponseHooks       []LLMResponseHook
	toolCallHooks          []ToolCallHook
	toolRespondHooks       []ToolRespondHook
	pluginErrorHooks       []PluginErrorHook
	astrbotLoadedHooks     []AstrbotLoadedHook
	platformLoadedHooks    []PlatformLoadedHook
	pluginLoadedHooks      []PluginLoadedHook
	pluginUnloadedHooks    []PluginUnloadedHook
	agentBeginHooks        []AgentBeginHook
	agentDoneHooks         []AgentDoneHook
}

// RegisterCommand adds a command to the global registry (merged at Serve time).
func RegisterCommand(cmd Command) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.commands = append(global.commands, cmd)
}

// RegisterFilter adds an event filter to the global registry (merged at Serve
// time). Returning false stops propagation of the event.
func RegisterFilter(f Filter) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.filters = append(global.filters, f)
}

// RegisterHook adds a lifecycle/pipeline hook to the global registry (merged
// at Serve time).
func RegisterHook(h Hook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.hooks = append(global.hooks, h)
}

// RegisterTool adds an LLM function tool to the global registry (merged at
// Serve time). The model can call it during chat; the handler runs inside the
// plugin process.
func RegisterTool(t Tool) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.tools = append(global.tools, t)
}

// RegisterLLMRequestHook adds an on_llm_request hook that can inspect and
// modify the LLM system prompt before the provider call.
func RegisterLLMRequestHook(h LLMRequestHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.llmRequestHooks = append(global.llmRequestHooks, h)
}

// RegisterResultHook adds a result-decoration hook that can modify the outgoing
// reply chain before it is sent.
func RegisterResultHook(h ResultHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.resultHooks = append(global.resultHooks, h)
}

// RegisterMessageHook adds a hook that observes incoming messages
// (Event "on_message", "on_message_received" or "on_pre_process").
func RegisterMessageHook(h MessageHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.messageHooks = append(global.messageHooks, h)
}

// RegisterAfterMessageSentHook adds a hook that fires after the bot's reply is
// sent (Event "on_after_message_sent").
func RegisterAfterMessageSentHook(h AfterMessageSentHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.afterMessageSentHooks = append(global.afterMessageSentHooks, h)
}

// RegisterWaitingLLMRequestHook adds a hook that fires before the LLM call
// queues (Event "on_waiting_llm_request").
func RegisterWaitingLLMRequestHook(h WaitingLLMRequestHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.waitingLLMRequestHooks = append(global.waitingLLMRequestHooks, h)
}

// RegisterLLMResponseHook adds a hook that fires after the LLM reply is
// produced (Event "on_llm_response").
func RegisterLLMResponseHook(h LLMResponseHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.llmResponseHooks = append(global.llmResponseHooks, h)
}

// RegisterToolCallHook adds a hook that fires before an LLM function tool
// executes (Event "on_using_llm_tool").
func RegisterToolCallHook(h ToolCallHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.toolCallHooks = append(global.toolCallHooks, h)
}

// RegisterToolRespondHook adds a hook that fires after an LLM function tool
// executes (Event "on_llm_tool_respond").
func RegisterToolRespondHook(h ToolRespondHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.toolRespondHooks = append(global.toolRespondHooks, h)
}

// RegisterPluginErrorHook adds a hook that fires when a plugin handler errors
// out (Event "on_plugin_error").
func RegisterPluginErrorHook(h PluginErrorHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.pluginErrorHooks = append(global.pluginErrorHooks, h)
}

// RegisterAstrbotLoadedHook adds a hook that fires after the host finishes
// loading (Event "on_astrbot_loaded").
func RegisterAstrbotLoadedHook(h AstrbotLoadedHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.astrbotLoadedHooks = append(global.astrbotLoadedHooks, h)
}

// RegisterPlatformLoadedHook adds a hook that fires after a platform adapter
// finishes loading (Event "on_platform_loaded").
func RegisterPlatformLoadedHook(h PlatformLoadedHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.platformLoadedHooks = append(global.platformLoadedHooks, h)
}

// RegisterPluginLoadedHook adds a hook that fires after a plugin finishes
// loading (Event "on_plugin_loaded").
func RegisterPluginLoadedHook(h PluginLoadedHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.pluginLoadedHooks = append(global.pluginLoadedHooks, h)
}

// RegisterPluginUnloadedHook adds a hook that fires after a plugin is unloaded
// (Event "on_plugin_unloaded").
func RegisterPluginUnloadedHook(h PluginUnloadedHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.pluginUnloadedHooks = append(global.pluginUnloadedHooks, h)
}

// RegisterAgentBeginHook adds a hook that fires when an agent run begins
// (Event "on_agent_begin").
func RegisterAgentBeginHook(h AgentBeginHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.agentBeginHooks = append(global.agentBeginHooks, h)
}

// RegisterAgentDoneHook adds a hook that fires when an agent run finishes
// (Event "on_agent_done").
func RegisterAgentDoneHook(h AgentDoneHook) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.agentDoneHooks = append(global.agentDoneHooks, h)
}

// normalizePermission validates a command's Permission string. 合法值为
// admin / everyone / 空串（大小写不敏感、忽略首尾空白，空串视为默认
// everyone），均静默返回；其余未知值打 Warn 并回退 everyone（子任务 F2：
// 原 default 分支对合法的 everyone / 空串也告警，属误报，此处仅对真正未知
// 的值告警；各输入的返回值与原先保持一致）。
func normalizePermission(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "admin":
		return "admin"
	case "everyone", "":
		return "everyone"
	default:
		logService().Warn("Command.Permission 未知，已回退为 everyone", "permission", p)
		return "everyone"
	}
}

// drain merges the global registry into p, preserving p's own entries after
// the registered ones. Called by Serve after OnLoad so registrations made from
// init()/OnLoad() are all captured.
func (r *registry) drain(p *Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmds := make([]Command, 0, len(r.commands)+len(p.Commands))
	cmds = append(cmds, r.commands...)
	cmds = append(cmds, p.Commands...)
	for i := range cmds {
		cmds[i].Permission = normalizePermission(cmds[i].Permission)
	}
	p.Commands = cmds

	filters := make([]Filter, 0, len(r.filters)+len(p.Filters))
	filters = append(filters, r.filters...)
	filters = append(filters, p.Filters...)
	p.Filters = filters

	hooks := make([]Hook, 0, len(r.hooks)+len(p.Hooks))
	hooks = append(hooks, r.hooks...)
	hooks = append(hooks, p.Hooks...)
	p.Hooks = hooks

	tools := make([]Tool, 0, len(r.tools)+len(p.Tools))
	tools = append(tools, r.tools...)
	tools = append(tools, p.Tools...)
	p.Tools = tools

	llm := make([]LLMRequestHook, 0, len(r.llmRequestHooks)+len(p.LLMRequestHooks))
	llm = append(llm, r.llmRequestHooks...)
	llm = append(llm, p.LLMRequestHooks...)
	p.LLMRequestHooks = llm

	results := make([]ResultHook, 0, len(r.resultHooks)+len(p.ResultHooks))
	results = append(results, r.resultHooks...)
	results = append(results, p.ResultHooks...)
	p.ResultHooks = results

	p.MessageHooks = merge(r.messageHooks, p.MessageHooks)
	p.AfterMessageSentHooks = merge(r.afterMessageSentHooks, p.AfterMessageSentHooks)
	p.WaitingLLMRequestHooks = merge(r.waitingLLMRequestHooks, p.WaitingLLMRequestHooks)
	p.LLMResponseHooks = merge(r.llmResponseHooks, p.LLMResponseHooks)
	p.ToolCallHooks = merge(r.toolCallHooks, p.ToolCallHooks)
	p.ToolRespondHooks = merge(r.toolRespondHooks, p.ToolRespondHooks)
	p.PluginErrorHooks = merge(r.pluginErrorHooks, p.PluginErrorHooks)
	p.AstrbotLoadedHooks = merge(r.astrbotLoadedHooks, p.AstrbotLoadedHooks)
	p.PlatformLoadedHooks = merge(r.platformLoadedHooks, p.PlatformLoadedHooks)
	p.PluginLoadedHooks = merge(r.pluginLoadedHooks, p.PluginLoadedHooks)
	p.PluginUnloadedHooks = merge(r.pluginUnloadedHooks, p.PluginUnloadedHooks)
	p.AgentBeginHooks = merge(r.agentBeginHooks, p.AgentBeginHooks)
	p.AgentDoneHooks = merge(r.agentDoneHooks, p.AgentDoneHooks)
}

// merge prepends registered (imperative) entries before the plugin's own
// declarative entries, preserving order.
func merge[T any](registered, declared []T) []T {
	out := make([]T, 0, len(registered)+len(declared))
	out = append(out, registered...)
	out = append(out, declared...)
	return out
}
