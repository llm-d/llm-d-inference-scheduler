package http

// Plugin common information for external http plugin
type Plugin struct {
	name string
	url  string
}

// Name returns the plugin's name
func (s *Plugin) Name() string {
	return s.name
}
