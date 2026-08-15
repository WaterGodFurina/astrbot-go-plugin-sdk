// Package sdk is the public plugin SDK for AstrBot Go.
//
// Plugin authors build their plugin against THIS module (a standalone module
// independent of the AstrBot host) and call sdk.Serve in their main function.
// The host process talks to the plugin over gRPC (go-plugin) using the
// contract defined in proto/plugin.proto and generated under gen/.
package sdk

import "encoding/json"

// ComponentType identifies the kind of message component.
// Values mirror pkg/message.ComponentType in the host.
type ComponentType string

const (
	CompPlain    ComponentType = "Plain"
	CompAt       ComponentType = "At"
	CompAtAll    ComponentType = "AtAll"
	CompReply    ComponentType = "Reply"
	CompImage    ComponentType = "Image"
	CompRecord   ComponentType = "Record"
	CompFile     ComponentType = "File"
	CompVideo    ComponentType = "Video"
	CompFace     ComponentType = "Face"
	CompEmoji    ComponentType = "Emoji"
	CompNode     ComponentType = "Node"
	CompNodes    ComponentType = "Nodes"
	CompPoke     ComponentType = "Poke"
	CompMusic    ComponentType = "Music"
	CompForward  ComponentType = "Forward"
	CompJson     ComponentType = "Json"
	CompShare    ComponentType = "Share"
	CompContact  ComponentType = "Contact"
	CompLocation ComponentType = "Location"
	CompShake    ComponentType = "Shake"
	CompDice     ComponentType = "Dice"
	CompRPS      ComponentType = "RPS"
	CompUnknown  ComponentType = "Unknown"
)

// Component is a serializable message segment. All fields are flat so the
// struct JSON-encodes cleanly across the RPC boundary.
type Component struct {
	Type     ComponentType  `json:"type"`
	Text     string         `json:"text,omitempty"`
	TargetID string         `json:"target_id,omitempty"`
	Name     string         `json:"name,omitempty"`
	URL      string         `json:"url,omitempty"`
	Path     string         `json:"path,omitempty"`
	File     string         `json:"file,omitempty"`
	Base64   string         `json:"base64,omitempty"`
	FileID   string         `json:"file_id,omitempty"`
	ID       string         `json:"id,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

// Text creates a plain-text component.
func Text(text string) Component { return Component{Type: CompPlain, Text: text} }

// ImageURL creates an image component from a URL.
func ImageURL(url string) Component { return Component{Type: CompImage, URL: url} }

// ImageFile creates an image component from a local path.
func ImageFile(path string) Component { return Component{Type: CompImage, Path: path} }

// Event is a lightweight, serializable view of an incoming message event.
// It is the plugin-facing equivalent of the host's core.Event.
type Event struct {
	Type       string         `json:"type"`
	Platform   string         `json:"platform"`
	SelfID     string         `json:"self_id,omitempty"`
	SenderID   string         `json:"sender_id"`
	SenderName string         `json:"sender_name"`
	ConvID     string         `json:"conv_id"`
	GroupName  string         `json:"group_name,omitempty"`
	IsGroup    bool           `json:"is_group"`
	IsAtBot    bool           `json:"is_at_bot"`
	IsAdmin    bool           `json:"is_admin"`
	MessageStr string         `json:"message_str"`
	PlainText  string         `json:"plain_text"`
	RawMessage string         `json:"raw_message,omitempty"`
	MessageID  string         `json:"message_id,omitempty"`
	Timestamp  int64          `json:"timestamp"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Chain      []Component    `json:"chain,omitempty"`
}

// GetSenderID returns the sender's user ID.
func (e *Event) GetSenderID() string {
	if e == nil {
		return ""
	}
	return e.SenderID
}

// GetGroupID returns the conversation/group ID, or "" for friend chats.
func (e *Event) GetGroupID() string {
	if e == nil || !e.IsGroup {
		return ""
	}
	return e.ConvID
}

// IsGroupMessage reports whether the event came from a group chat.
func (e *Event) IsGroupMessage() bool { return e != nil && e.IsGroup }

// IsAdminUser reports whether the sender is an admin.
func (e *Event) IsAdminUser() bool { return e != nil && e.IsAdmin }

// GetMessageStr returns the raw message text.
func (e *Event) GetMessageStr() string {
	if e == nil {
		return ""
	}
	return e.MessageStr
}

// MarshalJSON is provided for convenience/validation.
func (e *Event) MarshalJSON() ([]byte, error) { return json.Marshal(*e) }
