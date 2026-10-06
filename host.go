package sdk

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

// HostServiceAppID is the go-plugin broker AppID of the host's HostService.
// The host serves it (Accept) and plugins dial it (Dial). A high fixed value
// never collides with go-plugin's auto-incrementing NextId() stream.
const HostServiceAppID uint32 = 9000

// ---------------------------------------------------------------------------
// Plugin side: reverse calls into the host's HostService.
//
// Core is transport-agnostic: a transport installs hostCallerFn (gRPC dials the
// go-plugin broker; Native returns an in-process HostServiceServer). This keeps
// google.golang.org/grpc out of Native plugin builds.
// ---------------------------------------------------------------------------

var hostCallerFn func() (HostService, error)

// SetHostCallerFunc installs the transport's plugin->host caller factory.
func SetHostCallerFunc(fn func() (HostService, error)) { hostCallerFn = fn }

// hostServiceCaller returns the plugin's reverse-call client.
func hostServiceCaller() (HostService, error) {
	if hostCallerFn == nil {
		return nil, errNoHostCaller
	}
	return hostCallerFn()
}

var errNoHostCaller = &hostUnavailableError{"host service unavailable: no reverse-call transport linked"}

type hostUnavailableError struct{ msg string }

func (e *hostUnavailableError) Error() string { return e.msg }

// hostRPCTimeout 是插件→宿主反向调用的默认超时。宿主 hook 是第三方代码，
// 卡死时插件 handler 不能无限阻塞（26-7），与 python-sdk 的 30-180s
// timeout 对齐。
const (
	hostRPCTimeout = 30 * time.Second
	// hostLLMTimeout 是 LLM/渲染类长操作的超时：LLM 生成（工具循环、长
	// 上下文、推理模型）普遍超过 30s，不能走普通 RPC 的短超时。
	hostLLMTimeout = 180 * time.Second
)

// hostRPCCtx 返回带默认超时的上下文，供 Host API 内部 RPC 使用。调用方必须
// defer cancel() 释放定时器。
func hostRPCCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), hostRPCTimeout)
}

// hostLLMRPCCtx 返回带长超时的上下文，供 ChatLLM/TextToImage/HtmlRender 等
// 长操作 RPC 使用。调用方必须 defer cancel() 释放定时器。
func hostLLMRPCCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), hostLLMTimeout)
}

// Host is the plugin-facing reverse-call API into the AstrBot host process.
// It is only available while the plugin is being served (i.e. inside
// command/filter/hook/tool handlers, not inside OnLoad).
var Host = &host{}

// host provides the plugin-facing API to call back into the host.
type host struct{}

// CallAction invokes a platform API (e.g. OneBot v11 call_action). platform is
// the adapter id (e.g. "aiocqhttp"); params are the action parameters. The
// returned map is the action's "data" object.
func (h *host) CallAction(platform, api string, params map[string]any) (map[string]any, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.CallAction(ctx, platform, api, params)
}

// SendMessage sends a message chain to a session on a platform adapter.
// sessionID is the conversation id (group id or friend user id).
func (h *host) SendMessage(platform, sessionID string, chain []Component) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.SendMessage(ctx, platform, sessionID, chain)
}

// SendMessageComponents 发送原生组件链（P0-2）。组件可携带内联二进制或
// FileReference handle，大文件经宿主 blob 读取组装，避免全量 base64 塞进
// 消息链。新 API（原生组件）。
func (h *host) SendMessageComponents(platform, sessionID string, comps []Component) error {
	return h.SendMessage(platform, sessionID, comps)
}

// RecallMessage recalls an already-sent message by platform message id.
func (h *host) RecallMessage(platform, messageID string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.RecallMessage(ctx, platform, messageID)
}

// GetConfig returns the plugin's persisted config map
// (data/plugins/<name>/config.json on the host).
func (h *host) GetConfig(pluginName string) (map[string]any, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	// Go 插件为单租户进程，无 plugin_id：传空，由宿主按连接身份/注册名解析。
	return svc.GetConfig(ctx, pluginName, "")
}

// SetConfig persists the plugin's full config map
// (data/plugins/<name>/config.json on the host). Read-modify-write with
// GetConfig to preserve unrelated keys.
func (h *host) SetConfig(pluginName string, cfg map[string]any) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	// Go 插件为单租户进程，无 plugin_id：传空。
	return svc.SetConfig(ctx, pluginName, "", cfg)
}

// ChatLLMFull calls the host's chat LLM provider with the full request
// (prompt, system prompt, image/audio URLs, tools, contexts, provider id)
// and returns the model's reply text. It is the zero-copy entry point; the
// request pointer is passed through directly.
func (h *host) ChatLLMFull(req *ChatLLMRequest) (string, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return "", err
	}
	ctx, cancel := hostLLMRPCCtx()
	defer cancel()
	return svc.ChatLLM(ctx, req)
}

// ChatLLM calls the host's default chat LLM provider with the given prompt and
// returns the model's reply text. It does not execute tool calls. Kept as a
// thin wrapper over ChatLLMFull so existing plugin callers stay unchanged.
func (h *host) ChatLLM(prompt, systemPrompt string, imageURLs []string) (string, error) {
	return h.ChatLLMFull(&ChatLLMRequest{
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		ImageURLs:    imageURLs,
	})
}

// React adds an emoji reaction to a message on a platform adapter.
func (h *host) React(platform, sessionID, messageID, emoji string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.React(ctx, platform, sessionID, messageID, emoji)
}

// TextToImage renders text into an image via the host t2i engine, returning
// base64-encoded PNG bytes.
func (h *host) TextToImage(text, templateName string) (string, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return "", err
	}
	ctx, cancel := hostLLMRPCCtx()
	defer cancel()
	return svc.TextToImage(ctx, text, templateName)
}

// TextToImageBytes 返回宿主 t2i 渲染的 PNG 原始字节（解码 base64）。
func (h *host) TextToImageBytes(text, templateName string) ([]byte, error) {
	b64, err := h.TextToImage(text, templateName)
	if err != nil {
		return nil, err
	}
	if b64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(b64)
}

// HtmlRender renders an HTML template + data into an image via the host
// (t2i remote preferred, local gg fallback), returning base64-encoded PNG bytes.
func (h *host) HtmlRender(template, data, options string) (string, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return "", err
	}
	ctx, cancel := hostLLMRPCCtx()
	defer cancel()
	return svc.HtmlRender(ctx, template, data, options)
}

// HtmlRenderBytes 返回宿主 HtmlRender 渲染的 PNG 原始字节（解码 base64）。
func (h *host) HtmlRenderBytes(template, data, options string) ([]byte, error) {
	b64, err := h.HtmlRender(template, data, options)
	if err != nil {
		return nil, err
	}
	if b64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(b64)
}

// RegisterBridgeHook 告知宿主：本插件经"桥接钩子"接收入站消息（botpy/
// telegram 等兼容层用）。宿主收到入站消息时会把序列化事件推给该插件的
// HandleHook(name=hookName)。返回 nil 表示注册成功。
func (h *host) RegisterBridgeHook(hookName string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.RegisterBridgeHook(ctx, "", hookName)
}

// UnregisterBridgeHook 注销桥接钩子，宿主不再推送入站消息。
func (h *host) UnregisterBridgeHook(hookName string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.UnregisterBridgeHook(ctx, "", hookName)
}

// CreateBlob 把 data 交给宿主持久化，返回受控 FileReference handle（P0-2）。
// 大文件（>inline 阈值）由插件侧决定走此路径；宿主统一 TTL/GC。
func (h *host) CreateBlob(data []byte, mimeType, filename string, ttlSeconds int32) (*FileReference, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostLLMRPCCtx()
	defer cancel()
	ref, err := svc.CreateBlob(ctx, data, mimeType, filename, ttlSeconds)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, fmt.Errorf("host returned empty blob reference")
	}
	return ref, nil
}

// ReadBlob 分块读取宿主 blob（offset/limit；limit<=0 用宿主默认块）。
func (h *host) ReadBlob(handleID string, offset int64, limit int32) ([]byte, bool, int64, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, false, 0, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.ReadBlob(ctx, handleID, offset, limit)
}

// GetBlobInfo 返回 blob 元数据。
func (h *host) GetBlobInfo(handleID string) (*FileReference, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	ref, err := svc.GetBlobInfo(ctx, handleID)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, fmt.Errorf("blob not found: %s", handleID)
	}
	return ref, nil
}

// ReleaseBlob 主动释放宿主 blob（最终删除由宿主 TTL/GC 判定）。
func (h *host) ReleaseBlob(handleID string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.ReleaseBlob(ctx, handleID)
}

// ListSkills 返回宿主技能管理器中的全部技能（强类型 SkillInfo）。
func (h *host) ListSkills() ([]SkillInfo, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	raws := svc.ListSkills(ctx)
	out := make([]SkillInfo, 0, len(raws))
	for _, m := range raws {
		var s SkillInfo
		s.FromMap(m)
		out = append(out, s)
	}
	return out, nil
}

// SetSkillActive 启用/禁用指定技能。
func (h *host) SetSkillActive(name string, active bool) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.SetSkillActive(ctx, name, active)
}

