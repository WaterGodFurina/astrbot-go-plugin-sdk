package sdk

import "context"

// service_iface.go — the SDK core transport-neutral interfaces. This file is the
// SOURCE OF TRUTH (hand-written, not generated): the gRPC transport adapts
// these native signatures to/from the protobuf wire types, the Native transport
// calls them directly. No protobuf type may appear here.

// PluginService is the host->plugin operation surface. It is implemented by the
// core dispatcher (serviceServer) and consumed in-process by the Native
// transport; the gRPC transport's plugin server adapts the wire types to it.
type PluginService interface {
	// Register negotiates the protocol version and returns plugin metadata plus
	// handler descriptors.
	Register(ctx context.Context, protocolVersion int32) (PluginInfo, error)

	// HandleCommand dispatches to a command handler by name.
	HandleCommand(ctx context.Context, name string, args []string, event *Event) (HandleCommandResult, error)
	// HandleFilter dispatches to a filter handler by name.
	HandleFilter(ctx context.Context, name string, event *Event) (HandleFilterResult, error)
	// HandleHook dispatches to a hook handler by name. payloadJSON carries the
	// hook's typed payload (JSON, stdlib only).
	HandleHook(ctx context.Context, name string, event *Event, chain []Component, payloadJSON []byte) (HandleHookResult, error)
	// HandleLLMRequest runs an on_llm_request hook, returning the (possibly
	// modified) prompts.
	HandleLLMRequest(ctx context.Context, name string, event *Event, systemPrompt, userPrompt string) (HandleLLMRequestResult, error)
	// HandleTool invokes a registered LLM function tool.
	HandleTool(ctx context.Context, name string, args map[string]any, event *Event) (HandleToolResult, error)

	// ListTools returns the plugin's current LLM function tools (pulled live).
	ListTools(ctx context.Context) ([]ToolDesc, error)
	// ListWebApis returns the plugin's current Web API routes (pulled live).
	ListWebApis(ctx context.Context) ([]WebAPIDesc, error)
	// HandleWebRequest dispatches a dashboard-proxied HTTP request to a WebAPI.
	HandleWebRequest(ctx context.Context, req HandleWebRequest) (HandleWebResponse, error)

	// HealthCheck reports the plugin's liveness.
	HealthCheck(ctx context.Context) (HealthInfo, error)
	// SetLogLevel adjusts the plugin's logger level at runtime.
	SetLogLevel(ctx context.Context, level string) error
	// GetConfigSchema returns the plugin's current config schema JSON.
	GetConfigSchema(ctx context.Context) ([]byte, error)
	// FeedSessionWait pushes an inbound event into a registered session wait.
	FeedSessionWait(ctx context.Context, event *Event) (FeedSessionWaitResult, error)
	// Cleanup invokes the plugin's OnUnload hook.
	Cleanup(ctx context.Context) error
}

