package sdk

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"time"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// HostServiceAppID is the go-plugin broker AppID of the host's HostService.
// The host serves it (Accept) and plugins dial it (Dial). A high fixed value
// never collides with go-plugin's auto-incrementing NextId() stream.
const HostServiceAppID uint32 = 9000

// ---------------------------------------------------------------------------
// Plugin side: dialing the host's HostService over the broker.
// ---------------------------------------------------------------------------

var (
	brokerMu sync.RWMutex
	broker   *plugin.GRPCBroker

	hostMu       sync.Mutex
	hostSvc      sdkv1.HostServiceClient
	hostConn     *grpc.ClientConn
	hostDialDone bool
)

// setBroker stores the go-plugin broker handed to GRPCServer so handlers can
// dial the host lazily.
func setBroker(b *plugin.GRPCBroker) {
	brokerMu.Lock()
	defer brokerMu.Unlock()
	broker = b
	// Broker may be re-provisioned (plugin serving restarts); reset cached conn.
	hostMu.Lock()
	// hostConn 是 *grpc.ClientConn，自带 Close()。重建 broker 时先关闭旧
	// 连接再丢弃引用，避免底层 gRPC 连接与 goroutine 泄漏。
	if hostConn != nil {
		_ = hostConn.Close()
	}
	hostConn = nil
	hostSvc = nil
	hostDialDone = false
	hostMu.Unlock()
}

func hostServiceClient() (sdkv1.HostServiceClient, error) {
	// 先在 brokerMu 内取出 broker 引用并立即释放读取锁，再进入 hostMu。
	// 避免与 setBroker（brokerMu → hostMu 嵌套）构成 AB-BA 死锁：本函数
	// 在取得 hostMu 前已释放 brokerMu，任何时刻都不嵌套持有两把锁。
	brokerMu.RLock()
	b := broker
	brokerMu.RUnlock()
	if b == nil {
		return nil, errNoBroker
	}

	hostMu.Lock()
	defer hostMu.Unlock()
	// Only cache SUCCESS: a transient dial failure (e.g. host broker not ready
	// yet) must not poison the plugin for its whole lifetime, otherwise every
	// reverse call (GetConfig/ChatLLM/...) fails forever and plugins fall back
	// to default config (the 401 symptom).
	if hostDialDone && hostSvc != nil {
		return hostSvc, nil
	}
	conn, err := b.DialWithOptions(HostServiceAppID,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxGRPCMessageSize),
			grpc.MaxCallSendMsgSize(maxGRPCMessageSize),
		))
	if err != nil {
		return nil, err
	}
	hostConn = conn
	hostSvc = sdkv1.NewHostServiceClient(conn)
	hostDialDone = true
	return hostSvc, nil
}

var errNoBroker = &hostUnavailableError{"host service unavailable: plugin not being served"}

type hostUnavailableError struct{ msg string }

func (e *hostUnavailableError) Error() string { return e.msg }

// hostRPCTimeout 是插件→宿主反向调用的默认超时。宿主 hook 是第三方代码，
// 卡死时插件 handler 不能无限阻塞（26-7），与 python-sdk 的 30-180s
// timeout 对齐。
const hostRPCTimeout = 30 * time.Second

