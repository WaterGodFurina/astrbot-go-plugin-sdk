package sdk

import "sync"

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

// drain merges the global registry into p, preserving p's own entries after
// the registered ones. Called by Serve after OnLoad so registrations made from
// init()/OnLoad() are all captured.
func (r *registry) drain(p *Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmds := make([]Command, 0, len(r.commands)+len(p.Commands))
	cmds = append(cmds, r.commands...)
	cmds = append(cmds, p.Commands...)
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
}
