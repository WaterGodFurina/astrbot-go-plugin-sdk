package sdk

// native_types.go — transport-neutral value types that cross the SDK core
// boundary (PluginService / HostService / PluginClient). They are pure Go
// structs with no protobuf dependency: the gRPC transport converts them to/from
// the protobuf messages at its own edge, the Native transport passes them directly.
//
// Only types that genuinely have no equivalent native representation are
// declared here. Existing native types (Event, Component, SkillInfo,
// PMHistoryRecord, KBInfo, CronCreateSpec, CronJobInfo, MCPToolInfo,
// MCPToolCallResult, ProviderRequest, ...) are reused as-is.

// EventResult is the per-handler outcome attached to every dispatch response.
// It mirrors the protobuf EventResult data plane without protobuf.
type EventResult struct {
	// Handled reports that a handler matched and produced a result.
	Handled bool
	// Sent reports that the handler sent a message (kept for parity; the Go SDK
	// does not currently track send operations, so it is usually false).
	Sent bool
	// StopPropagation halts the host pipeline after this handler runs.
	StopPropagation bool
}

// CommandDesc describes a registered command (Register response / discovery).
type CommandDesc struct {
	Name         string
	Aliases      []string
	Description  string
	Usage        string
	Permission   string
	ParentGroup  string
	IsSubCommand bool
}

// FilterDesc describes a registered filter.
type FilterDesc struct {
	Name string
}

// HookDesc describes a registered hook (name + event it binds to).
type HookDesc struct {
	Name  string
	Event string
}

// ToolDesc describes an LLM function tool exposed by the plugin.
type ToolDesc struct {
	Name        string
	Description string
	// ParamsSchemaJSON is the JSON object schema of the tool arguments.
	ParamsSchemaJSON []byte
}

// WebAPIDesc describes a dashboard Web API route served by the plugin.
type WebAPIDesc struct {
	Route   string
	Methods []string
	Desc    string
}

// PluginInfo is the Register response: plugin metadata plus handler descriptors.
type PluginInfo struct {
	Name             string
	Version          string
	Description      string
	Author           string
	ConfigSchemaJSON []byte
	ProtocolVersion  int32
	Commands         []CommandDesc
	Filters          []FilterDesc
	Hooks            []HookDesc
	Tools            []ToolDesc
	WebAPIs          []WebAPIDesc
}

// HealthInfo is the HealthCheck response. Load/Plugins/RuntimeHeartbeat are
// reported by runtimes that mirror plugin state (Python shared runtime); a Go
// plugin leaves them zero.
type HealthInfo struct {
	OK               bool
	Load             float64
	Version          string
	Plugins          []PluginStatus
	RuntimeHeartbeat float64
}

// PluginStatus is a runtime-reported per-plugin status snapshot.
type PluginStatus struct {
	PluginID     string
	PluginName   string
	State        string
	Health       string
	LastActivity float64
	Error        string
	Generation   int64
}

// HandleCommandResult is the native result of dispatching a command.
type HandleCommandResult struct {
	Text   string
	Chain  []Component
	Result EventResult
}

// HandleFilterResult is the native result of dispatching a filter.
type HandleFilterResult struct {
	Allow  bool
	Result EventResult
}

// HandleHookResult is the native result of dispatching a hook.
type HandleHookResult struct {
	Chain  []Component
	Result EventResult
}

// HandleLLMRequestResult is the native result of an on_llm_request hook.
type HandleLLMRequestResult struct {
	SystemPrompt string
	UserPrompt   string
	Stop         bool
	Result       EventResult
}

// HandleToolResult is the native result of invoking an LLM function tool.
type HandleToolResult struct {
	Text    string
	IsError bool
	Result  EventResult
}

// WebUploadFile is one multipart file part of a proxied WebAPI request.
type WebUploadFile struct {
	Field       string
	Filename    string
	ContentType string
	Content     []byte
}

// HandleWebRequest is a dashboard-proxied HTTP request handed to a plugin
// WebAPI. Query/Headers are multi-value maps; PathParams holds dynamic route
// values; Body is the raw request body; Files carries multipart uploads.
type HandleWebRequest struct {
	PluginID   string
	Method     string
	Path       string
	Query      map[string][]string
	Headers    map[string][]string
	Body       []byte
	Files      []WebUploadFile
	PathParams map[string]string
}

// HandleWebResponse is the plugin WebAPI's reply.
type HandleWebResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

// FeedSessionWaitResult reports whether a registered session wait consumed the
// pushed event.
type FeedSessionWaitResult struct {
	Handled bool
}

// ChatLLMRequest is the native form of a chat-completion request (replaces the
// protobuf ChatLLMRequest pointer in the host hook surface).
type ChatLLMRequest struct {
	Prompt       string
	SystemPrompt string
	SessionID    string
	ImageURLs    []string
	AudioURLs    []string
	ToolsJSON    []byte
	ContextsJSON []byte
	ProviderID   string
}

// FileReference is a host-issued handle for a stored blob (replaces the
// protobuf FileReference in the host hook surface).
type FileReference struct {
	HandleID  string
	Size      int64
	MimeType  string
	Filename  string
	ExpiresAt int64
}

// ManagePluginRequest is the native form of the python-shared runtime's
// load/unload control call.
type ManagePluginRequest struct {
	// Action is "load" or "unload".
	Action string
	// PluginID is the host-assigned target plugin id (manifest id for shared
	// runtimes).
	PluginID string
	// PluginDir is the plugin source dir (absolute); empty for unload.
	PluginDir string
	// PluginName / Version are informational (Register is authoritative).
	PluginName string
	Version    string
}

// ManagePluginResponse is the reply to a ManagePlugin control call.
type ManagePluginResponse struct {
	OK    bool
	Error string
}

// FeedCronJobRequest pushes a cron firing into a (python-shared) plugin.
type FeedCronJobRequest struct {
	JobID       string
	JobName     string
	PayloadJSON []byte
	RunAt       string
	PluginID    string
}

// FeedCronJobResponse reports whether the plugin handled the cron firing.
type FeedCronJobResponse struct {
	Handled bool
}
