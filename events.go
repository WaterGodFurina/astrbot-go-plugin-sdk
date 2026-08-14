package sdk

// Event names for pipeline and lifecycle hooks. These mirror the Python AstrBot
// SDK's register_* decorators (astrbot/api/event/filter) so a plugin author can
// observe the same set of events regardless of language.
//
// Event-only hooks can also be declared with the generic Hook type (set Event
// to one of these constants); the typed hook types below additionally carry
// their payloads (LLMResponse / ToolCall / PluginError).
const (
	// EventOnMessage fires for every incoming adapter message as it flows
	// through the pipeline. It is also emitted by the generic Hook type.
	EventOnMessage = "on_message"
	// EventOnMessageReceived fires immediately after a message is received from
	// a platform adapter, before command matching.
	EventOnMessageReceived = "on_message_received"
	// EventOnPreProcess fires before the message pipeline starts processing.
	EventOnPreProcess = "on_pre_process"
	// EventOnAfterMessageSent fires after the bot's reply has been sent.
	EventOnAfterMessageSent = "on_after_message_sent"
	// EventOnWaitingLLMRequest fires when a message is about to call the LLM but
	// before it enters the queue/lock. Good for sending a "thinking…" notice.
	EventOnWaitingLLMRequest = "on_waiting_llm_request"
	// EventOnLLMRequest fires before the LLM provider call. It can inspect and
	// modify the system prompt (see LLMRequestHook).
	EventOnLLMRequest = "on_llm_request"
	// EventOnLLMResponse fires after the LLM reply is produced (see
	// LLMResponseHook).
	EventOnLLMResponse = "on_llm_response"
	// EventOnUsingLLMTool fires right before an LLM function tool executes.
	EventOnUsingLLMTool = "on_using_llm_tool"
	// EventOnLLMToolRespond fires right after an LLM function tool executes.
	EventOnLLMToolRespond = "on_llm_tool_respond"
	// EventOnDecoratingResult fires before the outgoing reply chain is sent
	// (see ResultHook).
	EventOnDecoratingResult = "on_decorating_result"
	// EventOnResultHandling is an earlier, non-decorating result hook (see
	// ResultHook).
	EventOnResultHandling = "on_result_handling"
	// EventOnPluginError fires when a plugin handler errors out (see
	// PluginErrorHook).
	EventOnPluginError = "on_plugin_error"
	// EventOnAstrbotLoaded fires after the host finishes loading (see
	// AstrbotLoadedHook).
	EventOnAstrbotLoaded = "on_astrbot_loaded"
	// EventOnPlatformLoaded fires after a platform adapter finishes loading.
	EventOnPlatformLoaded = "on_platform_loaded"
	// EventOnPluginLoaded fires after a plugin finishes loading.
	EventOnPluginLoaded = "on_plugin_loaded"
	// EventOnPluginUnloaded fires after a plugin is unloaded.
	EventOnPluginUnloaded = "on_plugin_unloaded"
	// EventOnAgentBegin fires when an agent run begins.
	EventOnAgentBegin = "on_agent_begin"
	// EventOnAgentDone fires when an agent run finishes.
	EventOnAgentDone = "on_agent_done"
)

// LLMResponse is the model reply delivered to on_llm_response / on_agent_done
// hooks. It is the serializable payload carried over HandleHook.
type LLMResponse struct {
	// Text is the model's reply text.
	Text string `json:"text"`
	// Model is the provider/model id that produced the reply, when known.
	Model string `json:"model,omitempty"`
	// MessageID is the platform message id of the sent reply, when known.
	MessageID string `json:"message_id,omitempty"`
}

// ToolCall describes an LLM function tool invocation. It is the payload carried
// over HandleHook for on_using_llm_tool (before execution) and
// on_llm_tool_respond (after execution, with Result/IsError populated).
type ToolCall struct {
	// Name is the tool name the model requested.
	Name string `json:"name"`
	// Args is the raw tool argument object.
	Args map[string]any `json:"args,omitempty"`
	// Result is the tool's return text. Only set on on_llm_tool_respond.
	Result string `json:"result,omitempty"`
	// IsError reports whether the tool call failed. Only set on
	// on_llm_tool_respond.
	IsError bool `json:"is_error,omitempty"`
}