// DeleteSkill 删除指定技能。
func (h *host) DeleteSkill(name string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.DeleteSkill(ctx, name)
}

// GetPlatformMessageHistory 按平台/用户取最近 limit 条平台消息记录
// （强类型 PMHistoryRecord）。
func (h *host) GetPlatformMessageHistory(platformID, userID string, limit int32) ([]PMHistoryRecord, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	raws := svc.GetPlatformMessageHistory(ctx, platformID, userID, limit)
	out := make([]PMHistoryRecord, 0, len(raws))
	for _, m := range raws {
		var r PMHistoryRecord
		r.FromMap(m)
		out = append(out, r)
	}
	return out, nil
}

// InsertPlatformMessageHistory 插入一条平台消息记录（content 为可 JSON 化的
// dict / list / str），返回完整记录（强类型 PMHistoryRecord，id/created_at
// 由宿主生成）。
func (h *host) InsertPlatformMessageHistory(platformID, userID, senderID string, content any, llmCheckpointID string, maxMessages int32) (PMHistoryRecord, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return PMHistoryRecord{}, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	m := svc.InsertPlatformMessageHistory(ctx, platformID, userID, senderID, content, llmCheckpointID, maxMessages)
	var r PMHistoryRecord
	r.FromMap(m)
	return r, nil
}

// UpdatePlatformMessageHistory 更新一条记录（content 或 llm_checkpoint_id，
// 传 nil 表示不更新对应字段）。
func (h *host) UpdatePlatformMessageHistory(id int64, content any, llmCheckpointID string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.UpdatePlatformMessageHistory(ctx, id, content, llmCheckpointID)
}

// DeletePlatformMessageHistory 按 ID 删除一条平台消息记录。
func (h *host) DeletePlatformMessageHistory(id int64) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.DeletePlatformMessageHistory(ctx, id)
}

// ListSkillsV2 带过滤参数的技能列表：activeOnly 仅返回启用技能；runtime
// 过滤运行时视图（"local"/"sandbox"/""=全部）；showSandboxPath 返回 sandbox
// 路径而非宿主本地路径（强类型 SkillInfo）。
func (h *host) ListSkillsV2(activeOnly bool, runtime string, showSandboxPath bool) ([]SkillInfo, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	raws := svc.ListSkillsV2(ctx, activeOnly, runtime, showSandboxPath)
	out := make([]SkillInfo, 0, len(raws))
	for _, m := range raws {
		var s SkillInfo
		s.FromMap(m)
		out = append(out, s)
	}
	return out, nil
}

// KBRetrieve 检索宿主知识库：返回拼接后的上下文文本与结果 JSON 数组
// （无命中时 context_text 为空、results_json 为 "[]"）。可经
// ParseKBSearchResults 解析为强类型切片。kbNames 为空 = 宿主全部知识库。
func (h *host) KBRetrieve(query string, kbNames []string, topKFusion, topMFinal int) (string, string, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return "", "", err
	}
	// 检索含嵌入 API 调用，属长操作，用 LLM 级超时。
	ctx, cancel := hostLLMRPCCtx()
	defer cancel()
	contextText, results, err := svc.KBRetrieve(ctx, query, kbNames, topKFusion, topMFinal)
	if err != nil {
		return "", "", err
	}
	if results == "" {
		results = "[]"
	}
	return contextText, results, nil
}

// KBUploadFromURL 让宿主从 URL 拉取文档写入指定知识库并分块
// （chunkSize/chunkOverlap <=0 用宿主默认；kbNameOrID 为知识库名或 ID）。
func (h *host) KBUploadFromURL(kbNameOrID, url string, chunkSize, chunkOverlap int) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	// 下载 + 分块 + 逐块嵌入是长操作，用 LLM 级超时。
	ctx, cancel := hostLLMRPCCtx()
	defer cancel()
	return svc.KBUploadFromURL(ctx, kbNameOrID, url, chunkSize, chunkOverlap)
}

// KBListKBs 返回宿主全部知识库元数据（强类型 KBInfo）。
func (h *host) KBListKBs() ([]KBInfo, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	raws := svc.KBListKBs(ctx)
	out := make([]KBInfo, 0, len(raws))
	for _, m := range raws {
		var k KBInfo
		k.FromMap(m)
		out = append(out, k)
	}
	return out, nil
}

// RegisterFileToken 把宿主侧文件路径登记为随机不可枚举的令牌（timeoutSec
// <=0 用宿主默认 TTL），下游凭 token 经宿主公开文件路由（/api/file/{token}）
// 读取文件，避免暴露真实路径。
func (h *host) RegisterFileToken(path string, timeoutSec int32) (string, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return "", err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.RegisterFileToken(ctx, path, timeoutSec)
}

// CronCreate 创建定时任务，返回宿主 Job 快照（强类型 CronJobInfo）。
func (h *host) CronCreate(spec CronCreateSpec) (CronJobInfo, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return CronJobInfo{}, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	m, err := svc.CronCreate(ctx, &spec)
	if err != nil {
		return CronJobInfo{}, err
	}
	var info CronJobInfo
	info.FromMap(m)
	return info, nil
}

// CronUpdate 按 jobID 更新任务字段（fields 仅含需更新的键），返回更新后的
// Job 快照（强类型 CronJobInfo）。
func (h *host) CronUpdate(jobID string, fields map[string]any) (CronJobInfo, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return CronJobInfo{}, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	m, err := svc.CronUpdate(ctx, jobID, fields)
	if err != nil {
		return CronJobInfo{}, err
	}
	var info CronJobInfo
	info.FromMap(m)
	return info, nil
}

// CronDelete 删除指定定时任务。
func (h *host) CronDelete(jobID string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.CronDelete(ctx, jobID)
}

// CronList 列出定时任务（jobType 空 = 全部类型，强类型 CronJobInfo）。
func (h *host) CronList(jobType string) ([]CronJobInfo, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	raws := svc.CronList(ctx, jobType)
	out := make([]CronJobInfo, 0, len(raws))
	for _, m := range raws {
		var info CronJobInfo
		info.FromMap(m)
		out = append(out, info)
	}
	return out, nil
}

// CronRunNow 立即触发一次指定任务。
func (h *host) CronRunNow(jobID string) error {
	svc, err := hostServiceCaller()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	return svc.CronRunNow(ctx, jobID)
}

// McpListTools 汇总宿主已连接 MCP server 的全部工具（强类型 MCPToolInfo）。
func (h *host) McpListTools() ([]MCPToolInfo, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	raws := svc.McpListTools(ctx)
	out := make([]MCPToolInfo, 0, len(raws))
	for _, m := range raws {
		var t MCPToolInfo
		t.FromMap(m)
		out = append(out, t)
	}
	return out, nil
}

// McpCallTool 调用宿主侧 MCP 工具（server + toolName + args），返回完整
// 结果（content/isError，含宿主提取的纯文本摘要）。
func (h *host) McpCallTool(server, toolName string, args map[string]any) (*MCPToolCallResult, error) {
	svc, err := hostServiceCaller()
	if err != nil {
		return nil, err
	}
	// MCP 工具可能执行慢操作（网页抓取/子进程等），用 LLM 级超时。
	ctx, cancel := hostLLMRPCCtx()
	defer cancel()
	result, text, isError, err := svc.McpCallTool(ctx, server, toolName, args)
	if err != nil {
		return nil, err
	}
	var r MCPToolCallResult
	r.FromMap(result)
	// result 缺失/解析失败时回退到顶层字段。
	if len(r.Content) == 0 && r.Text == "" {
		r.IsError = r.IsError || isError
		r.Text = text
	}
	return &r, nil
}

// ---------------------------------------------------------------------------
// Host side: serving the HostService over the broker.
// ---------------------------------------------------------------------------

