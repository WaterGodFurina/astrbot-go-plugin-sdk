package sdk

// Command is a message command a plugin accepts. The plugin author only has
// to fill in the descriptor fields and the Handler.
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	Permission  string // "everyone" (default) or "admin"
	Handler     func(e *Event, args []string) (string, error)
}

// Filter is an event filter. Returning false stops propagation of the event
// (mirrors the host's filter semantics).
type Filter struct {
	Name    string
	Handler func(e *Event) bool
}

// Hook is a lifecycle or pipeline hook (e.g. Event "startup", "on_message").
type Hook struct {
	Name    string
	Event   string
	Handler func(e *Event) error
}

// Config exposes the plugin's persisted config (plugins/<name>/config.json).
type Config struct {
	Data map[string]any
}

// Get returns a config value by key.
func (c *Config) Get(key string) (any, bool) {
	if c == nil || c.Data == nil {
		return nil, false
	}
	v, ok := c.Data[key]
	return v, ok
}

// GetString returns a string config value, or "" if missing/not a string.
func (c *Config) GetString(key string) string {
	v, ok := c.Get(key)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// GetBool returns a bool config value, or false.
func (c *Config) GetBool(key string) bool {
	v, ok := c.Get(key)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// Plugin is the declarative plugin definition. Fill it out and pass it to
// Serve() from your main function.
type Plugin struct {
	Name         string
	Version      string
	Description  string
	Author       string
	ConfigSchema map[string]any
	Commands     []Command
	Filters      []Filter
	Hooks        []Hook
	OnConfig     func(cfg *Config) error
	OnUnload     func() error
}
