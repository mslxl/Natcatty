package main

import "strings"

// The library is pinned to the commit MyGo v0.10.0 binds and mirrors:
// its Go bindings hard-code this ABI, so moving the commit without porting
// them makes the terminal fail to load. See docs/research/go-mygo-rewrite.md
// and the mobile-support research on libghostty-vt.
const (
	upstreamURL  = "https://github.com/ghostty-org/ghostty"
	pinnedCommit = "befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028"

	// pinnedSourceSHA256 is the sha256 of the GitHub codeload tarball for
	// pinnedCommit. git access is not required: the tarball is downloaded,
	// verified and extracted, so every build starts from the same bytes.
	pinnedSourceSHA256 = "0819cdb11f4e88420fb79b71f416c35c5995d7bd5dc044156a8f017467451570"

	// libVersion is what the pinned library reports through
	// ghostty_type_json, and what the Go bindings expect.
	libVersion = "0.1.0-dev"
	zigVersion = "0.16.0"

	manifestSchema    = 1
	releaseTag        = "libghostty-vt-befcdfd2-mobile-1"
	defaultReleaseURL = "https://github.com/mslxl/natcatty/releases/download/" + releaseTag

	androidPageSize = 16384
)

// target describes one phone artifact. iOS builds one XCFramework and takes
// two slices out of it; Android builds one archive per Zig triple.
type target struct {
	Key              string
	Platform         string // "ios" or "android"
	XCFrameworkSlice string // iOS only
	ZigTriple        string // Android only, for zig -Dtarget
	ClangTriple      string // Android only, for the NDK 16 KB link check
}

var targets = []target{
	{Key: "ios-arm64", Platform: "ios", XCFrameworkSlice: "ios-arm64"},
	{Key: "ios-arm64-simulator", Platform: "ios", XCFrameworkSlice: "ios-arm64-simulator"},
	{Key: "android-arm64-v8a", Platform: "android", ZigTriple: "aarch64-linux-android.26", ClangTriple: "aarch64-linux-android26"},
	{Key: "android-armeabi-v7a", Platform: "android", ZigTriple: "arm-linux-androideabi.26", ClangTriple: "armv7a-linux-androideabi26"},
	{Key: "android-x86_64", Platform: "android", ZigTriple: "x86_64-linux-android.26", ClangTriple: "x86_64-linux-android26"},
}

// assetName is the published name. It carries the eight-character commit
// prefix the release tag uses, so an artifact can never be mistaken for one
// built from another commit.
func assetName(t target) string {
	return "libghostty-vt-" + pinnedCommit[:8] + "-" + t.Key + ".a"
}

// expectedSymbols is the set of ghostty_* symbols MyGo v0.10.0's bindings
// look up. An artifact missing any of them would fail at run time, so the
// build fails instead.
var expectedSymbols = strings.Fields(`
ghostty_alloc
ghostty_focus_encode
ghostty_formatter_format_alloc
ghostty_formatter_free
ghostty_formatter_terminal_new
ghostty_free
ghostty_grid_ref_cell
ghostty_grid_ref_graphemes
ghostty_grid_ref_hyperlink_uri
ghostty_grid_ref_row
ghostty_key_encoder_encode
ghostty_key_encoder_free
ghostty_key_encoder_new
ghostty_key_encoder_setopt
ghostty_key_encoder_setopt_from_terminal
ghostty_key_event_free
ghostty_key_event_new
ghostty_key_event_set_action
ghostty_key_event_set_composing
ghostty_key_event_set_consumed_mods
ghostty_key_event_set_key
ghostty_key_event_set_mods
ghostty_key_event_set_unshifted_codepoint
ghostty_key_event_set_utf8
ghostty_kitty_graphics_get
ghostty_kitty_graphics_image
ghostty_kitty_graphics_image_get
ghostty_kitty_graphics_placement_get
ghostty_kitty_graphics_placement_iterator_free
ghostty_kitty_graphics_placement_iterator_new
ghostty_kitty_graphics_placement_next
ghostty_kitty_graphics_placement_render_info
ghostty_mouse_encoder_encode
ghostty_mouse_encoder_free
ghostty_mouse_encoder_new
ghostty_mouse_encoder_reset
ghostty_mouse_encoder_setopt
ghostty_mouse_encoder_setopt_from_terminal
ghostty_mouse_event_clear_button
ghostty_mouse_event_free
ghostty_mouse_event_new
ghostty_mouse_event_set_action
ghostty_mouse_event_set_button
ghostty_mouse_event_set_mods
ghostty_mouse_event_set_position
ghostty_paste_encode
ghostty_paste_is_safe
ghostty_render_state_begin_update
ghostty_render_state_clean
ghostty_render_state_end_update
ghostty_render_state_free
ghostty_render_state_get
ghostty_render_state_new
ghostty_render_state_row_cells_free
ghostty_render_state_row_cells_get
ghostty_render_state_row_cells_new
ghostty_render_state_row_cells_select
ghostty_render_state_row_get
ghostty_render_state_row_iterator_free
ghostty_render_state_row_iterator_new
ghostty_render_state_row_iterator_next
ghostty_row_get
ghostty_search_feed
ghostty_search_free
ghostty_search_get
ghostty_search_new
ghostty_search_set
ghostty_search_tick
ghostty_selection_gesture_event
ghostty_selection_gesture_event_free
ghostty_selection_gesture_event_new
ghostty_selection_gesture_event_set
ghostty_selection_gesture_free
ghostty_selection_gesture_get
ghostty_selection_gesture_new
ghostty_selection_gesture_reset
ghostty_sys_set
ghostty_terminal_compress
ghostty_terminal_compression_activity
ghostty_terminal_free
ghostty_terminal_get
ghostty_terminal_grid_ref
ghostty_terminal_new
ghostty_terminal_paste
ghostty_terminal_point_from_grid_ref
ghostty_terminal_reset
ghostty_terminal_resize
ghostty_terminal_scroll_viewport
ghostty_terminal_select_all
ghostty_terminal_selection_format_alloc
ghostty_terminal_set
ghostty_terminal_vt_write
ghostty_type_json
`)