// hostRPCCtx 返回带默认超时的上下文，供 Host API 内部 RPC 使用。调用方必须
// defer cancel() 释放定时器。
func hostRPCCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), hostRPCTimeout)
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
	svc, err := hostServiceClient()
	if err != nil {
		return nil, err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	resp, err := svc.CallAction(ctx, &sdkv1.CallActionRequest{
		Platform:   platform,
		Api:        api,
		ParamsJson: paramsJSON,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(resp.ResultJson) > 0 {
		if err := json.Unmarshal(resp.ResultJson, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SendMessage sends a message chain to a session on a platform adapter.
// sessionID is the conversation id (group id or friend user id).
func (h *host) SendMessage(platform, sessionID string, chain []Component) error {
	svc, err := hostServiceClient()
	if err != nil {
		return err
	}
	chainJSON, err := json.Marshal(chain)
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	_, err = svc.SendMessage(ctx, &sdkv1.SendMessageRequest{
		Platform:  platform,
		SessionId: sessionID,
		ChainJson: chainJSON,
	})
	return err
}

// RecallMessage recalls an already-sent message by platform message id.
func (h *host) RecallMessage(platform, messageID string) error {
	svc, err := hostServiceClient()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	_, err = svc.RecallMessage(ctx, &sdkv1.RecallMessageRequest{
		Platform:  platform,
		MessageId: messageID,
	})
	return err
}

// GetConfig returns the plugin's persisted config map
// (data/plugins/<name>/config.json on the host).
func (h *host) GetConfig(pluginName string) (map[string]any, error) {
	svc, err := hostServiceClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	resp, err := svc.GetConfig(ctx, &sdkv1.GetConfigRequest{
		PluginName: pluginName,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(resp.ConfigJson) > 0 {
		if err := json.Unmarshal(resp.ConfigJson, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SetConfig persists the plugin's full config map
// (data/plugins/<name>/config.json on the host). Read-modify-write with
// GetConfig to preserve unrelated keys.
func (h *host) SetConfig(pluginName string, cfg map[string]any) error {
	svc, err := hostServiceClient()
	if err != nil {
		return err
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	_, err = svc.SetConfig(ctx, &sdkv1.SetConfigRequest{
		PluginName: pluginName,
		ConfigJson: cfgJSON,
	})
	return err
}

// ChatLLM calls the host's default chat LLM provider with the given prompt and
// returns the model's reply text. It does not execute tool calls.
func (h *host) ChatLLM(prompt, systemPrompt string, imageURLs []string) (string, error) {
	svc, err := hostServiceClient()
	if err != nil {
		return "", err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	resp, err := svc.ChatLLM(ctx, &sdkv1.ChatLLMRequest{
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		ImageUrls:    imageURLs,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// React adds an emoji reaction to a message on a platform adapter.
func (h *host) React(platform, sessionID, messageID, emoji string) error {
	svc, err := hostServiceClient()
	if err != nil {
		return err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	_, err = svc.React(ctx, &sdkv1.ReactRequest{
		Platform:  platform,
		SessionId: sessionID,
		MessageId: messageID,
		Emoji:     emoji,
	})
	return err
}

// TextToImage renders text into an image via the host t2i engine, returning
// base64-encoded PNG bytes.
func (h *host) TextToImage(text, templateName string) (string, error) {
	svc, err := hostServiceClient()
	if err != nil {
		return "", err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	resp, err := svc.TextToImage(ctx, &sdkv1.TextToImageRequest{
		Text:         text,
		TemplateName: templateName,
	})
	if err != nil {
		return "", err
	}
	return resp.ImageBase64, nil
}

// HtmlRender renders an HTML template + data into an image via the host
// (t2i remote preferred, local gg fallback), returning base64-encoded PNG bytes.
func (h *host) HtmlRender(template, data, options string) (string, error) {
	svc, err := hostServiceClient()
	if err != nil {
		return "", err
	}
	ctx, cancel := hostRPCCtx()
	defer cancel()
	resp, err := svc.HtmlRender(ctx, &sdkv1.HtmlRenderRequest{
		Template: template,
		Data:     data,
		Options:  options,
	})
	if err != nil {
		return "", err
	}
	return resp.ImageBase64, nil
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
	// ChatLLM calls the default chat provider with prompt + system prompt.
	// imageURLs (may be nil) are appended as multimodal content parts.
	ChatLLM func(prompt, systemPrompt string, imageURLs []string) (string, error)
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
	// ListStars 返回全部已安装插件元数据。
	ListStars func() []map[string]any
	// GetStar 按插件名取元数据。
	GetStar func(name string) map[string]any
	// SetPluginEnabled 启用/禁用插件。
	SetPluginEnabled func(pluginName string, enabled bool) error
	// InstallPlugin 安装插件（git/url 源）。
	InstallPlugin func(repo string) error
	// UninstallPlugin 卸载插件。
	UninstallPlugin func(pluginName string) error

	// ── 会话等待（SessionWaiter）──
	// RegisterSessionWait 注册插件对 umo 的等待，返回 wait_id（空 = 不支持）。
	// pluginName 由 SDK 侧从连接身份（s.pluginID）自动注入，宿主据此记录等待
	// 归属插件（proto 未携带插件名字段，避免改协议）。
	RegisterSessionWait func(pluginName, umo string, timeoutSeconds int32) string
	// UnregisterSessionWait 注销等待。
	UnregisterSessionWait func(waitID string)
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

// hostServiceLogger 是宿主侧 HostService 的告警日志通道。SDK 没有宿主注入
// 的 logger，默认输出到 stderr；宿主可在启动插件前通过 SetHostServiceLogger
// 替换为自带 logger。
var hostServiceLogger = hclog.New(&hclog.LoggerOptions{
	Name:   "astrbot-sdk.hostservice",
	Level:  hclog.Warn,
	Output: os.Stderr,
})

// SetHostServiceLogger 替换宿主侧 HostService 的告警 logger（nil 忽略）。
func SetHostServiceLogger(l hclog.Logger) {
	if l == nil {
		return
	}
	hostServiceLogger = l
}

// warnJSON 记录 HostService 处理中 JSON 编解码失败，避免被 `_ =` 静默吞掉
// （26-4）。调用方行为不变：尽力降级为空值并继续。
func warnJSON(what string, err error) {
	if err != nil {
		hostServiceLogger.Warn("host service JSON 处理失败", "what", what, "err", err)
	}
}

// requireIdentity 是控制面 HostService RPC 的最小鉴权：插件管理、会话/
// Provider 控制、会话等待注册等会改动宿主生态或状态的操作必须绑定身份
// （s.pluginID 非空）。宿主未设置身份时（SetCurrentHostPluginID /
// BindHostServiceName 未生效）pluginID 为空，一律拒绝，防止匿名/未绑定身份
// 插件接管宿主插件生态（26-2）。GetConfig/SetConfig 走更细粒度的名字隔离
// （见其各自实现），不在此列。
func (s *hostServiceServer) requireIdentity() error {
	if s.pluginID == "" {
		return status.Error(codes.PermissionDenied, "control-plane HostService RPC requires a bound plugin identity")
	}
	return nil
}

// dropPluginHostState 在插件连接关闭时清理该连接遗留的宿主侧状态：
// hostServers 连接登记与 ChatLLM/CallAction 限流窗口，避免表只增不减
// （26-3）。connKey 是 accept 时刻的 manifest id（hostServers 的 key）；
// 若期间经 BindHostServiceName 更新了注册名，限流表还可能有注册名条目，
// 一并清理。
func dropPluginHostState(connKey string) {
	if connKey == "" {
		return
	}
	registered := ""
	hostServersMu.Lock()
	if s, ok := hostServers[connKey]; ok {
		registered = s.pluginID
		delete(hostServers, connKey)
	}
	hostServersMu.Unlock()
	chatLLMRate.drop(connKey)
	callActionRate.drop(connKey)
	if registered != "" && registered != connKey {
		chatLLMRate.drop(registered)
		callActionRate.drop(registered)
	}
}

// acceptHostService serves HostService on a broker listener so plugins can
// dial back into the host. It returns the gRPC server and listener so the
// caller can stop them when the plugin client is closed (prevents reload
// leaks of the serving goroutine and listener socket).
func acceptHostService(b *plugin.GRPCBroker, id uint32) (*grpc.Server, net.Listener, *hostServiceServer, error) {
	lis, err := b.Accept(id)
	if err != nil {
		return nil, nil, nil, err
	}
	// 提高默认 4MB 消息上限：插件经 HostService 反向传大 chain_json
	//（base64 图片/长对话）时不会因超限而失败。
	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxGRPCMessageSize),
		grpc.MaxSendMsgSize(maxGRPCMessageSize),
	)
	// connKey 保留 accept 时刻的 manifest id（hostServers 表的 key），
	// 供连接关闭时清理 hostServers/限流表残留（26-3）。
	pid := currentHostPluginID()
	server := &hostServiceServer{pluginID: pid, connKey: pid}
	// 记录连接→插件 id，供 Register 后用注册名更新身份。
	if pid != "" {
		hostServersMu.Lock()
		hostServers[pid] = server
		hostServersMu.Unlock()
	}
	sdkv1.RegisterHostServiceServer(srv, server)
	go func() {
		_ = srv.Serve(lis)
	}()
	return srv, lis, server, nil
}

// hostServiceServer implements sdkv1.HostServiceServer on the host side,
// delegating to the hooks installed via SetHostHooks. Each plugin connection
// gets its own instance, bound to the plugin id the host was loading when the
// connection was accepted, so reverse calls can be validated per-plugin.
type hostServiceServer struct {
	sdkv1.UnimplementedHostServiceServer
	// pluginID 是当前连接身份：accept 时为 manifest id，Register 后由
	// BindHostServiceName 更新为注册名（GetConfig/SetConfig 传的是注册名）。
	pluginID string
	// connKey 是 accept 时刻的 manifest id，hostServers 表以此作为 key；
	// 连接关闭时用于清理 hostServers 与限流表条目（26-3）。
	connKey string
}

// hostPluginID is the id of the plugin the host is currently establishing a
// connection for. The host sets it (SetCurrentHostPluginID) right before
// go-plugin Dispense; acceptHostService reads it so the per-connection
// hostServiceServer knows which plugin it serves.
var (
	hostPluginIDMu sync.Mutex
	hostPluginID   string

	// hostServers 记录 accept 时创建的 per-connection hostServiceServer
	//（key=插件 manifest id），供宿主在 Register 后用注册名更新身份。
	hostServersMu sync.Mutex
	hostServers   = map[string]*hostServiceServer{}
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

// BindHostServiceName updates the per-connection HostService server's plugin
// identity to the plugin's registered name (Register 返回值）。插件
// GetConfig/SetConfig 传的是注册名，而 accept 时只绑定 manifest id，二者
// 可能不同（如 jm_cosmos vs astrbot_plugin_jm_cosmos），故宿主在 Register
// 成功后调用本函数对齐身份，保证身份隔离校验通过。
func BindHostServiceName(id, name string) {
	hostServersMu.Lock()
	defer hostServersMu.Unlock()
	if s, ok := hostServers[id]; ok {
		s.pluginID = name
	}
}

func (s *hostServiceServer) CallAction(_ context.Context, req *sdkv1.CallActionRequest) (*sdkv1.CallActionResponse, error) {
	// CallAction 是高频平台 API 入口，做与 ChatLLM 同款的窗口限流（26-6）。
	if !callActionRate.allow(s.pluginID) {
		return nil, status.Errorf(codes.ResourceExhausted, "插件 %q CallAction 调用过于频繁（每分钟上限 %d 次）", s.pluginID, callActionRate.maxPer)
	}
	h := getHostHooks()
	if h.CallAction == nil {
		return &sdkv1.CallActionResponse{}, nil
	}
	params := map[string]any{}
	if len(req.ParamsJson) > 0 {
		warnJSON("CallAction params_json", json.Unmarshal(req.ParamsJson, &params))
	}
	result, err := h.CallAction(req.Platform, req.Api, params)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &sdkv1.CallActionResponse{ResultJson: out}, nil
}

func (s *hostServiceServer) SendMessage(_ context.Context, req *sdkv1.SendMessageRequest) (*sdkv1.Empty, error) {
	h := getHostHooks()
	if h.SendMessage == nil {
		return &sdkv1.Empty{}, nil
	}
	var chain []Component
	if len(req.ChainJson) > 0 {
		warnJSON("SendMessage chain_json", json.Unmarshal(req.ChainJson, &chain))
	}
	if err := h.SendMessage(req.Platform, req.SessionId, chain); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *hostServiceServer) RecallMessage(_ context.Context, req *sdkv1.RecallMessageRequest) (*sdkv1.Empty, error) {
	h := getHostHooks()
	if h.RecallMessage == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.RecallMessage(req.Platform, req.MessageId); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *hostServiceServer) GetConfig(_ context.Context, req *sdkv1.GetConfigRequest) (*sdkv1.GetConfigResponse, error) {
	// 身份隔离：插件只能读取自己名字的配置，禁止探测/读取其他插件配置
	//（插件自身以宿主用户运行、可直接读文件系统，此校验是纵深防御，
	//  真正隔离需插件降权/容器化）。
	if s.pluginID != "" && req.PluginName != s.pluginID {
		return nil, status.Errorf(codes.PermissionDenied, "插件 %q 无权读取插件 %q 的配置", s.pluginID, req.PluginName)
	}
	h := getHostHooks()
	if h.GetConfig == nil {
		return &sdkv1.GetConfigResponse{}, nil
	}
	cfg, err := h.GetConfig(req.PluginName)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return &sdkv1.GetConfigResponse{ConfigJson: out}, nil
}

func (s *hostServiceServer) SetConfig(_ context.Context, req *sdkv1.SetConfigRequest) (*sdkv1.Empty, error) {
	// 身份隔离：插件只能写自己名字的配置，禁止篡改其他插件配置。
	if s.pluginID != "" && req.PluginName != s.pluginID {
		return nil, status.Errorf(codes.PermissionDenied, "插件 %q 无权修改插件 %q 的配置", s.pluginID, req.PluginName)
	}
	h := getHostHooks()
	if h.SetConfig == nil {
		return &sdkv1.Empty{}, nil
	}
	cfg := map[string]any{}
	if len(req.ConfigJson) > 0 {
		warnJSON("SetConfig config_json", json.Unmarshal(req.ConfigJson, &cfg))
	}
	if err := h.SetConfig(req.PluginName, cfg); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

// rateWindow 记录某插件在一个固定窗口内的调用计数。
type rateWindow struct {
	window time.Time
	count  int
}

// rateTable 是"插件→窗口计数"的限流表，用 pluginID 作为 key。只增不减会
// 在插件频繁装卸/长期运行时持续累积（26-3），故提供 drop 供连接关闭时清理。
// pluginID 空时（旧宿主未设置身份）不进行限流，保证兼容。
type rateTable struct {
	mu     sync.Mutex
	maxPer int
	table  map[string]rateWindow
}

// allow 返回本次调用是否放行，并推进窗口计数。
func (r *rateTable) allow(id string) bool {
	if id == "" || r == nil {
		return true // 未知身份不限流，保证兼容
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	e := r.table[id]
	if e.window.IsZero() || now.Sub(e.window) >= time.Minute {
		e.window = now
		e.count = 0
	}
	e.count++
	r.table[id] = e
	return e.count <= r.maxPer
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

// SetChatLLMRateLimit 调整每插件每分钟 ChatLLM 反向调用上限（<=0 表示关闭
// 限流），供宿主在启动前配置（26-6，原 maxChatLLMPerMinute 硬编码）。
// 未调用时默认 30。返回设置前的旧值。
func SetChatLLMRateLimit(perMinute int) int {
	old := chatLLMRate.maxPer
	if perMinute > 0 {
		chatLLMRate.maxPer = perMinute
	}
	return old
}

func (s *hostServiceServer) ChatLLM(_ context.Context, req *sdkv1.ChatLLMRequest) (*sdkv1.ChatLLMResponse, error) {
	if !chatLLMRate.allow(s.pluginID) {
		return nil, status.Errorf(codes.ResourceExhausted, "插件 %q ChatLLM 调用过于频繁（每分钟上限 %d 次）", s.pluginID, chatLLMRate.maxPer)
	}
	h := getHostHooks()
	if h.ChatLLM == nil {
		return &sdkv1.ChatLLMResponse{}, nil
	}
	text, err := h.ChatLLM(req.Prompt, req.SystemPrompt, req.ImageUrls)
	if err != nil {
		return nil, err
	}
	return &sdkv1.ChatLLMResponse{Text: text}, nil
}

// React adds an emoji reaction to a message on a platform adapter.
func (s *hostServiceServer) React(_ context.Context, req *sdkv1.ReactRequest) (*sdkv1.Empty, error) {
	h := getHostHooks()
	if h.React == nil {
		return &sdkv1.Empty{}, nil
	}
	return &sdkv1.Empty{}, h.React(req.Platform, req.SessionId, req.MessageId, req.Emoji)
}

// TextToImage renders text into an image via the host t2i engine.
func (s *hostServiceServer) TextToImage(_ context.Context, req *sdkv1.TextToImageRequest) (*sdkv1.TextToImageResponse, error) {
	h := getHostHooks()
	if h.TextToImage == nil {
		return &sdkv1.TextToImageResponse{}, nil
	}
	b64, err := h.TextToImage(req.Text, req.TemplateName)
	if err != nil {
		return nil, err
	}
	return &sdkv1.TextToImageResponse{ImageBase64: b64}, nil
}

// HtmlRender renders an HTML template + data into an image via the host.
func (s *hostServiceServer) HtmlRender(_ context.Context, req *sdkv1.HtmlRenderRequest) (*sdkv1.HtmlRenderResponse, error) {
	h := getHostHooks()
	if h.HtmlRender == nil {
		return &sdkv1.HtmlRenderResponse{}, nil
	}
	b64, err := h.HtmlRender(req.Template, req.Data, req.Options)
	if err != nil {
		return nil, err
	}
	return &sdkv1.HtmlRenderResponse{ImageBase64: b64}, nil
}

// ── 会话管理 RPC 实现 ──────────────────────────────────────────────────────

func (s *hostServiceServer) GetCurrConversationID(_ context.Context, req *sdkv1.ConversationIDRequest) (*sdkv1.ConversationIDResponse, error) {
	h := getHostHooks()
	if h.GetCurrConversationID == nil {
		return &sdkv1.ConversationIDResponse{}, nil
	}
	return &sdkv1.ConversationIDResponse{Cid: h.GetCurrConversationID(req.UnifiedMsgOrigin)}, nil
}

func (s *hostServiceServer) NewConversation(_ context.Context, req *sdkv1.NewConversationRequest) (*sdkv1.ConversationIDResponse, error) {
	h := getHostHooks()
	if h.NewConversation == nil {
		return &sdkv1.ConversationIDResponse{}, nil
	}
	return &sdkv1.ConversationIDResponse{Cid: h.NewConversation(req.UnifiedMsgOrigin, req.PlatformId, req.PersonaId)}, nil
}

func (s *hostServiceServer) GetConversation(_ context.Context, req *sdkv1.GetConversationRequest) (*sdkv1.ConversationResponse, error) {
	h := getHostHooks()
	if h.GetConversation == nil {
		return &sdkv1.ConversationResponse{}, nil
	}
	out, err := json.Marshal(h.GetConversation(req.UnifiedMsgOrigin, req.ConversationId, req.CreateIfNotExists))
	if err != nil {
		return nil, err
	}
	return &sdkv1.ConversationResponse{ConversationJson: out}, nil
}

func (s *hostServiceServer) GetConversations(_ context.Context, req *sdkv1.GetConversationsRequest) (*sdkv1.ConversationsResponse, error) {
	h := getHostHooks()
	if h.GetConversations == nil {
		return &sdkv1.ConversationsResponse{}, nil
	}
	resp := &sdkv1.ConversationsResponse{}
	for _, c := range h.GetConversations(req.UnifiedMsgOrigin) {
		out, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		resp.ConversationsJson = append(resp.ConversationsJson, out)
	}
	return resp, nil
}

func (s *hostServiceServer) DeleteConversation(_ context.Context, req *sdkv1.DeleteConversationRequest) (*sdkv1.Empty, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.DeleteConversation == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.DeleteConversation(req.UnifiedMsgOrigin, req.ConversationId); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *hostServiceServer) SwitchConversation(_ context.Context, req *sdkv1.SwitchConversationRequest) (*sdkv1.Empty, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.SwitchConversation == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.SwitchConversation(req.UnifiedMsgOrigin, req.ConversationId); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *hostServiceServer) UpdateConversationTitle(_ context.Context, req *sdkv1.UpdateConversationTitleRequest) (*sdkv1.Empty, error) {
	h := getHostHooks()
	if h.UpdateConversationTitle == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.UpdateConversationTitle(req.UnifiedMsgOrigin, req.ConversationId, req.Title); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *hostServiceServer) UpdateConversationPersonaID(_ context.Context, req *sdkv1.UpdateConversationPersonaRequest) (*sdkv1.Empty, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.UpdateConversationPersonaID == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.UpdateConversationPersonaID(req.UnifiedMsgOrigin, req.ConversationId, req.PersonaId); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

// ── 人格管理 RPC 实现 ──────────────────────────────────────────────────────

func (s *hostServiceServer) GetPersonas(_ context.Context, _ *sdkv1.Empty) (*sdkv1.PersonasResponse, error) {
	h := getHostHooks()
	if h.GetPersonas == nil {
		return &sdkv1.PersonasResponse{}, nil
	}
	resp := &sdkv1.PersonasResponse{}
	for _, p := range h.GetPersonas() {
		out, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		resp.PersonasJson = append(resp.PersonasJson, out)
	}
	return resp, nil
}

func (s *hostServiceServer) GetDefaultPersona(_ context.Context, req *sdkv1.GetDefaultPersonaRequest) (*sdkv1.PersonaResponse, error) {
	h := getHostHooks()
	if h.GetDefaultPersona == nil {
		return &sdkv1.PersonaResponse{}, nil
	}
	out, err := json.Marshal(h.GetDefaultPersona(req.Umo))
	if err != nil {
		return nil, err
	}
	return &sdkv1.PersonaResponse{PersonaJson: out}, nil
}

func (s *hostServiceServer) GetPersonaTree(_ context.Context, _ *sdkv1.Empty) (*sdkv1.PersonaTreeResponse, error) {
	h := getHostHooks()
	if h.GetPersonaTree == nil {
		return &sdkv1.PersonaTreeResponse{}, nil
	}
	folders, personas := h.GetPersonaTree()
	resp := &sdkv1.PersonaTreeResponse{}
	for _, f := range folders {
		out, err := json.Marshal(f)
		if err != nil {
			return nil, err
		}
		resp.FoldersJson = append(resp.FoldersJson, out)
	}
	for _, p := range personas {
		out, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		resp.PersonasJson = append(resp.PersonasJson, out)
	}
	return resp, nil
}

func (s *hostServiceServer) ResolveSelectedPersona(_ context.Context, req *sdkv1.ResolvePersonaRequest) (*sdkv1.ResolvePersonaResponse, error) {
	h := getHostHooks()
	if h.ResolveSelectedPersona == nil {
		return &sdkv1.ResolvePersonaResponse{}, nil
	}
	settings := map[string]any{}
	if len(req.ProviderSettingsJson) > 0 {
		warnJSON("ResolveSelectedPersona provider_settings_json", json.Unmarshal(req.ProviderSettingsJson, &settings))
	}
	personaID, personaName, personaPrompt, forceApplied, isDefault := h.ResolveSelectedPersona(
		req.Umo, req.ConversationPersonaId, req.PlatformName, settings,
	)
	return &sdkv1.ResolvePersonaResponse{
		PersonaId:             personaID,
		PersonaName:           personaName,
		PersonaPrompt:         personaPrompt,
		ForceAppliedPersonaId: forceApplied,
		IsDefault:             isDefault,
	}, nil
}

// ── Provider 管理 RPC 实现 ─────────────────────────────────────────────────

func (s *hostServiceServer) ListProviders(_ context.Context, req *sdkv1.ListProvidersRequest) (*sdkv1.ProvidersResponse, error) {
	h := getHostHooks()
	if h.ListProviders == nil {
		return &sdkv1.ProvidersResponse{}, nil
	}
	resp := &sdkv1.ProvidersResponse{}
	for _, p := range h.ListProviders(req.Capability) {
		out, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		resp.ProvidersJson = append(resp.ProvidersJson, out)
	}
	return resp, nil
}

func (s *hostServiceServer) GetUsingProvider(_ context.Context, req *sdkv1.GetUsingProviderRequest) (*sdkv1.ProviderResponse, error) {
	h := getHostHooks()
	if h.GetUsingProvider == nil {
		return &sdkv1.ProviderResponse{}, nil
	}
	out, err := json.Marshal(h.GetUsingProvider(req.Umo, req.Capability))
	if err != nil {
		return nil, err
	}
	return &sdkv1.ProviderResponse{ProviderJson: out}, nil
}

func (s *hostServiceServer) SetProvider(_ context.Context, req *sdkv1.SetProviderRequest) (*sdkv1.Empty, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.SetProvider == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.SetProvider(req.Umo, req.ProviderId, req.Capability); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *hostServiceServer) GetProviderModels(_ context.Context, req *sdkv1.GetProviderModelsRequest) (*sdkv1.ProviderModelsResponse, error) {
	h := getHostHooks()
	if h.GetProviderModels == nil {
		return &sdkv1.ProviderModelsResponse{}, nil
	}
	return &sdkv1.ProviderModelsResponse{Models: h.GetProviderModels(req.ProviderId)}, nil
}

// ── 插件/Star 管理 RPC 实现 ────────────────────────────────────────────────

func (s *hostServiceServer) ListStars(_ context.Context, _ *sdkv1.Empty) (*sdkv1.StarsResponse, error) {
	h := getHostHooks()
	if h.ListStars == nil {
		return &sdkv1.StarsResponse{}, nil
	}
	resp := &sdkv1.StarsResponse{}
	for _, st := range h.ListStars() {
		out, err := json.Marshal(st)
		if err != nil {
			return nil, err
		}
		resp.StarsJson = append(resp.StarsJson, out)
	}
	return resp, nil
}

func (s *hostServiceServer) GetStar(_ context.Context, req *sdkv1.GetStarRequest) (*sdkv1.StarResponse, error) {
	h := getHostHooks()
	if h.GetStar == nil {
		return &sdkv1.StarResponse{}, nil
	}
	out, err := json.Marshal(h.GetStar(req.Name))
	if err != nil {
		return nil, err
	}
	return &sdkv1.StarResponse{StarJson: out}, nil
}

func (s *hostServiceServer) SetPluginEnabled(_ context.Context, req *sdkv1.SetPluginEnabledRequest) (*sdkv1.Empty, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.SetPluginEnabled == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.SetPluginEnabled(req.PluginName, req.Enabled); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *hostServiceServer) InstallPlugin(_ context.Context, req *sdkv1.InstallPluginRequest) (*sdkv1.Empty, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.InstallPlugin == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.InstallPlugin(req.Repo); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *hostServiceServer) UninstallPlugin(_ context.Context, req *sdkv1.UninstallPluginRequest) (*sdkv1.Empty, error) {
	if err := s.requireIdentity(); err != nil {
		return nil, err
	}
	h := getHostHooks()
	if h.UninstallPlugin == nil {
		return &sdkv1.Empty{}, nil
	}
	if err := h.UninstallPlugin(req.PluginName); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

// RegisterSessionWait registers a session wait for this plugin (the host
// feeds matching inbound events back via PluginService.FeedSessionWait).
// pluginName 从连接身份注入（s.pluginID，Register 后为注册名），宿主凭此
// 关联等待与插件实例。
func (s *hostServiceServer) RegisterSessionWait(_ context.Context, req *sdkv1.RegisterSessionWaitRequest) (*sdkv1.RegisterSessionWaitResponse, error) {
	// 空身份时拒绝注册：宿主无法把等待归属到任何插件，避免记录无主等待
	//（26-5）；同时归入控制面最小鉴权（26-2）。
	if s.pluginID == "" {
		return nil, status.Error(codes.FailedPrecondition, "cannot register session wait without a bound plugin identity")
	}
	h := getHostHooks()
	if h.RegisterSessionWait == nil {
		return &sdkv1.RegisterSessionWaitResponse{}, nil
	}
	waitID := h.RegisterSessionWait(s.pluginID, req.Umo, req.TimeoutSeconds)
	return &sdkv1.RegisterSessionWaitResponse{WaitId: waitID}, nil
}

// UnregisterSessionWait removes a previously registered session wait.
func (s *hostServiceServer) UnregisterSessionWait(_ context.Context, req *sdkv1.UnregisterSessionWaitRequest) (*sdkv1.Empty, error) {
	h := getHostHooks()
	if h.UnregisterSessionWait == nil {
		return &sdkv1.Empty{}, nil
	}
	h.UnregisterSessionWait(req.WaitId)
	return &sdkv1.Empty{}, nil
}

// maxChatLLMPerMinute 每插件每分钟 ChatLLM/CallAction 反向调用上限的默认值。
// 宿主可在启动前经 SetChatLLMRateLimit 覆盖（26-6）。
const maxChatLLMPerMinute = 30