// HostServiceHooks is implemented by the AstrBot host to service reverse
// plugin calls. Install it once via SetHostHooks before launching plugins.
type HostServiceHooks struct {
	// CallAction forwards a platform API call. Return the action's data object.
	CallAction func(platform, api string, params map[string]any) (map[string]any, error)
	// SendMessage sends a chain to a session on a platform adapter.
	SendMessage func(platform, sessionID string, chain []Component) error
	// RecallMessage recalls a sent message by platform message id.
	RecallMessage func(platform, messageID string) error
	// GetConfig returns the plugin's persisted config map.
	GetConfig func(pluginName string) (map[string]any, error)
	// SetConfig persists the plugin's full config map.
	SetConfig func(pluginName string, cfg map[string]any) error
	// GetConfigByID 是 GetConfig 的租户精确版本（python-shared 多租户）：
	// 宿主按 manifest plugin_id 直接解析配置，不依赖注册名（同名插件不再
	// 歧义）。非 nil 时 HostServiceServer 优先调用它；nil 时回退 GetConfig
	//（单插件进程 / 旧宿主）。
	GetConfigByID func(pluginID string) (map[string]any, error)
	// SetConfigByID 是 SetConfig 的租户精确版本（同上）。
	SetConfigByID func(pluginID string, cfg map[string]any) error
	// ChatLLM calls the default chat provider with the full request. It
	// receives the request pointer directly (zero-copy passthrough) so the
	// host can consume audio_urls/tools_json/contexts_json/provider_id.
	ChatLLM func(req *ChatLLMRequest) (string, error)
	// React adds an emoji reaction to a message on a platform.
	React func(platform, sessionID, messageID, emoji string) error
	// TextToImage renders text into an image, returning base64 PNG bytes.
	TextToImage func(text, templateName string) (string, error)
	// HtmlRender renders an HTML template + data into an image (t2i remote
	// preferred, local gg fallback), returning base64 PNG bytes.
	HtmlRender func(template, data, options string) (string, error)

	// ── 会话管理（对齐 Python conversation_manager）──
	// GetCurrConversationID 返回 umo 的当前会话 ID（无会话返回 ""）。
	GetCurrConversationID func(unifiedMsgOrigin string) string
	// NewConversation 新建会话（设为当前），返回其 ID。
	NewConversation func(unifiedMsgOrigin, platformID, personaID string) string
	// GetConversation 按 umo+cid 取会话（createIfNotExists 时不存在则新建），
	// 返回序列化 JSON map（cid/title/persona_id/history/updated_at/...）。
	GetConversation func(unifiedMsgOrigin, cid string, createIfNotExists bool) map[string]any
	// GetConversations 列出 umo 的全部会话（umo 空 = 全部）。
	GetConversations func(unifiedMsgOrigin string) []map[string]any
	// DeleteConversation 删除会话（cid 空 = 当前会话）。
	DeleteConversation func(unifiedMsgOrigin, cid string) error
	// SwitchConversation 切换 umo 的当前会话。
	SwitchConversation func(unifiedMsgOrigin, cid string) error
	// UpdateConversationTitle 更新会话标题。
	UpdateConversationTitle func(unifiedMsgOrigin, cid, title string) error
	// UpdateConversationPersonaID 更新会话绑定人格。
	UpdateConversationPersonaID func(unifiedMsgOrigin, cid, personaID string) error

	// ── 人格管理（对齐 Python persona_manager）──
	// GetPersonas 返回全部人格（PersonaPayload 序列化 map）。
	GetPersonas func() []map[string]any
	// GetDefaultPersona 按 umo 解析默认人格。
	GetDefaultPersona func(umo string) map[string]any
	// GetPersonaTree 返回文件夹树（嵌套）与全部人格。
	GetPersonaTree func() (folders []map[string]any, personas []map[string]any)
	// ResolveSelectedPersona 解析当前生效人格。
	ResolveSelectedPersona func(umo, conversationPersonaID, platformName string, providerSettings map[string]any) (personaID, personaName, personaPrompt, forceAppliedPersonaID string, isDefault bool)

	// ── Provider 管理（对齐 Python provider_manager）──
	// ListProviders 按能力类型列出全部 provider。
	ListProviders func(capability string) []map[string]any
	// GetUsingProvider 取 umo 当前使用的 provider（按能力类型）。
	GetUsingProvider func(umo, capability string) map[string]any
	// SetProvider 设置 umo 的当前 provider。
	SetProvider func(umo, providerID, capability string) error
	// GetProviderModels 取 provider 的模型列表。
	GetProviderModels func(providerID string) []string

	// ── 插件/Star 管理（对齐 Python star_manager）──
	// GetPluginRegistry 返回全部已安装插件元数据。
	GetPluginRegistry func() []map[string]any
	// GetStar 按插件名取元数据。
	GetStar func(name string) map[string]any
	// SetPluginEnabled 启用/禁用插件。
	SetPluginEnabled func(pluginName string, enabled bool) error
	// InstallPlugin 安装插件（git/url 源）。
	InstallPlugin func(repo string) error
	// UninstallPlugin 卸载插件。
	UninstallPlugin func(pluginName string) error
	// ListCommandDescriptors 返回全部插件的命令描述符（JSON 序列化，
	// 含插件名/命令/别名/描述/权限/子命令/组），helps 类插件跨进程枚举指令。
	ListCommandDescriptors func() []map[string]any
	// ListPlatforms 返回全部已加载平台实例元数据（JSON 序列化，含
	// id/type/name/display_name/config），群分析类插件发现平台。
	ListPlatforms func() []map[string]any

	// ── 会话等待（SessionWaiter）──
	// RegisterSessionWait 注册插件对 umo 的等待，返回 wait_id（空 = 不支持）。
	// pluginName 由 SDK 侧从连接身份（s.pluginID）自动注入，宿主据此记录等待
	// 归属插件（proto 未携带插件名字段，避免改协议）。
	RegisterSessionWait func(pluginName, umo string, timeoutSeconds int32) string
	// UnregisterSessionWait 注销等待。
	UnregisterSessionWait func(waitID string)

	// ── 桥接钩子（botpy/telegram 等兼容层）──
	// RegisterBridgeHook 注册插件的桥接钩子，宿主收到入站消息时把序列化事件
	// 推给该插件的 HandleHook(name=hookName)。pluginName 由 SDK 侧从连接身份
	// 注入。
	RegisterBridgeHook func(pluginName, hookName string) error
	// UnregisterBridgeHook 注销桥接钩子。
	UnregisterBridgeHook func(pluginName, hookName string) error

	// ── 大文件 Blob 存储（P0-2）──
	// CreateBlob 持久化 data 并返回受控 handle（宿主统一 TTL/GC，插件不传
	// 任意文件路径）。
	CreateBlob func(data []byte, mimeType, filename string, ttlSeconds int32) (*FileReference, error)
	// ReadBlob 按 offset/limit 分块读。
	ReadBlob func(handleID string, offset int64, limit int32) ([]byte, bool, int64, error)
	// GetBlobInfo 返回 blob 元数据。
	GetBlobInfo func(handleID string) (*FileReference, error)
	// ReleaseBlob 主动标记删除（最终删除仍由宿主 TTL/GC 判定）。
	ReleaseBlob func(handleID string) error

	// ── 技能（Skills，宿主 internal/skills 能力）──
	// ListSkills 返回宿主技能管理器的全部技能（GB SkillInfo JSON）。
	ListSkills func() []map[string]any
	// SetSkillActive 启用/禁用指定技能。
	SetSkillActive func(name string, active bool) error
	// DeleteSkill 删除指定技能。
	DeleteSkill func(name string) error

	// ── 平台消息历史（宿主 db platform_message_history）──
	// GetPlatformMessageHistory 按平台/用户取最近 limit 条记录。
	GetPlatformMessageHistory func(platformID, userID string, limit int32) []map[string]any
	// InsertPlatformMessageHistory 插入一条记录，返回完整记录 dict
	//（id/created_at 由宿主生成）。
	InsertPlatformMessageHistory func(platformID, userID, senderID string, content any, llmCheckpointID string, maxMessages int32) map[string]any
	// UpdatePlatformMessageHistory 更新一条记录（content 或 llm_checkpoint_id）。
	UpdatePlatformMessageHistory func(id int64, content any, llmCheckpointID string) error
	// DeletePlatformMessageHistory 按 ID 删除一条记录。
	DeletePlatformMessageHistory func(id int64) error

	// ── 技能视图扩展（sandbox runtime 视图）──
	// ListSkillsV2 带过滤参数的技能列表：activeOnly 仅返回启用技能；runtime
	// 过滤运行时视图（"local"/"sandbox"/""=全部）；showSandboxPath 返回
	// sandbox 路径而非宿主本地路径。
	ListSkillsV2 func(activeOnly bool, runtime string, showSandboxPath bool) []map[string]any

	// ── 知识库（宿主 internal/knowledgebase）──
	// KBRetrieve 检索知识库：kbNames 为空 = 宿主全部知识库；topKFusion 为
	// 融合召回数、topMFinal 为最终保留条数（<=0 用宿主默认）。返回拼接后的
	// 上下文文本与检索结果 JSON 数组（无命中时 context_text 为空、
	// results_json 为 "[]"）。
	KBRetrieve func(query string, kbNames []string, topKFusion, topMFinal int) (contextText string, resultsJSON string, err error)
	// KBUploadFromURL 让宿主从 URL 拉取文档写入指定知识库并分块
	//（chunkSize/chunkOverlap <=0 用宿主默认；kbNameOrID 为知识库名或 ID）。
	KBUploadFromURL func(kbNameOrID, url string, chunkSize, chunkOverlap int) error
	// KBListKBs 返回宿主全部知识库元数据（每项为 KnowledgeBase 结构的
	// snake_case map：kb_id/kb_name/description/...）。
	KBListKBs func() []map[string]any

	// ── 文件令牌（file_token 文件服务）──
	// RegisterFileToken 把宿主侧文件路径登记为随机不可枚举的令牌
	//（timeoutSec <=0 用宿主默认 TTL），下游凭 token 经宿主公开文件路由
	// 读取，避免暴露真实路径。
	RegisterFileToken func(path string, timeoutSec int32) (string, error)

	// ── 插件定时任务（宿主 internal/cron）──
	// CronCreate 创建定时任务，返回宿主 Job 快照 map（job_id/name/...）。
	CronCreate func(spec *CronCreateSpec) (map[string]any, error)
	// CronUpdate 按 jobID 更新任务字段（fields 仅含需更新的键：
	// name/cron_expression/timezone/payload/description/enabled），返回更新
	// 后的 Job 快照。
	CronUpdate func(jobID string, fields map[string]any) (map[string]any, error)
	// CronDelete 删除指定定时任务。
	CronDelete func(jobID string) error
	// CronList 列出定时任务（jobType 空 = 全部类型）。
	CronList func(jobType string) []map[string]any
	// CronRunNow 立即触发一次指定任务。
	CronRunNow func(jobID string) error

	// ── 宿主 MCP 读写桥接（只读列出 + 调用宿主侧 MCP 工具；插件自管 MCP
	// 不经此通道）──
	// McpListTools 汇总宿主已连接 MCP server 的全部工具（每项含
	// server/name/description/schema_json）。
	McpListTools func() []map[string]any
	// McpCallTool 调用宿主侧 MCP 工具：result 为完整结果对象
	//（{"content": [...], "isError": bool}），text 为纯文本摘要，isError
	// 标记宿主侧调用是否出错。
	McpCallTool func(server, toolName string, args map[string]any) (result map[string]any, text string, isError bool, err error)
}