// PluginError describes a plugin handler failure. It is the payload carried
// over HandleHook for on_plugin_error.
type PluginError struct {
	// PluginName is the name of the plugin that failed.
	PluginName string `json:"plugin_name"`
	// HandlerName is the name of the handler that failed.
	HandlerName string `json:"handler_name"`
	// Error is the error message.
	Error string `json:"error"`
}

// MessageHook observes every incoming message (Event "on_message" by default,
// or "on_message_received" / "on_pre_process" via Event). It cannot modify the
// event or its result.
type MessageHook struct {
	Name string
	// Event is "on_message" (default), "on_message_received" or "on_pre_process".
	Event   string
	Handler func(e *Event) error
}

// AfterMessageSentHook fires after the bot's reply has been sent
// (Event "on_after_message_sent").
type AfterMessageSentHook struct {
	Name    string
	Handler func(e *Event) error
}

// WaitingLLMRequestHook fires when a message is about to call the LLM but
// before it enters the queue/lock (Event "on_waiting_llm_request"). Good for
// sending a "thinking…" notice via sdk.Host.SendMessage.
type WaitingLLMRequestHook struct {
	Name    string
	Handler func(e *Event) error
}

// LLMResponseHook fires after the LLM reply is produced
// (Event "on_llm_response"). It can inspect the model reply (e.g. capture
// conversation memory) but cannot change what was already streamed.
type LLMResponseHook struct {
	Name    string
	Handler func(e *Event, resp *LLMResponse) error
}

// ToolCallHook fires right before an LLM function tool executes
// (Event "on_using_llm_tool"). Args is the raw tool argument map; it can be
// inspected but not modified.
type ToolCallHook struct {
	Name    string
	Handler func(e *Event, call *ToolCall) error
}

// ToolRespondHook fires right after an LLM function tool executes
// (Event "on_llm_tool_respond").
type ToolRespondHook struct {
	Name    string
	Handler func(e *Event, call *ToolCall) error
}

// PluginErrorHook fires when a plugin handler errors out
// (Event "on_plugin_error"). err is the wrapped failure (payload string).
type PluginErrorHook struct {
	Name    string
	Handler func(e *Event, pe *PluginError) error
}

// AstrbotLoadedHook fires after the host finishes loading
// (Event "on_astrbot_loaded"). It takes no event.
type AstrbotLoadedHook struct {
	Name    string
	Handler func() error
}

// PlatformLoadedHook fires after a platform adapter finishes loading
// (Event "on_platform_loaded"). The payload carries the platform adapter id.
type PlatformLoadedHook struct {
	Name    string
	Handler func(platform string) error
}

// PluginLoadedHook fires after a plugin finishes loading
// (Event "on_plugin_loaded"). The payload carries the plugin name.
type PluginLoadedHook struct {
	Name    string
	Handler func(pluginName string) error
}

// PluginUnloadedHook fires after a plugin is unloaded
// (Event "on_plugin_unloaded"). The payload carries the plugin name.
type PluginUnloadedHook struct {
	Name    string
	Handler func(pluginName string) error
}

// AgentBeginHook fires when an agent run begins (Event "on_agent_begin").
type AgentBeginHook struct {
	Name    string
	Handler func(e *Event) error
}

// AgentDoneHook fires when an agent run finishes (Event "on_agent_done"). resp
// carries the final reply of the agent run.
type AgentDoneHook struct {
	Name    string
	Handler func(e *Event, resp *LLMResponse) error
}

// hookEventName returns the hook's canonical event name, falling back to the
// default for types whose Event field is empty.
func (h *MessageHook) hookEventName() string {
	if h.Event == "" {
		return EventOnMessage
	}
	return h.Event
}
