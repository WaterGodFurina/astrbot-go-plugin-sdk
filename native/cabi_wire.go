package native

// cabi_wire.go defines the JSON request envelopes used by the Windows C-ABI
// bridge (cabi_windows.go / cabi_dispatch_windows.go). A C ABI cannot carry Go
// values, so requests cross as `encoding/json` bytes; responses are the JSON
// encoding of the corresponding sdk native result type (PluginInfo,
// HandleCommandResult, ...). The host and the DLL share these exported types,
// so the wire format is defined exactly once.
//
// This file is platform-neutral so the Windows host loader can import it.

import (
	"encoding/json"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
)

// CABIRegisterRequest is the request envelope for method "Register".
type CABIRegisterRequest struct {
	ProtocolVersion int32 `json:"protocol_version"`
}

// CABIHandleCommandRequest is the request envelope for method "HandleCommand".
type CABIHandleCommandRequest struct {
	Name  string     `json:"name"`
	Args  []string   `json:"args"`
	Event *sdk.Event `json:"event"`
}

// CABIHandleFilterRequest is the request envelope for method "HandleFilter".
type CABIHandleFilterRequest struct {
	Name  string     `json:"name"`
	Event *sdk.Event `json:"event"`
}

// CABIHandleHookRequest is the request envelope for method "HandleHook".
// PayloadJSON carries the hook's typed payload as raw JSON (not base64).
type CABIHandleHookRequest struct {
	Name        string          `json:"name"`
	Event       *sdk.Event      `json:"event"`
	Chain       []sdk.Component `json:"chain"`
	PayloadJSON json.RawMessage `json:"payload_json,omitempty"`
}

// CABIHandleLLMRequestRequest is the request envelope for method
// "HandleLLMRequest".
type CABIHandleLLMRequestRequest struct {
	Name         string     `json:"name"`
	Event        *sdk.Event `json:"event"`
	SystemPrompt string     `json:"system_prompt"`
	UserPrompt   string     `json:"user_prompt"`
}

// CABIHandleToolRequest is the request envelope for method "HandleTool".
type CABIHandleToolRequest struct {
	Name  string         `json:"name"`
	Args  map[string]any `json:"args"`
	Event *sdk.Event     `json:"event"`
}

// CABISetLogLevelRequest is the request envelope for method "SetLogLevel".
type CABISetLogLevelRequest struct {
	Level string `json:"level"`
}

// CABIHandleWebRequest is the request envelope for method "HandleWebRequest".
type CABIHandleWebRequest struct {
	Req sdk.HandleWebRequest `json:"req"`
}

// CABIFeedSessionWaitRequest is the request envelope for method
// "FeedSessionWait".
type CABIFeedSessionWaitRequest struct {
	Event *sdk.Event `json:"event"`
}