var (
	hostHooksMu sync.RWMutex
	hostHooks   HostServiceHooks
)

// SetHostHooks installs the host-side implementation of HostService. Call it
// once from the host before launching any plugin client process.
func SetHostHooks(h HostServiceHooks) {
	hostHooksMu.Lock()
	defer hostHooksMu.Unlock()
	hostHooks = h
}

func getHostHooks() HostServiceHooks {
	hostHooksMu.RLock()
	defer hostHooksMu.RUnlock()
	return hostHooks
}

// hostServiceLoggerMu 保护 hostServiceLogger（SetHostServiceLogger 写、
// warnJSON 读）。
var hostServiceLoggerMu sync.RWMutex

// hostServiceLogger 是宿主侧 HostService 的告警日志通道。SDK 没有宿主注入
// 的 logger，默认输出到 stderr；宿主可在启动插件前通过 SetHostServiceLogger
// 替换为自带 logger。
var hostServiceLogger Logger = func() Logger {
	l := newStdLogger("astrbot-sdk.hostservice")
	l.SetLevelName("WARNING")
	return l
}()

// SetHostServiceLogger 替换宿主侧 HostService 的告警 logger（nil 忽略）。
func SetHostServiceLogger(l Logger) {
	if l == nil {
		return
	}
	hostServiceLoggerMu.Lock()
	hostServiceLogger = l
	hostServiceLoggerMu.Unlock()
}

// warnJSON 记录 HostService 处理中 JSON 编解码失败，避免被 `_ =` 静默吞掉
// （26-4）。调用方行为不变：尽力降级为空值并继续。
func warnJSON(what string, err error) {
	if err != nil {
		hostServiceLoggerMu.RLock()
		logger := hostServiceLogger
		hostServiceLoggerMu.RUnlock()
		logger.Warn("host service JSON 处理失败", "what", what, "err", err)
	}
}

// requireIdentity 是控制面 HostService RPC 的最小鉴权：插件管理、会话/
// Provider 控制、会话等待注册、配置读写等会改动宿主生态或状态的操作必须
// 绑定身份（s.pluginID 非空）。宿主未设置身份时（SetCurrentHostPluginID /
// BindHostServiceName 未生效）pluginID 为空，一律拒绝，防止匿名/未绑定身份
// 插件接管宿主插件生态（26-2）。
func (s *HostServiceServer) requireIdentity() error {
	if s.identity() == "" {
		return Error(CodePermissionDenied, "control-plane HostService RPC requires a bound plugin identity")
	}
	return nil
}

// DropPluginHostState 在插件连接关闭时清理该连接遗留的宿主侧状态：
// hostServers 连接登记与 ChatLLM/CallAction 限流窗口，避免表只增不减
// （26-3）。connKey 是 accept 时刻的 manifest id（hostServers 的 key）；
// 若期间经 BindHostServiceName 更新了注册名，限流表还可能有注册名条目，
// 一并清理。srv 用于归属比对：仅当 hostServers[connKey] 仍指向本连接时才
// 删除，避免重载竞态下旧连接 Close 误删后继连接的登记。
func DropPluginHostState(connKey string, srv *HostServiceServer) {
	if connKey == "" {
		return
	}
	registered := ""
	hostServersMu.Lock()
	cur, ok := hostServers[connKey]
	if ok && cur == srv {
		registered = srv.identity()
		delete(hostServers, connKey)
	}
	hostServersMu.Unlock()
	if !ok || cur == srv { // 无后继连接（或登记仍属本连接）时才清理限流
		chatLLMRate.drop(connKey)
		callActionRate.drop(connKey)
		if registered != "" && registered != connKey {
			chatLLMRate.drop(registered)
			callActionRate.drop(registered)
		}
	}
}

// HostServiceServer implements the native HostService interface on the host
// side, delegating to the hooks installed via SetHostHooks. Each plugin
// connection gets its own instance, bound to the plugin id the host was loading
// when the connection was accepted, so reverse calls can be validated
// per-plugin.
type HostServiceServer struct {
	// idMu 保护 pluginID（BindHostServiceName 写、各 RPC 读）。
	idMu sync.RWMutex
	// pluginID 是当前连接的注册名：accept 时为 manifest id，Register 后由
	// BindHostServiceName 更新为插件自报的注册名。仅用于配置归属校验
	//（GetConfig/SetConfig 的 req.PluginName != identity()）与以注册名为参数
	// 的 hook；切勿用于管理鉴权（注册名可被冒用）。
	pluginID string
	// connKey 是 accept 时刻绑定的 manifest id（hostServers 表 key）：
	//   - 管理鉴权键（connectionID），manifest id 由宿主分配、插件无法自报；
	//   - 连接关闭时用于清理 hostServers 与限流表条目（26-3）。
	// BindHostServiceName 只改注册名 pluginID，绝不清空/覆盖 connKey。
	connKey string
	// sessionWaitMu 保护 sessionWaitIDs / sessionWaitHasID。
	sessionWaitMu sync.Mutex
	// sessionWaitIDs 是本连接注册到宿主的 wait_id 集合，Unregister 时据此
	// 做归属校验，防止枚举他人 wait_id 跨插件注销。
	sessionWaitIDs map[string]struct{}
	// sessionWaitHasID 标记宿主是否曾返回非空 wait_id（支持 wait_id 特性）；
	// 为 false 时（宿主不支持）放宽为不做归属校验，保持旧行为兼容。
	sessionWaitHasID bool
}

// HostServiceServer is the host-side HostService implementation.
var _ HostService = (*HostServiceServer)(nil)

// identity 返回当前连接的注册名（带锁读 pluginID）。注册名是插件 Register 时
// 自报的名字，只能用于"配置归属"校验（GetConfig/SetConfig 的
// req.PluginName != s.identity()）与以注册名为参数的 hook。
//
// 安全：注册名可被任意插件声明，绝不能作为管理鉴权键——恶意插件只要与管理员
// 插件重名即可冒充。管理鉴权一律用 connectionID()（连接绑定的 manifest id）。
func (s *HostServiceServer) identity() string {
	s.idMu.RLock()
	defer s.idMu.RUnlock()
	return s.pluginID
}

// connectionID 返回 accept 时刻绑定到本连接的 manifest id（connKey），是管理
// 鉴权（hostAdminAuthorized）的唯一键。manifest id 由宿主按安装来源分配、插件
// 无法自行声明，故与管理员插件重名也无法冒充。
//
// connKey 为空（旧宿主未调用 SetCurrentHostPluginID，或测试直接构造）时回退
// 注册名 pluginID，保持兼容；connKey 一旦绑定不再变更，无需加锁。
func (s *HostServiceServer) connectionID() string {
	if s.connKey != "" {
		return s.connKey
	}
	s.idMu.RLock()
	defer s.idMu.RUnlock()
	return s.pluginID
}

// SharedRuntimeConnKey 是共享 Runtime（python-shared）连接的 connKey 哨兵值：
// 一个进程承载 N 个插件，连接级身份不能代表某个具体插件，故身份改为按
// **请求**携带的 plugin_name 经宿主注入的解析器（SetSharedPluginResolver）
// 解析为 manifest id（方案第 4/8 节「身份绑定由 per-connection 改为
// per-request/per-session」）。
const SharedRuntimeConnKey = "__shared_runtime__"

var (
	sharedResolverMu sync.RWMutex
	// sharedPluginResolver 把共享 Runtime 反调用携带的插件注册名解析为
	// manifest id；由宿主注入（pluginConfigID）。返回 "" 表示无法唯一解析
	// （fail-closed）。
	sharedPluginResolver func(string) string
)

// SetSharedPluginResolver 注入共享 Runtime 的「注册名 → manifest id」解析器。
// 仅在宿主托管 python-shared 时设置；单插件进程不需要。
func SetSharedPluginResolver(fn func(string) string) {
	sharedResolverMu.Lock()
	sharedPluginResolver = fn
	sharedResolverMu.Unlock()
}