// HostService is the plugin->host reverse-call surface: the capabilities the
// SDK exposes to plugin authors. The host implements it (HostServiceHooks is the
// canonical implementation); the Native transport passes the implementation
// in-process, the gRPC transport adapts it to the protobuf wire types.
type HostService interface {
	// ── core ──
	CallAction(ctx context.Context, platform, api string, params map[string]any) (map[string]any, error)
	SendMessage(ctx context.Context, platform, sessionID string, chain []Component) error
	RecallMessage(ctx context.Context, platform, messageID string) error
	GetConfig(ctx context.Context, pluginName string) (map[string]any, error)
	SetConfig(ctx context.Context, pluginName string, cfg map[string]any) error
	ChatLLM(ctx context.Context, req *ChatLLMRequest) (string, error)
	React(ctx context.Context, platform, sessionID, messageID, emoji string) error
	TextToImage(ctx context.Context, text, templateName string) (string, error)
	HtmlRender(ctx context.Context, template, data, options string) (string, error)

	// ── conversation ──
	GetCurrConversationID(ctx context.Context, unifiedMsgOrigin string) string
	NewConversation(ctx context.Context, unifiedMsgOrigin, platformID, personaID string) string
	GetConversation(ctx context.Context, unifiedMsgOrigin, cid string, createIfNotExists bool) map[string]any
	GetConversations(ctx context.Context, unifiedMsgOrigin string) []map[string]any
	DeleteConversation(ctx context.Context, unifiedMsgOrigin, cid string) error
	SwitchConversation(ctx context.Context, unifiedMsgOrigin, cid string) error
	UpdateConversationTitle(ctx context.Context, unifiedMsgOrigin, cid, title string) error
	UpdateConversationPersonaID(ctx context.Context, unifiedMsgOrigin, cid, personaID string) error

	// ── persona ──
	GetPersonas(ctx context.Context) []map[string]any
	GetDefaultPersona(ctx context.Context, umo string) map[string]any
	GetPersonaTree(ctx context.Context) (folders []map[string]any, personas []map[string]any)
	ResolveSelectedPersona(ctx context.Context, umo, conversationPersonaID, platformName string, providerSettings map[string]any) (personaID, personaName, personaPrompt, forceAppliedPersonaID string, isDefault bool)

	// ── provider ──
	ListProviders(ctx context.Context, capability string) []map[string]any
	GetUsingProvider(ctx context.Context, umo, capability string) map[string]any
	SetProvider(ctx context.Context, umo, providerID, capability string) error
	GetProviderModels(ctx context.Context, providerID string) []string

	// ── plugin / Star ──
	GetPluginRegistry(ctx context.Context) []map[string]any
	GetStar(ctx context.Context, name string) map[string]any
	SetPluginEnabled(ctx context.Context, pluginName string, enabled bool) error
	InstallPlugin(ctx context.Context, repo string) error
	UninstallPlugin(ctx context.Context, pluginName string) error
	ListCommandDescriptors(ctx context.Context) []map[string]any
	ListPlatforms(ctx context.Context) []map[string]any

	// ── session wait ──
	RegisterSessionWait(ctx context.Context, pluginName, umo string, timeoutSeconds int32) string
	UnregisterSessionWait(ctx context.Context, waitID string)

	// ── bridge hooks ──
	RegisterBridgeHook(ctx context.Context, pluginName, hookName string) error
	UnregisterBridgeHook(ctx context.Context, pluginName, hookName string) error

	// ── blob ──
	CreateBlob(ctx context.Context, data []byte, mimeType, filename string, ttlSeconds int32) (*FileReference, error)
	ReadBlob(ctx context.Context, handleID string, offset int64, limit int32) ([]byte, bool, int64, error)
	GetBlobInfo(ctx context.Context, handleID string) (*FileReference, error)
	ReleaseBlob(ctx context.Context, handleID string) error

	// ── skills ──
	ListSkills(ctx context.Context) []map[string]any
	SetSkillActive(ctx context.Context, name string, active bool) error
	DeleteSkill(ctx context.Context, name string) error
	ListSkillsV2(ctx context.Context, activeOnly bool, runtime string, showSandboxPath bool) []map[string]any

	// ── platform message history ──
	GetPlatformMessageHistory(ctx context.Context, platformID, userID string, limit int32) []map[string]any
	InsertPlatformMessageHistory(ctx context.Context, platformID, userID, senderID string, content any, llmCheckpointID string, maxMessages int32) map[string]any
	UpdatePlatformMessageHistory(ctx context.Context, id int64, content any, llmCheckpointID string) error
	DeletePlatformMessageHistory(ctx context.Context, id int64) error

	// ── knowledge base ──
	KBRetrieve(ctx context.Context, query string, kbNames []string, topKFusion, topMFinal int) (contextText string, resultsJSON string, err error)
	KBUploadFromURL(ctx context.Context, kbNameOrID, url string, chunkSize, chunkOverlap int) error
	KBListKBs(ctx context.Context) []map[string]any

	// ── file tokens ──
	RegisterFileToken(ctx context.Context, path string, timeoutSec int32) (string, error)

	// ── cron ──
	CronCreate(ctx context.Context, spec *CronCreateSpec) (map[string]any, error)
	CronUpdate(ctx context.Context, jobID string, fields map[string]any) (map[string]any, error)
	CronDelete(ctx context.Context, jobID string) error
	CronList(ctx context.Context, jobType string) []map[string]any
	CronRunNow(ctx context.Context, jobID string) error

	// ── MCP ──
	McpListTools(ctx context.Context) []map[string]any
	McpCallTool(ctx context.Context, server, toolName string, args map[string]any) (result map[string]any, text string, isError bool, err error)
}
