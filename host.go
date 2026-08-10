package sdk

import (
	"context"
	"encoding/json"
	"net"
	"sync"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
		grpc.WithTransportCredentials(insecure.NewCredentials()))
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
		Platform:   platform,
		SessionId:  sessionID,
		ChainJson:  chainJSON,
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
		Platform:   platform,
		MessageId:  messageID,
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
func (h *host) ChatLLM(prompt, systemPrompt string) (string, error) {
	svc, err := hostServiceClient()
	if err != nil {
		return "", err
	}
	resp, err := svc.ChatLLM(context.Background(), &sdkv1.ChatLLMRequest{
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
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
	ChatLLM func(prompt, systemPrompt string) (string, error)
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
	srv := grpc.NewServer()
	sdkv1.RegisterHostServiceServer(srv, &hostServiceServer{})
	go func() {
		_ = srv.Serve(lis)
	}()
	return srv, lis, nil
}

// hostServiceServer implements sdkv1.HostServiceServer on the host side,
// delegating to the hooks installed via SetHostHooks.
type hostServiceServer struct {
	sdkv1.UnimplementedHostServiceServer
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

func (s *hostServiceServer) ChatLLM(_ context.Context, req *sdkv1.ChatLLMRequest) (*sdkv1.ChatLLMResponse, error) {
	h := getHostHooks()
	if h.ChatLLM == nil {
		return &sdkv1.ChatLLMResponse{}, nil
	}
	text, err := h.ChatLLM(req.Prompt, req.SystemPrompt)
	if err != nil {
		return nil, err
	}
	return &sdkv1.ChatLLMResponse{Text: text}, nil
}
