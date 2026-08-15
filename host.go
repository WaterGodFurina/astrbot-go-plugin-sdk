package sdk

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
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
	hostMu.Lock()
	defer hostMu.Unlock()
	// Only cache SUCCESS: a transient dial failure (e.g. host broker not ready
	// yet) must not poison the plugin for its whole lifetime, otherwise every
	// reverse call (GetConfig/ChatLLM/...) fails forever and plugins fall back
	// to default config (the 401 symptom).
	if hostDialDone && hostSvc != nil {
		return hostSvc, nil
	}
	brokerMu.RLock()
	b := broker
	brokerMu.RUnlock()
	if b == nil {
		return nil, errNoBroker
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
	resp, err := svc.CallAction(context.Background(), &sdkv1.CallActionRequest{
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
	_, err = svc.SendMessage(context.Background(), &sdkv1.SendMessageRequest{
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
	_, err = svc.RecallMessage(context.Background(), &sdkv1.RecallMessageRequest{
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
	resp, err := svc.GetConfig(context.Background(), &sdkv1.GetConfigRequest{
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
	_, err = svc.SetConfig(context.Background(), &sdkv1.SetConfigRequest{
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
	resp, err := svc.ChatLLM(context.Background(), &sdkv1.ChatLLMRequest{
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
	_, err = svc.React(context.Background(), &sdkv1.ReactRequest{
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
	resp, err := svc.TextToImage(context.Background(), &sdkv1.TextToImageRequest{
		Text:         text,
		TemplateName: templateName,
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

// acceptHostService serves HostService on a broker listener so plugins can
// dial back into the host. It returns the gRPC server and listener so the
// caller can stop them when the plugin client is closed (prevents reload
// leaks of the serving goroutine and listener socket).
func acceptHostService(b *plugin.GRPCBroker, id uint32) (*grpc.Server, net.Listener, error) {
	lis, err := b.Accept(id)
	if err != nil {
		return nil, nil, err
	}
	// 提高默认 4MB 消息上限：插件经 HostService 反向传大 chain_json
	//（base64 图片/长对话）时不会因超限而失败。
	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxGRPCMessageSize),
		grpc.MaxSendMsgSize(maxGRPCMessageSize),
	)
	server := &hostServiceServer{pluginID: currentHostPluginID()}
	// 记录连接→插件 id，供 Register 后用注册名更新身份。
	if pid := server.pluginID; pid != "" {
		hostServersMu.Lock()
		hostServers[pid] = server
		hostServersMu.Unlock()
	}
	sdkv1.RegisterHostServiceServer(srv, server)
	go func() {
		_ = srv.Serve(lis)
	}()
	return srv, lis, nil
}

// hostServiceServer implements sdkv1.HostServiceServer on the host side,
// delegating to the hooks installed via SetHostHooks. Each plugin connection
// gets its own instance, bound to the plugin id the host was loading when the
// connection was accepted, so reverse calls can be validated per-plugin.
type hostServiceServer struct {
	sdkv1.UnimplementedHostServiceServer
	pluginID string
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
	h := getHostHooks()
	if h.CallAction == nil {
		return &sdkv1.CallActionResponse{}, nil
	}
	params := map[string]any{}
	if len(req.ParamsJson) > 0 {
		_ = json.Unmarshal(req.ParamsJson, &params)
	}
	result, err := h.CallAction(req.Platform, req.Api, params)
	if err != nil {
		return nil, err
	}
	out, _ := json.Marshal(result)
	return &sdkv1.CallActionResponse{ResultJson: out}, nil
}

func (s *hostServiceServer) SendMessage(_ context.Context, req *sdkv1.SendMessageRequest) (*sdkv1.Empty, error) {
	h := getHostHooks()
	if h.SendMessage == nil {
		return &sdkv1.Empty{}, nil
	}
	var chain []Component
	if len(req.ChainJson) > 0 {
		_ = json.Unmarshal(req.ChainJson, &chain)
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
	out, _ := json.Marshal(cfg)
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
		_ = json.Unmarshal(req.ConfigJson, &cfg)
	}
	if err := h.SetConfig(req.PluginName, cfg); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

// chatLLMLimiter 对插件的 ChatLLM 反向调用做限流（每插件每分钟上限），
// 防止恶意/失控插件无限调用宿主 LLM 消耗额度。
func (s *hostServiceServer) chatLLMLimiter() bool {
	if s.pluginID == "" {
		return true // 未知身份（旧宿主未设置）不限流，保证兼容
	}
	chatLLMRateMu.Lock()
	defer chatLLMRateMu.Unlock()
	now := time.Now()
	e := chatLLMRate[s.pluginID]
	if e.window.IsZero() || now.Sub(e.window) >= time.Minute {
		e.window = now
		e.count = 0
	}
	e.count++
	chatLLMRate[s.pluginID] = e
	return e.count <= maxChatLLMPerMinute
}

func (s *hostServiceServer) ChatLLM(_ context.Context, req *sdkv1.ChatLLMRequest) (*sdkv1.ChatLLMResponse, error) {
	if !s.chatLLMLimiter() {
		return nil, status.Errorf(codes.ResourceExhausted, "插件 %q ChatLLM 调用过于频繁（每分钟上限 %d 次）", s.pluginID, maxChatLLMPerMinute)
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

// maxChatLLMPerMinute 每插件每分钟 ChatLLM 反向调用上限。
const maxChatLLMPerMinute = 30

var (
	chatLLMRateMu sync.Mutex
	chatLLMRate   = map[string]struct {
		window time.Time
		count  int
	}{}
)
