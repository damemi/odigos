package mcp

// Access-control tool names — exempt from the write gate so the access mode can
// always be read and toggled (otherwise read-write could never be enabled).
const (
	getAccessModeToolName    = "get_access_mode"
	enableWriteModeToolName  = "enable_write_mode"
	disableWriteModeToolName = "disable_write_mode"
)
