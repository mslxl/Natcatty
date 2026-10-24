package catalog

import (
	"fmt"
	"sync"
)

// LoaderName is the transport-control tool that exposes deferred catalog
// groups. It is not a Netcatty capability, so it lives outside the catalog.
const LoaderName = "load_netcatty_tools"

// loaderDescription is copied verbatim from the original
// electron/mcp/progressiveMcpTools.cjs.
const loaderDescription = "Load more Netcatty tools by category. Choose attachments, sftp_advanced, vault_hosts, vault_notes, snippets, scripts, portforward, or all. Newly loaded tools appear immediately without restarting the MCP server."

// CoreNames is the original initial MCP surface, in registration order. The
// loader is the fifteenth initially visible tool.
var CoreNames = []string{
	"get_environment",
	"session_close",
	"terminal_execute",
	"terminal_start",
	"terminal_poll",
	"terminal_stop",
	"sftp_list",
	"sftp_read_file",
	"sftp_write_file",
	"sftp_download",
	"sftp_upload",
	"vault_hosts_list",
	"host_open",
	"terminal_read_context",
}

// Toolsets is the original deferred-tool grouping. The map order is not
// meaningful; ToolsetNames carries the loader's enum order.
var Toolsets = map[string][]string{
	"attachments": {
		"list_attachments",
		"read_attachment",
	},
	"sftp_advanced": {
		"sftp_stat",
		"sftp_home",
		"sftp_mkdir",
		"sftp_delete",
		"sftp_rename",
		"sftp_chmod",
	},
	"vault_hosts": {
		"host_get",
		"vault_hosts_create",
		"vault_hosts_update",
		"vault_hosts_delete",
		"vault_hosts_import",
		"host_notes_get",
		"host_notes_set",
		"vault_identities_list",
		"vault_proxy_profiles_list",
		"vault_groups_list",
		"vault_groups_create",
		"vault_groups_update",
		"vault_groups_delete",
	},
	"vault_notes": {
		"vault_notes_list",
		"vault_notes_get",
		"vault_notes_create",
		"vault_notes_update",
		"vault_notes_delete",
		"vault_notes_import",
	},
	"snippets": {
		"snippets_list",
		"snippets_get",
		"snippets_run",
		"snippets_create",
		"snippets_update",
		"snippets_delete",
	},
	"scripts": {
		"scripts_list",
		"scripts_get",
		"scripts_create",
		"scripts_update",
		"scripts_delete",
		"scripts_run",
		"scripts_reference",
		"scripts_runs_list",
		"scripts_run_stop",
		"scripts_run_pause",
		"scripts_run_resume",
		"scripts_targets_set",
		"host_connect_scripts_list",
		"host_connect_scripts_set",
	},
	"portforward": {
		"portforward_rules_list",
		"portforward_tunnels_list",
		"portforward_rules_create",
		"portforward_rules_update",
		"portforward_rules_duplicate",
		"portforward_rules_delete",
		"portforward_start",
		"portforward_stop",
	},
}

// ToolsetNames is the loader enum order from the original catalog.
var ToolsetNames = []string{
	"attachments",
	"sftp_advanced",
	"vault_hosts",
	"vault_notes",
	"snippets",
	"scripts",
	"portforward",
}

// LoadableToolsetNames is the loader's accepted values: every toolset plus
// "all".
var LoadableToolsetNames = append(append([]string{}, ToolsetNames...), "all")

// LoaderTool is the synthetic tool entry for load_netcatty_tools.
func LoaderTool() Tool {
	tool := Tool{
		Name:        LoaderName,
		Group:       "control",
		Surface:     "loader",
		Description: loaderDescription,
		Input: map[string]InputField{
			"toolset": {
				Type:        "string",
				Description: "Netcatty tool category to load",
				Enum:        LoadableToolsetNames,
			},
		},
		inputOrder: []string{"toolset"},
	}
	return tool
}

// Visibility tracks which catalog tools the current session exposes. It is
// safe for concurrent use, though the stdio server serializes requests.
type Visibility struct {
	mu     sync.Mutex
	order  []string
	loaded map[string]bool
}

// NewVisibility starts with the 14 core tools visible.
func NewVisibility() *Visibility {
	view := &Visibility{
		order:  append([]string(nil), CoreNames...),
		loaded: make(map[string]bool, len(CoreNames)),
	}
	for _, name := range CoreNames {
		view.loaded[name] = true
	}
	return view
}

// Visible returns the loaded catalog tools in visibility order.
func (v *Visibility) Visible() []Tool {
	v.mu.Lock()
	defer v.mu.Unlock()
	tools := make([]Tool, 0, len(v.order))
	for _, name := range v.order {
		if tool, ok := ByName(name); ok {
			tools = append(tools, tool)
		}
	}
	return tools
}

// IsVisible reports whether a catalog tool is currently exposed.
func (v *Visibility) IsVisible(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.loaded[name]
}

// VisibleCount counts the visible tools including the loader.
func (v *Visibility) VisibleCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.order) + 1
}

// LoadedCatalogCount counts visible catalog tools, excluding the loader.
func (v *Visibility) LoadedCatalogCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.order)
}

// RemainingCatalogCount counts catalog tools not yet visible.
func (v *Visibility) RemainingCatalogCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(All()) - len(v.order)
}

// Load exposes a toolset and reports which tools became visible and which were
// already visible. "all" exposes every deferred tool.
func (v *Visibility) Load(toolset string) (newly, already []string, err error) {
	requested, ok := Toolsets[toolset]
	if toolset == "all" {
		requested, ok = allDeferred(), true
	}
	if !ok {
		return nil, nil, fmt.Errorf("unknown Netcatty toolset %q", toolset)
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	for _, name := range requested {
		if v.loaded[name] {
			already = append(already, name)
			continue
		}
		if _, ok := ByName(name); !ok {
			return nil, nil, fmt.Errorf("toolset %q names unknown tool %q", toolset, name)
		}
		v.loaded[name] = true
		v.order = append(v.order, name)
		newly = append(newly, name)
	}
	return newly, already, nil
}

// allDeferred returns every deferred tool in toolset order.
func allDeferred() []string {
	var names []string
	for _, toolset := range ToolsetNames {
		names = append(names, Toolsets[toolset]...)
	}
	return names
}
