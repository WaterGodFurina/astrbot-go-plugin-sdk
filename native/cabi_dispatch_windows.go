//go:build windows

// Windows C-ABI method dispatcher. A C ABI cannot carry Go values, so the host
// sends `encoding/json` request envelopes (defined in cabi_wire.go) and this
// dispatcher decodes them into native Go values, calls the in-process
// sdk.PluginService, and JSON-encodes the native result. No protobuf.
package native

import (
	"context"
	"encoding/json"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
)

// cabiCall decodes req (JSON) into the method's request envelope, invokes svc
// and JSON-encodes the native result. Covers every PluginService method so the
// C ABI surface stays 1:1 with the Go interface.
func cabiCall(svc sdk.PluginService, method string, req []byte) ([]byte, error) {
	ctx := context.Background()
	switch method {
	case "Register":
		in := CABIRegisterRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		out, err := svc.Register(ctx, in.ProtocolVersion)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "HandleCommand":
		in := CABIHandleCommandRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		out, err := svc.HandleCommand(ctx, in.Name, in.Args, in.Event)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "HandleFilter":
		in := CABIHandleFilterRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		out, err := svc.HandleFilter(ctx, in.Name, in.Event)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "HandleHook":
		in := CABIHandleHookRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		out, err := svc.HandleHook(ctx, in.Name, in.Event, in.Chain, []byte(in.PayloadJSON))
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "HandleLLMRequest":
		in := CABIHandleLLMRequestRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		out, err := svc.HandleLLMRequest(ctx, in.Name, in.Event, in.SystemPrompt, in.UserPrompt)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "HandleTool":
		in := CABIHandleToolRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		out, err := svc.HandleTool(ctx, in.Name, in.Args, in.Event)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "ListTools":
		out, err := svc.ListTools(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "ListWebApis":
		out, err := svc.ListWebApis(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "HandleWebRequest":
		in := CABIHandleWebRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		out, err := svc.HandleWebRequest(ctx, in.Req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "HealthCheck":
		out, err := svc.HealthCheck(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "SetLogLevel":
		in := CABISetLogLevelRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		if err := svc.SetLogLevel(ctx, in.Level); err != nil {
			return nil, err
		}
		return nil, nil
	case "FeedSessionWait":
		in := CABIFeedSessionWaitRequest{}
		if err := decodeCABI(req, &in); err != nil {
			return nil, err
		}
		out, err := svc.FeedSessionWait(ctx, in.Event)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "GetConfigSchema":
		out, err := svc.GetConfigSchema(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "Cleanup":
		if err := svc.Cleanup(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	case "FeedCronJob":
		return nil, sdk.Errorf(sdk.CodeUnimplemented, "FeedCronJob is not implemented by the Go plugin SDK")
	case "ManagePlugin":
		return nil, sdk.Errorf(sdk.CodeUnimplemented, "ManagePlugin is not implemented by the Go plugin SDK")
	default:
		return nil, sdk.Errorf(sdk.CodeUnimplemented, "unknown method %q", method)
	}
}

// decodeCABI unmarshals a non-empty JSON envelope, tolerating an empty request
// for methods with no arguments.
func decodeCABI(req []byte, out any) error {
	if len(req) == 0 {
		return nil
	}
	return json.Unmarshal(req, out)
}