// isSharedConn 报告本连接是否为共享 Runtime（多租户）。
func (s *HostServiceServer) isSharedConn() bool {
	return s.connKey == SharedRuntimeConnKey
}

// authID 返回用于管理鉴权 / 配置归属的 manifest id：
//   - 单插件连接：连接绑定的 manifest id（connKey）；
//   - 共享 Runtime：按请求 plugin_name 经宿主解析器解析（per-request）；
//     解析失败返回 ""（fail-closed，拒绝）。
func (s *HostServiceServer) authID(pluginName string) string {
	if s.isSharedConn() {
		sharedResolverMu.RLock()
		fn := sharedPluginResolver
		sharedResolverMu.RUnlock()
		if fn == nil {
			return ""
		}
		return fn(pluginName)
	}
	return s.connectionID()
}

// ownsConfig 报告本连接是否有权读写 pluginName 的配置。
//   - 单插件连接：注册名必须与连接身份一致（严格）；
//   - 共享 Runtime：连接承载多插件，无法从连接判定调用方，退化为「目标名能
//     解析为已知 manifest id 即放行」。这是共享模式的**已知隔离损失**（方案
//     第 7 节：共享进程无进程级隔离）；需要严格配置隔离的插件应走 python-grpc。
func (s *HostServiceServer) ownsConfig(pluginName string) bool {
	if s.isSharedConn() {
		return s.authID(pluginName) != ""
	}
	return pluginName == s.identity()
}

// hostPluginID is the id of the plugin the host is currently establishing a
// connection for. The host sets it (SetCurrentHostPluginID) right before
// go-plugin Dispense; acceptHostService reads it so the per-connection
// HostServiceServer knows which plugin it serves.
var (
	hostPluginIDMu sync.Mutex
	hostPluginID   string

	// hostServers 记录 accept 时创建的 per-connection HostServiceServer
	//（key=插件 manifest id），供宿主在 Register 后用注册名更新身份。
	hostServersMu sync.Mutex
	hostServers   = map[string]*HostServiceServer{}
)

// SetCurrentHostPluginID records the plugin id being loaded so the next
// acceptHostService call can bind HostService reverse-call validation to it.
// The host calls this with id before Dispense and with "" afterwards. Loads
// of different plugins are effectively serialized (startInstance waits for the
// handshake), so the window is safe in practice.
func SetCurrentHostPluginID(id string) {
	hostPluginIDMu.Lock()
	hostPluginID = id
	hostPluginIDMu.Unlock()
}

func currentHostPluginID() string {
	hostPluginIDMu.Lock()
	defer hostPluginIDMu.Unlock()
	return hostPluginID
}

// CurrentHostPluginID is the exported accessor used by the gRPC transport's
// acceptHostService to bind the per-connection identity.
func CurrentHostPluginID() string { return currentHostPluginID() }

// HostServiceLogWarn logs through the host-service logger (used by the gRPC
// transport for accept-time diagnostics).
func HostServiceLogWarn(msg string, args ...any) {
	hostServiceLoggerMu.RLock()
	l := hostServiceLogger
	hostServiceLoggerMu.RUnlock()
	l.Warn(msg, args...)
}

// ConnKey returns the connection's accept-time manifest id (hostServers key).
func (s *HostServiceServer) ConnKey() string { return s.connKey }

// NewHostServiceServer creates and registers a per-connection host-service
// implementation. The gRPC transport serves it; the Native transport returns it
// as the plugin's in-process reverse-call client.
func NewHostServiceServer(pluginID, connKey string) *HostServiceServer {
	s := &HostServiceServer{pluginID: pluginID, connKey: connKey}
	if pluginID != "" {
		hostServersMu.Lock()
		hostServers[pluginID] = s
		hostServersMu.Unlock()
	}
	return s
}

// BindHostServiceName updates the per-connection HostService server's plugin
// identity to the plugin's registered name（Register 返回值）。插件
// GetConfig/SetConfig 传的是注册名，而 accept 时只绑定 manifest id，二者
// 可能不同（如 jm_cosmos vs astrbot_plugin_jm_cosmos），故宿主在 Register
// 成功后调用本函数对齐"配置归属"身份，保证身份隔离校验通过。
//
// 安全：只更新注册名 pluginID，绝不触碰 connKey——管理鉴权始终以 accept 时
// 绑定的 manifest id（connectionID）为准，注册名不得反过来覆盖鉴权键，否则
// 重名冒充漏洞复现。
func BindHostServiceName(id, name string) {
	hostServersMu.Lock()
	defer hostServersMu.Unlock()
	if s, ok := hostServers[id]; ok {
		s.idMu.Lock()
		s.pluginID = name
		s.idMu.Unlock()
	}
}

func (s *HostServiceServer) CallAction(_ context.Context, platform, api string, params map[string]any) (map[string]any, error) {
	// CallAction 是高频平台 API 入口，做与 ChatLLM 同款的窗口限流（26-6）。
	id := s.identity()
	if !callActionRate.allow(id) {
		return nil, Errorf(CodeResourceExhausted, "插件 %q CallAction 调用过于频繁（每分钟上限 %d 次）", id, callActionRate.limit())
	}
	h := getHostHooks()
	if h.CallAction == nil {
		return map[string]any{}, nil
	}
	return h.CallAction(platform, api, params)
}

func (s *HostServiceServer) SendMessage(_ context.Context, platform, sessionID string, chain []Component) error {
	h := getHostHooks()
	if h.SendMessage == nil {
		return nil
	}
	return h.SendMessage(platform, sessionID, chain)
}

func (s *HostServiceServer) RecallMessage(_ context.Context, platform, messageID string) error {
	h := getHostHooks()
	if h.RecallMessage == nil {
		return nil
	}
	return h.RecallMessage(platform, messageID)
}

// configTargetID 解析配置读写的目标租户 id，并做归属校验（fail-closed）。
//
//   - 共享 Runtime（多租户）：plugin_id 由插件侧 current PluginSession 注入，
//     是唯一可靠的租户身份。优先用 plugin_id 直接定位；同时给出注册名时，
//     若注册名解析出的 id 与 plugin_id 不一致则拒绝（防止 id/名错配）。
//     **不再**以注册名作为路由依据（消除同名歧义与"按名探测他人配置"）。
//   - 单插件连接：目标必须等于连接身份（严格），保持既有行为。
func (s *HostServiceServer) configTargetID(pluginName, pluginID string) (string, error) {
	if s.isSharedConn() {
		id := pluginID
		if pluginName != "" {
			rid := s.authID(pluginName)
			if rid != "" && id != "" && rid != id {
				return "", Errorf(CodePermissionDenied, "plugin_id %q 与注册名 %q 解析结果 %q 不一致，已拒绝", id, pluginName, rid)
			}
			if id == "" {
				id = rid // 旧 SDK 未带 plugin_id：回退按注册名解析（已知隔离损失）
			}
		}
		if id == "" {
			return "", Errorf(CodePermissionDenied, "共享 Runtime 缺少 plugin_id 且注册名 %q 无法唯一解析", pluginName)
		}
		return id, nil
	}
	// 单插件连接：目标名/id（若给出）必须与本连接身份一致。
	if pluginID != "" && pluginID != s.connectionID() {
		return "", Errorf(CodePermissionDenied, "插件 %q 无权访问插件 %q 的配置", s.identity(), pluginID)
	}
	if pluginName != "" && pluginName != s.identity() {
		return "", Errorf(CodePermissionDenied, "插件 %q 无权访问插件 %q 的配置", s.identity(), pluginName)
	}
	return s.connectionID(), nil
}

func (s *HostServiceServer) GetConfig(_ context.Context, pluginName, pluginID string) (map[string]any, error) {
	// 身份隔离：插件只能读取自己的配置，禁止探测/读取其他插件配置
	//（插件自身以宿主用户运行、可直接读文件系统，此校验是纵深防御，
	//  真正隔离需插件降权/容器化）。空身份一律拒绝（fail-closed）。
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	targetID, err := s.configTargetID(pluginName, pluginID)
	if err != nil {
		return nil, err
	}
	h := getHostHooks()
	// 多租户优先走按 id 精确解析；旧宿主未提供时回退按注册名。
	if h.GetConfigByID != nil {
		return h.GetConfigByID(targetID)
	}
	if h.GetConfig == nil {
		return map[string]any{}, nil
	}
	return h.GetConfig(pluginName)
}

func (s *HostServiceServer) SetConfig(_ context.Context, pluginName, pluginID string, cfg map[string]any) error {
	// 身份隔离：插件只能写自己的配置，禁止篡改其他插件配置。空身份
	// 一律拒绝（fail-closed）。
	if err := s.requireIdentity(); err != nil {
		return err
	}
	targetID, err := s.configTargetID(pluginName, pluginID)
	if err != nil {
		return err
	}
	h := getHostHooks()
	if h.SetConfigByID != nil {
		return h.SetConfigByID(targetID, cfg)
	}
	if h.SetConfig == nil {
		return nil
	}
	return h.SetConfig(pluginName, cfg)
}

