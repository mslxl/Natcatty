package plugin

// Contributions is the set of static extension points a manifest declares.
// Runtime registration is not allowed in v1: the host knows every
// contribution before the guest starts.
type Contributions struct {
	Commands    []Command             `toml:"commands"`
	Menus       []Menu                `toml:"menus"`
	Keybindings []Keybinding          `toml:"keybindings"`
	Views       []ViewContribution    `toml:"views"`
	Language    LanguageContributions `toml:"language"`
	SystemPanel []PanelSection        `toml:"system_panel"`
	// MCPTools are the external-MCP tools this plugin contributes to the
	// host's single MCP server. They are declared statically so install
	// consent, the client-visible list, and audit agree before any guest code
	// runs.
	MCPTools []MCPTool `toml:"mcp_tools"`
	// Connections are the terminal protocols this plugin owns. Each id is
	// namespaced x-<publisher>-<name>; a built-in id is reserved and two
	// enabled providers cannot claim the same protocol.
	Connections []ConnectionProvider `toml:"connections"`
	// DataProviders are the non-terminal data sources this plugin contributes.
	// They are distinct from Hosts, Sessions, and Connection Providers; a
	// profile names one qualified provider and the host enforces its declared
	// capability family and access mode.
	DataProviders []DataProvider `toml:"data_providers"`
}

// Command is one command-palette/menu/keybinding action contributed by a
// plugin. ID is local to the plugin; the host registers it under
// QualifiedCommandID(pluginID, ID).
type Command struct {
	ID    string `toml:"id"`
	Title string `toml:"title"`
	// TitleKey names a catalog message resolved at display time in the host
	// locale. Title stays the literal fallback.
	TitleKey string   `toml:"title_key"`
	Keywords []string `toml:"keywords"`
}

// Menu is one application-menu item. It runs the referenced command.
type Menu struct {
	Command string `toml:"command"`
	Label   string `toml:"label"`
	// LabelKey names a catalog message resolved at display time in the host
	// locale. Label stays the literal fallback.
	LabelKey string `toml:"label_key"`
}

// Keybinding is one accelerator that runs the referenced command.
type Keybinding struct {
	Command     string `toml:"command"`
	Accelerator string `toml:"accelerator"`
}
