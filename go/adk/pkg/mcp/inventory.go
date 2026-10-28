package mcp

// IsMCPTool identifies request-scoped tools built by CreateToolsets.
func IsMCPTool(value any) bool {
	switch value.(type) {
	case scopedTool, *scopedTool:
		return true
	default:
		return false
	}
}