// rateWindow 记录某插件在一个固定窗口内的调用计数。
type rateWindow struct {
	window time.Time
	count  int
}

// rateTable 是"插件→窗口计数"的限流表，用 pluginID 作为 key。只增不减会
// 在插件频繁装卸/长期运行时持续累积（26-3），故提供 drop 供连接关闭时清理。
// pluginID 空时（旧宿主未设置身份）归入独立的 __anonymous__ 保守配额，
// 避免匿名调用绕过限流（fail-closed）。
type rateTable struct {
	mu     sync.Mutex
	maxPer int
	table  map[string]rateWindow
}

// allow 返回本次调用是否放行，并推进窗口计数。
func (r *rateTable) allow(id string) bool {
	if r == nil {
		return true
	}
	if id == "" {
		id = "__anonymous__"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// 窗口对齐整分钟（now.Truncate），避免任意起点使窗口尾+新窗口头
	// 各计满 maxPer，边界突刺达 2 倍上限。
	bucket := time.Now().Truncate(time.Minute)
	e := r.table[id]
	if !e.window.Equal(bucket) {
		e.window = bucket
		e.count = 0
	}
	e.count++
	r.table[id] = e
	return e.count <= r.maxPer
}

// limit 返回当前窗口上限（带锁读，供错误信息展示）。
func (r *rateTable) limit() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxPer
}

// drop 删除某插件 id 的限流条目，供连接关闭时清理（26-3）。
func (r *rateTable) drop(id string) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	delete(r.table, id)
	r.mu.Unlock()
}

// chatLLMRate 对插件的 ChatLLM 反向调用限流（每插件每分钟上限
// maxChatLLMPerMinute），防止恶意/失控插件无限调用宿主 LLM 消耗额度。
var chatLLMRate = &rateTable{maxPer: maxChatLLMPerMinute, table: map[string]rateWindow{}}

// callActionRate 对插件的 CallAction 反向调用做同款限流（26-6）：CallAction
// 是高频平台 API 入口，同样可能被滥用。
var callActionRate = &rateTable{maxPer: maxChatLLMPerMinute, table: map[string]rateWindow{}}

// SetChatLLMRateLimit 调整每插件每分钟 ChatLLM 与 CallAction 反向调用上限
// （两者同源共用一份配置，26-6），供宿主在启动前配置。未调用时默认 30。
// 返回设置前的旧值。
func SetChatLLMRateLimit(perMinute int) int {
	old := setRateLimit(chatLLMRate, perMinute)
	setRateLimit(callActionRate, perMinute)
	return old
}

// setRateLimit 设置单个限流表的每插件每分钟上限（<=0 保持原值不变，
// 即不提供关闭限流的开关）。
func setRateLimit(r *rateTable, perMinute int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.maxPer
	if perMinute > 0 {
		r.maxPer = perMinute
	}
	return old
}

func (s *HostServiceServer) ChatLLM(_ context.Context, req *ChatLLMRequest) (string, error) {
	id := s.identity()
	if !chatLLMRate.allow(id) {
		return "", Errorf(CodeResourceExhausted, "插件 %q ChatLLM 调用过于频繁（每分钟上限 %d 次）", id, chatLLMRate.limit())
	}
	h := getHostHooks()
	if h.ChatLLM == nil {
		return "", nil
	}
	return h.ChatLLM(req)
}

// React adds an emoji reaction to a message on a platform adapter.
func (s *HostServiceServer) React(_ context.Context, platform, sessionID, messageID, emoji string) error {
	h := getHostHooks()
	if h.React == nil {
		return nil
	}
	return h.React(platform, sessionID, messageID, emoji)
}

// TextToImage renders text into an image via the host t2i engine.
func (s *HostServiceServer) TextToImage(_ context.Context, text, templateName string) (string, error) {
	h := getHostHooks()
	if h.TextToImage == nil {
		return "", nil
	}
	return h.TextToImage(text, templateName)
}

// HtmlRender renders an HTML template + data into an image via the host.
func (s *HostServiceServer) HtmlRender(_ context.Context, template, data, options string) (string, error) {
	h := getHostHooks()
	if h.HtmlRender == nil {
		return "", nil
	}
	return h.HtmlRender(template, data, options)
}

// ── 会话管理 RPC 实现 ──────────────────────────────────────────────────────

func (s *HostServiceServer) GetCurrConversationID(_ context.Context, unifiedMsgOrigin string) string {
	h := getHostHooks()
	if h.GetCurrConversationID == nil {
		return ""
	}
	return h.GetCurrConversationID(unifiedMsgOrigin)
}

func (s *HostServiceServer) NewConversation(_ context.Context, unifiedMsgOrigin, platformID, personaID string) string {
	// 新建会话会"设为当前"，等效于重置该用户的对话上下文，与其他会话
	// 变更 RPC 一致地要求绑定身份。
	if err := s.requireIdentity(); err != nil {
		return ""
	}
	h := getHostHooks()
	if h.NewConversation == nil {
		return ""
	}
	return h.NewConversation(unifiedMsgOrigin, platformID, personaID)
}

func (s *HostServiceServer) GetConversation(_ context.Context, unifiedMsgOrigin, cid string, createIfNotExists bool) map[string]any {
	h := getHostHooks()
	if h.GetConversation == nil {
		return nil
	}
	return h.GetConversation(unifiedMsgOrigin, cid, createIfNotExists)
}

func (s *HostServiceServer) GetConversations(_ context.Context, unifiedMsgOrigin string) []map[string]any {
	// 会话含完整聊天历史，属隐私敏感数据：要求绑定身份，且禁止经插件
	// RPC 全量列举（空 umo = 所有用户/平台的全部会话）。
	if err := s.requireIdentity(); err != nil {
		return nil
	}
	if unifiedMsgOrigin == "" {
		return nil
	}
	h := getHostHooks()
	if h.GetConversations == nil {
		return nil
	}
	return h.GetConversations(unifiedMsgOrigin)
}

func (s *HostServiceServer) DeleteConversation(_ context.Context, unifiedMsgOrigin, cid string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.DeleteConversation == nil {
		return nil
	}
	return h.DeleteConversation(unifiedMsgOrigin, cid)
}

func (s *HostServiceServer) SwitchConversation(_ context.Context, unifiedMsgOrigin, cid string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.SwitchConversation == nil {
		return nil
	}
	return h.SwitchConversation(unifiedMsgOrigin, cid)
}

func (s *HostServiceServer) UpdateConversationTitle(_ context.Context, unifiedMsgOrigin, cid, title string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.UpdateConversationTitle == nil {
		return nil
	}
	return h.UpdateConversationTitle(unifiedMsgOrigin, cid, title)
}

func (s *HostServiceServer) UpdateConversationPersonaID(_ context.Context, unifiedMsgOrigin, cid, personaID string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.UpdateConversationPersonaID == nil {
		return nil
	}
	return h.UpdateConversationPersonaID(unifiedMsgOrigin, cid, personaID)
}

// ── 人格管理 RPC 实现 ──────────────────────────────────────────────────────

func (s *HostServiceServer) GetPersonas(_ context.Context) []map[string]any {
	h := getHostHooks()
	if h.GetPersonas == nil {
		return nil
	}
	return h.GetPersonas()
}

func (s *HostServiceServer) GetDefaultPersona(_ context.Context, umo string) map[string]any {
	h := getHostHooks()
	if h.GetDefaultPersona == nil {
		return nil
	}
	return h.GetDefaultPersona(umo)
}

func (s *HostServiceServer) GetPersonaTree(_ context.Context) (folders []map[string]any, personas []map[string]any) {
	h := getHostHooks()
	if h.GetPersonaTree == nil {
		return nil, nil
	}
	return h.GetPersonaTree()
}

func (s *HostServiceServer) ResolveSelectedPersona(_ context.Context, umo, conversationPersonaID, platformName string, providerSettings map[string]any) (personaID, personaName, personaPrompt, forceAppliedPersonaID string, isDefault bool) {
	h := getHostHooks()
	if h.ResolveSelectedPersona == nil {
		return "", "", "", "", false
	}
	return h.ResolveSelectedPersona(umo, conversationPersonaID, platformName, providerSettings)
}

// ── Provider 管理 RPC 实现 ─────────────────────────────────────────────────

func (s *HostServiceServer) ListProviders(_ context.Context, capability string) []map[string]any {
	h := getHostHooks()
	if h.ListProviders == nil {
		return nil
	}
	return h.ListProviders(capability)
}

func (s *HostServiceServer) GetUsingProvider(_ context.Context, umo, capability string) map[string]any {
	h := getHostHooks()
	if h.GetUsingProvider == nil {
		return nil
	}
	return h.GetUsingProvider(umo, capability)
}

func (s *HostServiceServer) SetProvider(_ context.Context, umo, providerID, capability string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.SetProvider == nil {
		return nil
	}
	return h.SetProvider(umo, providerID, capability)
}

func (s *HostServiceServer) GetProviderModels(_ context.Context, providerID string) []string {
	h := getHostHooks()
	if h.GetProviderModels == nil {
		return nil
	}
	return h.GetProviderModels(providerID)
}

