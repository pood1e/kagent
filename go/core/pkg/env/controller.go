package env

// KUBECONFIG remains an external client-go interface.
var Kubeconfig = RegisterStringVar("KUBECONFIG", "", "Kubernetes client configuration file list. When unset, client-go uses its normal in-cluster or user kubeconfig discovery.", ComponentController, ComponentCLI, ComponentTesting)

var (
	MCPDiscoveryURL       = RegisterStringVar("KAGENT_MCP_DISCOVERY_URL", "", "Platform gateway origin accepting projected credentials for managed MCP discovery.", ComponentController)
	MCPDiscoveryTokenFile = RegisterStringVar("KAGENT_MCP_DISCOVERY_TOKEN_FILE", "", "Projected Kubernetes service account token for managed MCP discovery.", ComponentController)
)
