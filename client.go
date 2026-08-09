package sdk

import (
	"context"
	"encoding/json"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"google.golang.org/grpc"
)

// Client is the host-side, typed wrapper around the plugin's gRPC service.
// The host obtains it from go-plugin's Client() (see PluginServiceGRPCPlugin.GRPCClient).
type Client struct {
	conn *grpc.ClientConn
	svc  sdkv1.PluginServiceClient
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
	return c.svc.Register(ctx, &sdkv1.RegisterRequest{})
}

// HandleCommand invokes a command handler.
func (c *Client) HandleCommand(ctx context.Context, name string, args []string, e *Event) (string, error) {
	ev, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	resp, err := c.svc.HandleCommand(ctx, &sdkv1.HandleCommandRequest{
		Name:      name,
		Args:      args,
		EventJson: ev,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// HandleFilter invokes a filter handler, returning whether the event may continue.
func (c *Client) HandleFilter(ctx context.Context, name string, e *Event) (bool, error) {
	ev, err := json.Marshal(e)
	if err != nil {
		return true, err
	}
	resp, err := c.svc.HandleFilter(ctx, &sdkv1.HandleFilterRequest{Name: name, EventJson: ev})
	if err != nil {
		return true, err
	}
	return resp.Allow, nil
}

// HandleHook invokes a hook handler.
func (c *Client) HandleHook(ctx context.Context, name string, e *Event) error {
	ev, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = c.svc.HandleHook(ctx, &sdkv1.HandleHookRequest{Name: name, EventJson: ev})
	return err
}

// HealthCheck probes the plugin's liveness.
func (c *Client) HealthCheck(ctx context.Context) (*sdkv1.HealthResponse, error) {
	return c.svc.HealthCheck(ctx, &sdkv1.Empty{})
}

// Cleanup tells the plugin to run its unload hook.
func (c *Client) Cleanup(ctx context.Context) error {
	_, err := c.svc.Cleanup(ctx, &sdkv1.Empty{})
	return err
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c != nil && c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