// ── 插件/Star 管理 RPC 实现 ────────────────────────────────────────────────

func (s *HostServiceServer) GetPluginRegistry(_ context.Context) []map[string]any {
	h := getHostHooks()
	if h.GetPluginRegistry == nil {
		return nil
	}
	return h.GetPluginRegistry()
}

func (s *HostServiceServer) GetStar(_ context.Context, name string) map[string]any {
	h := getHostHooks()
	if h.GetStar == nil {
		return nil
	}
	return h.GetStar(name)
}

func (s *HostServiceServer) SetPluginEnabled(_ context.Context, pluginName string, enabled bool) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	// 目标校验：插件只能启停自身（按注册名比对 s.identity()），操作其他插件
	// 须在管理员名单内。管理鉴权以连接绑定的 manifest id（connectionID）为键，
	// 注册名重名无法冒充（26-2 / p11）。
	if !s.ownsConfig(pluginName) && !hostAdminAuthorized(s.connectionID()) {
		return Errorf(CodePermissionDenied,
			"插件 %q 无权操作插件 %q（仅允许操作自身，或经宿主授权的管理插件）", s.identity(), pluginName)
	}
	h := getHostHooks()
	if h.SetPluginEnabled == nil {
		return nil
	}
	return h.SetPluginEnabled(pluginName, enabled)
}

func (s *HostServiceServer) InstallPlugin(_ context.Context, repo string) error {
	// 安装接受任意 git/url 源，等价于把 RCE 安装面暴露给插件，只允许
	// 管理员名单内的插件执行。
	if err := s.requireIdentity(); err != nil {
		return err
	}
	// 管理鉴权键 = 连接绑定的 manifest id（connectionID），不是可被冒用的注册名。
	if !hostAdminAuthorized(s.connectionID()) {
		return Errorf(CodePermissionDenied, "插件 %q 无权安装插件（需宿主授权为管理插件）", s.identity())
	}
	h := getHostHooks()
	if h.InstallPlugin == nil {
		return nil
	}
	return h.InstallPlugin(repo)
}

func (s *HostServiceServer) UninstallPlugin(_ context.Context, pluginName string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	// 卸载只允许管理员名单内的插件执行（自身也在名单内时受同一约束）。
	// 管理鉴权键 = 连接绑定的 manifest id（connectionID），不是可被冒用的注册名。
	if !hostAdminAuthorized(s.connectionID()) {
		return Errorf(CodePermissionDenied, "插件 %q 无权卸载插件（需宿主授权为管理插件）", s.identity())
	}
	h := getHostHooks()
	if h.UninstallPlugin == nil {
		return nil
	}
	return h.UninstallPlugin(pluginName)
}

// ListCommandDescriptors returns JSON-serialized command descriptors for all
// plugins (commands/sub-commands/groups/aliases/permission/description),
// consumed by helps-like plugins that enumerate commands across processes.
func (s *HostServiceServer) ListCommandDescriptors(_ context.Context) []map[string]any {
	h := getHostHooks()
	if h.ListCommandDescriptors == nil {
		return nil
	}
	return h.ListCommandDescriptors()
}

func (s *HostServiceServer) ListPlatforms(_ context.Context) []map[string]any {
	h := getHostHooks()
	if h.ListPlatforms == nil {
		return nil
	}
	return h.ListPlatforms()
}

// RegisterSessionWait registers a session wait for this plugin (the host
// feeds matching inbound events back via PluginService.FeedSessionWait).
// pluginName 从连接身份注入（s.pluginID，Register 后为注册名），宿主凭此
// 关联等待与插件实例。
func (s *HostServiceServer) RegisterSessionWait(_ context.Context, _ string, umo string, timeoutSeconds int32) string {
	// 空身份时拒绝注册：宿主无法把等待归属到任何插件，避免记录无主等待
	//（26-5）；同时归入控制面最小鉴权（26-2）。身份一律取连接绑定的
	// s.identity()，避免插件自报名称冒充（对齐旧 proto 无 plugin_name 字段
	// 时由 SDK 注入的语义）。
	if s.identity() == "" {
		return ""
	}
	h := getHostHooks()
	if h.RegisterSessionWait == nil {
		return ""
	}
	waitID := h.RegisterSessionWait(s.identity(), umo, timeoutSeconds)
	// 记录本连接注册的 wait_id 供 Unregister 归属校验；宿主返回非空 id
	// 视为支持 wait_id 特性，此后启用严格校验。
	s.sessionWaitMu.Lock()
	if s.sessionWaitIDs == nil {
		s.sessionWaitIDs = map[string]struct{}{}
	}
	s.sessionWaitIDs[waitID] = struct{}{}
	if waitID != "" {
		s.sessionWaitHasID = true
	}
	s.sessionWaitMu.Unlock()
	return waitID
}

// UnregisterSessionWait removes a previously registered session wait.
func (s *HostServiceServer) UnregisterSessionWait(_ context.Context, waitID string) {
	// 对齐 RegisterSessionWait 的控制面最小鉴权：注销等待同样需要绑定身份。
	if err := s.requireIdentity(); err != nil {
		return
	}
	// 归属校验：wait_id 特性启用后，只允许注销本连接注册过的 wait_id，
	// 防止枚举他人 wait_id 跨插件注销（26-5）。
	s.sessionWaitMu.Lock()
	_, owned := s.sessionWaitIDs[waitID]
	strict := s.sessionWaitHasID
	s.sessionWaitMu.Unlock()
	if strict && !owned {
		return
	}
	h := getHostHooks()
	if h.UnregisterSessionWait == nil {
		return
	}
	h.UnregisterSessionWait(waitID)
}

// RegisterBridgeHook 注册插件到宿主的桥接钩子（botpy/telegram 等兼容层）。
// pluginName 从连接身份注入（s.pluginID），宿主凭此关联钩子与插件实例。
// 空身份时拒绝（对齐 RegisterSessionWait 的控制面最小鉴权），避免登记无主
// 钩子。
func (s *HostServiceServer) RegisterBridgeHook(_ context.Context, _ string, hookName string) error {
	if s.identity() == "" {
		return Error(CodeFailedPrecondition, "cannot register bridge hook without a bound plugin identity")
	}
	h := getHostHooks()
	if h.RegisterBridgeHook == nil {
		return nil
	}
	return h.RegisterBridgeHook(s.identity(), hookName)
}

// UnregisterBridgeHook 注销插件到宿主的桥接钩子。与 Register 对称地要求
// 绑定身份：匿名连接不得注销任何钩子。
func (s *HostServiceServer) UnregisterBridgeHook(_ context.Context, _ string, hookName string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.UnregisterBridgeHook == nil {
		return nil
	}
	return h.UnregisterBridgeHook(s.identity(), hookName)
}

// CreateBlob 把插件的大二进制交由宿主持久化，返回受控 FileReference handle。
func (s *HostServiceServer) CreateBlob(_ context.Context, data []byte, mimeType, filename string, ttlSeconds int32) (*FileReference, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.CreateBlob == nil {
		return nil, Error(CodeUnimplemented, "host blob store not configured")
	}
	return h.CreateBlob(data, mimeType, filename, ttlSeconds)
}

// ReadBlob 按 offset/limit 分块读宿主 blob。
func (s *HostServiceServer) ReadBlob(_ context.Context, handleID string, offset int64, limit int32) ([]byte, bool, int64, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, false, 0, err
	}
	h := getHostHooks()
	if h.ReadBlob == nil {
		return nil, false, 0, Error(CodeUnimplemented, "host blob store not configured")
	}
	return h.ReadBlob(handleID, offset, limit)
}

// GetBlobInfo 返回 blob 元数据。
func (s *HostServiceServer) GetBlobInfo(_ context.Context, handleID string) (*FileReference, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.GetBlobInfo == nil {
		return nil, Error(CodeUnimplemented, "host blob store not configured")
	}
	return h.GetBlobInfo(handleID)
}

// ReleaseBlob 主动释放 blob（最终删除由宿主 TTL/GC 判定）。
func (s *HostServiceServer) ReleaseBlob(_ context.Context, handleID string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.ReleaseBlob == nil {
		return nil
	}
	return h.ReleaseBlob(handleID)
}

// ListSkills 返回宿主技能管理器中的全部技能（每条 SkillInfo JSON）。
func (s *HostServiceServer) ListSkills(_ context.Context) []map[string]any {
	if err := s.requireIdentity(); err != nil {
		return nil
	}
	h := getHostHooks()
	if h.ListSkills == nil {
		return nil
	}
	return h.ListSkills()
}

// SetSkillActive 启用/禁用指定技能。
func (s *HostServiceServer) SetSkillActive(_ context.Context, name string, active bool) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.SetSkillActive == nil {
		return nil
	}
	return h.SetSkillActive(name, active)
}

// DeleteSkill 删除指定技能。
func (s *HostServiceServer) DeleteSkill(_ context.Context, name string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.DeleteSkill == nil {
		return nil
	}
	return h.DeleteSkill(name)
}

