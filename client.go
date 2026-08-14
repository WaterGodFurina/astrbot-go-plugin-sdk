package sdk

import (
	"context"
	"encoding/json"
	"net"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"google.golang.org/grpc"
)

// maxGRPCMessageSize raises the gRPC message cap above the 4MB default so
// large event_json/chain_json payloads (base64 images, long conversations)
// can cross the wire between host and plugin.
const maxGRPCMessageSize = 128 << 20 // 128MB

// rpcCallOpts is attached to every host→plugin RPC to lift both send and
// receive limits for that call.
var rpcCallOpts = []grpc.CallOption{
	grpc.MaxCallRecvMsgSize(maxGRPCMessageSize),
	grpc.MaxCallSendMsgSize(maxGRPCMessageSize),
}

// Client is the host-side, typed wrapper around the plugin's gRPC service.
// The host obtains it from go-plugin's Client() (see PluginServiceGRPCPlugin.GRPCClient).
type Client struct {
	conn *grpc.ClientConn
	svc  sdkv1.PluginServiceClient

	// hostSrv/hostLis are the HostService gRPC server the host serves on the
	// broker for this plugin. They are stopped by Close() so reloading a plugin
	// does not leak the listener or its serving goroutine.
	hostSrv *grpc.Server
	hostLis net.Listener
}

// NewClient wraps an existing gRPC connection.
func NewClient(conn *grpc.ClientConn) *Client {
	return &Client{
		conn: conn,
		svc:  sdkv1.NewPluginServiceClient(conn),
	}
}

// Register fetches the plugin's metadata and handler descriptors.
func (c *Client) Register(ctx context.Context) (*sdkv1.RegisterResponse, error) {
	return c.svc.Register(ctx, &sdkv1.RegisterRequest{}, rpcCallOpts...)
}

// HandleCommand invokes a command handler, returning its text reply plus an
// optional rich result chain (text + images + files).
func (c *Client) HandleCommand(ctx context.Context, name string, args []string, e *Event) (string, []Component, error) {
	ev, err := json.Marshal(e)
	if err != nil {
		return "", nil, err
	}
	resp, err := c.svc.HandleCommand(ctx, &sdkv1.HandleCommandRequest{
		Name:      name,
		Args:      args,
		EventJson: ev,
	}, rpcCallOpts...)
	if err != nil {
		return "", nil, err
	}
	var chain []Component
	if len(resp.ChainJson) > 0 {
		if err := json.Unmarshal(resp.ChainJson, &chain); err != nil {
			return "", nil, err
		}
	}
	return resp.Text, chain, nil
}

// HandleFilter invokes a filter handler, returning whether the event may continue.
func (c *Client) HandleFilter(ctx context.Context, name string, e *Event) (bool, error) {
	ev, err := json.Marshal(e)
	if err != nil {
		return true, err
	}
	resp, err := c.svc.HandleFilter(ctx, &sdkv1.HandleFilterRequest{Name: name, EventJson: ev}, rpcCallOpts...)
	if err != nil {
		return true, err
	}
	return resp.Allow, nil
}

// HandleHook invokes a hook handler. For result-decoration hooks, chain is the
// current result chain and the (possibly decorated) chain is returned.
func (c *Client) HandleHook(ctx context.Context, name string, e *Event, chain []Component) ([]Component, bool, error) {
	return c.handleHook(ctx, name, e, chain, nil)
}

// HandleHookWithPayload invokes a payload-carrying hook handler (on_llm_response,
// on_using_llm_tool, on_llm_tool_respond, on_plugin_error, lifecycle hooks).
// payload is JSON-marshaled into the RPC; pass nil for event-only hooks.
func (c *Client) HandleHookWithPayload(ctx context.Context, name string, e *Event, chain []Component, payload any) ([]Component, bool, error) {
	return c.handleHook(ctx, name, e, chain, payload)
}

func (c *Client) handleHook(ctx context.Context, name string, e *Event, chain []Component, payload any) ([]Component, bool, error) {
	ev, err := json.Marshal(e)
	if err != nil {
		return chain, false, err
	}
	var chainJSON []byte
	if len(chain) > 0 {
		if chainJSON, err = json.Marshal(chain); err != nil {
			return chain, false, err
		}
	}
	var payloadJSON []byte
	if payload != nil {
		if payloadJSON, err = json.Marshal(payload); err != nil {
			return chain, false, err
		}
	}
	resp, err := c.svc.HandleHook(ctx, &sdkv1.HandleHookRequest{Name: name, EventJson: ev, ChainJson: chainJSON, PayloadJson: payloadJSON}, rpcCallOpts...)
	if err != nil {
		return chain, false, err
	}
	if len(resp.ChainJson) > 0 {
		var out []Component
		if err := json.Unmarshal(resp.ChainJson, &out); err == nil {
			chain = out
		}
	}
	return chain, resp.Stop, nil
}

// HandleLLMRequest invokes an on_llm_request hook, returning the (possibly
// modified) system prompt and whether the LLM call should be stopped.
func (c *Client) HandleLLMRequest(ctx context.Context, name string, e *Event, systemPrompt, userPrompt string) (string, bool, error) {
	ev, err := json.Marshal(e)
	if err != nil {
		return systemPrompt, false, err
	}
	resp, err := c.svc.HandleLLMRequest(ctx, &sdkv1.HandleLLMRequestRequest{
		Name:         name,
		EventJson:    ev,
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
	}, rpcCallOpts...)
	if err != nil {
		return systemPrompt, false, err
	}
	return resp.SystemPrompt, resp.Stop, nil
}

// HandleTool invokes a registered LLM function tool.
func (c *Client) HandleTool(ctx context.Context, name string, args map[string]any, e *Event) (string, bool, error) {
	ev, err := json.Marshal(e)
	if err != nil {
		return "", false, err
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", false, err
	}
	resp, err := c.svc.HandleTool(ctx, &sdkv1.HandleToolRequest{
		Name:      name,
		ArgsJson:  argsJSON,
		EventJson: ev,
	}, rpcCallOpts...)
	if err != nil {
		return "", false, err
	}
	return resp.Text, resp.IsError, nil
}

// HealthCheck probes the plugin's liveness.
func (c *Client) HealthCheck(ctx context.Context) (*sdkv1.HealthResponse, error) {
	return c.svc.HealthCheck(ctx, &sdkv1.Empty{}, rpcCallOpts...)
}

// Cleanup tells the plugin to run its unload hook.
func (c *Client) Cleanup(ctx context.Context) error {
	_, err := c.svc.Cleanup(ctx, &sdkv1.Empty{}, rpcCallOpts...)
	return err
}

// Close releases the underlying gRPC connection and stops the HostService
// server this client served on the broker (if any). Call it after the plugin
// process has been killed so reloads do not leak connections/goroutines.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if c.hostSrv != nil {
		c.hostSrv.Stop()
		c.hostSrv = nil
	}
	if c.hostLis != nil {
		_ = c.hostLis.Close()
		c.hostLis = nil
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// setHostServiceServer records the HostService gRPC server + listener served
// for this client so Close() can release them.
func (c *Client) setHostServiceServer(srv *grpc.Server, lis net.Listener) {
	c.hostSrv = srv
	c.hostLis = lis
}