// GetPlatformMessageHistory 按平台/用户取最近 limit 条平台消息记录。
func (s *HostServiceServer) GetPlatformMessageHistory(_ context.Context, platformID, userID string, limit int32) []map[string]any {
	if err := s.requireIdentity(); err != nil {
		return nil
	}
	h := getHostHooks()
	if h.GetPlatformMessageHistory == nil {
		return nil
	}
	return h.GetPlatformMessageHistory(platformID, userID, limit)
}

// InsertPlatformMessageHistory 插入一条平台消息记录（content 由调用方直接传入）。
func (s *HostServiceServer) InsertPlatformMessageHistory(_ context.Context, platformID, userID, senderID string, content any, llmCheckpointID string, maxMessages int32) map[string]any {
	if err := s.requireIdentity(); err != nil {
		return nil
	}
	h := getHostHooks()
	if h.InsertPlatformMessageHistory == nil {
		return nil
	}
	return h.InsertPlatformMessageHistory(platformID, userID, senderID, content, llmCheckpointID, maxMessages)
}

// UpdatePlatformMessageHistory 更新一条记录（content 可选；llm_checkpoint_id 空表示不更新）。
func (s *HostServiceServer) UpdatePlatformMessageHistory(_ context.Context, id int64, content any, llmCheckpointID string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.UpdatePlatformMessageHistory == nil {
		return nil
	}
	return h.UpdatePlatformMessageHistory(id, content, llmCheckpointID)
}

// DeletePlatformMessageHistory 按 ID 删除一条平台消息记录。
func (s *HostServiceServer) DeletePlatformMessageHistory(_ context.Context, id int64) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.DeletePlatformMessageHistory == nil {
		return nil
	}
	return h.DeletePlatformMessageHistory(id)
}

// ListSkillsV2 带过滤参数的技能列表（active_only/runtime/show_sandbox_path）。
func (s *HostServiceServer) ListSkillsV2(_ context.Context, activeOnly bool, runtime string, showSandboxPath bool) []map[string]any {
	if err := s.requireIdentity(); err != nil {
		return nil
	}
	h := getHostHooks()
	if h.ListSkillsV2 == nil {
		return nil
	}
	return h.ListSkillsV2(activeOnly, runtime, showSandboxPath)
}

// KBRetrieve 检索宿主知识库，返回拼接上下文文本与结果 JSON 数组。
func (s *HostServiceServer) KBRetrieve(_ context.Context, query string, kbNames []string, topKFusion, topMFinal int) (contextText string, resultsJSON string, err error) {
	if err := s.requireIdentity(); err != nil {
		return "", "", err
	}
	h := getHostHooks()
	if h.KBRetrieve == nil {
		return "", "", Error(CodeUnimplemented, "host hook KBRetrieve not configured")
	}
	contextText, resultsJSON, err = h.KBRetrieve(query, kbNames, topKFusion, topMFinal)
	if err != nil {
		return "", "", err
	}
	if resultsJSON == "" {
		resultsJSON = "[]"
	}
	return contextText, resultsJSON, nil
}

// KBUploadFromURL 让宿主从 URL 拉取文档写入指定知识库并分块。
func (s *HostServiceServer) KBUploadFromURL(_ context.Context, kbNameOrID, url string, chunkSize, chunkOverlap int) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.KBUploadFromURL == nil {
		return Error(CodeUnimplemented, "host hook KBUploadFromURL not configured")
	}
	return h.KBUploadFromURL(kbNameOrID, url, chunkSize, chunkOverlap)
}

// KBListKBs 列出宿主全部知识库元数据（每项 KnowledgeBase 结构 JSON）。
func (s *HostServiceServer) KBListKBs(_ context.Context) []map[string]any {
	if err := s.requireIdentity(); err != nil {
		return nil
	}
	h := getHostHooks()
	if h.KBListKBs == nil {
		return nil
	}
	return h.KBListKBs()
}

// RegisterFileToken 把宿主侧文件路径登记为随机不可枚举令牌。
func (s *HostServiceServer) RegisterFileToken(_ context.Context, path string, timeoutSec int32) (string, error) {
	if err := s.requireIdentity(); err != nil {
		return "", err
	}
	h := getHostHooks()
	if h.RegisterFileToken == nil {
		return "", Error(CodeUnimplemented, "host hook RegisterFileToken not configured")
	}
	return h.RegisterFileToken(path, timeoutSec)
}

// CronCreate 创建定时任务，返回宿主 Job 快照。
func (s *HostServiceServer) CronCreate(_ context.Context, spec *CronCreateSpec) (map[string]any, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.CronCreate == nil {
		return nil, Error(CodeUnimplemented, "host hook CronCreate not configured")
	}
	cp := CronCreateSpec{}
	if spec != nil {
		cp = *spec
	}
	// 调用方身份注入：宿主在 payload 打 _plugin_id 路由键，cron 到点
	// 触发时按其定位插件实例回推 FeedCronJob（对齐 RegisterSessionWait
	// 的 s.identity() 注入模式）。
	cp.PluginName = s.identity()
	return h.CronCreate(&cp)
}

// CronUpdate 按 job_id 更新任务字段（fields 部分更新语义）。
func (s *HostServiceServer) CronUpdate(_ context.Context, jobID string, fields map[string]any) (map[string]any, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.CronUpdate == nil {
		return nil, Error(CodeUnimplemented, "host hook CronUpdate not configured")
	}
	return h.CronUpdate(jobID, fields)
}

// CronDelete 删除指定定时任务。
func (s *HostServiceServer) CronDelete(_ context.Context, jobID string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.CronDelete == nil {
		return Error(CodeUnimplemented, "host hook CronDelete not configured")
	}
	return h.CronDelete(jobID)
}

// CronList 列出定时任务（job_type 空 = 全部类型）。
func (s *HostServiceServer) CronList(_ context.Context, jobType string) []map[string]any {
	if err := s.requireIdentity(); err != nil {
		return nil
	}
	h := getHostHooks()
	if h.CronList == nil {
		return nil
	}
	return h.CronList(jobType)
}

// CronRunNow 立即触发一次指定任务。
func (s *HostServiceServer) CronRunNow(_ context.Context, jobID string) error {
	if err := s.requireIdentity(); err != nil {
		return err
	}
	h := getHostHooks()
	if h.CronRunNow == nil {
		return Error(CodeUnimplemented, "host hook CronRunNow not configured")
	}
	return h.CronRunNow(jobID)
}

// McpListTools 汇总宿主已连接 MCP server 的全部工具
// （每项 {server, name, description, schema_json}）。
func (s *HostServiceServer) McpListTools(_ context.Context) []map[string]any {
	if err := s.requireIdentity(); err != nil {
		return nil
	}
	h := getHostHooks()
	if h.McpListTools == nil {
		return nil
	}
	return h.McpListTools()
}

// McpCallTool 调用宿主侧 MCP 工具，返回完整结果 / 纯文本摘要 / 是否出错。
func (s *HostServiceServer) McpCallTool(_ context.Context, server, toolName string, args map[string]any) (result map[string]any, text string, isError bool, err error) {
	if err := s.requireIdentity(); err != nil {
		return nil, "", false, err
	}
	h := getHostHooks()
	if h.McpCallTool == nil {
		return nil, "", false, Error(CodeUnimplemented, "host hook McpCallTool not configured")
	}
	return h.McpCallTool(server, toolName, args)
}

// maxChatLLMPerMinute 每插件每分钟 ChatLLM/CallAction 反向调用上限的默认值。
// 宿主可在启动前经 SetChatLLMRateLimit 覆盖（26-6）。
const maxChatLLMPerMinute = 30

// hostAdminList 是获准执行插件管理操作（SetPluginEnabled 操作他插件、
// InstallPlugin/UninstallPlugin）的 *manifest id* 集合，由宿主在启动前经
// SetPluginAdminList 配置。空集合表示无管理员插件（默认仅允许插件操作自身）。
//
// 清单语义 = manifest id 集合（不是注册名）：比对对象是连接绑定的
// HostServiceServer.connectionID()。注册名由插件自报、可被重名冒用，绝不能
// 作为鉴权键（p11）。
var (
	hostAdminListMu sync.RWMutex
	hostAdminList   = map[string]struct{}{}
)

// SetPluginAdminList 设置可执行插件管理操作的插件 manifest id 集合（宿主在
// 启动插件前调用；不调用则默认无管理员插件）。传 nil/空切片清空名单。
// 宿主须传入 manifest id（见 internal/plugin.pluginAdminListFromConfig），
// 而非注册名。
func SetPluginAdminList(ids []string) {
	hostAdminListMu.Lock()
	hostAdminList = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			hostAdminList[id] = struct{}{}
		}
	}
	hostAdminListMu.Unlock()
}

// hostAdminAuthorized 报告给定 manifest id 是否在管理员名单中。按 id 精确匹配
// （大小写敏感、不 trim、不做子串），避免模糊匹配放大授权面。
func hostAdminAuthorized(pluginID string) bool {
	hostAdminListMu.RLock()
	defer hostAdminListMu.RUnlock()
	_, ok := hostAdminList[pluginID]
	return ok
}
