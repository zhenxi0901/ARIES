package hermes

import (
	"strings"
	"testing"

	"github.com/hyscale-lab/aries/internal/harness"
	"github.com/hyscale-lab/aries/pkg/core"
)

// The MCP block is rendered into config.yaml from the servers the profile
// configures and the ones a benchmark serves from its own sandbox. Hermes
// assumes streamable-http when no transport is given, so only "sse" is
// written out; the timeout is per tool call.
func TestRenderConfigWritesTheMCPServerBlock(t *testing.T) {
	settings := baseSettings()
	settings.mcpServers = []harness.MCPServerConfig{
		{Name: "toolathlon", URL: "http://task-sandbox:10086/sse", Transport: "sse", TimeoutSeconds: 1200},
		{Name: "docs", URL: "https://mcp.example.invalid/mcp", Transport: "streamable-http"},
	}
	rendered, err := renderConfig(core.ModelConfig{Provider: "deepseek", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com/v1", APIKeyEnv: "DEEPSEEK_API_KEY"}, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "\nmcp_servers:\n" +
		"  toolathlon:\n" +
		"    url: \"http://task-sandbox:10086/sse\"\n" +
		"    transport: \"sse\"\n" +
		"    timeout: 1200\n" +
		"  docs:\n" +
		"    url: \"https://mcp.example.invalid/mcp\"\n"
	if !strings.Contains(string(rendered), want) {
		t.Fatalf("rendered:\n%s\nwant to contain:\n%s", rendered, want)
	}
}

func TestRenderConfigOmitsTheMCPBlockWhenThereAreNoServers(t *testing.T) {
	rendered, err := renderConfig(core.ModelConfig{Provider: "deepseek", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com/v1", APIKeyEnv: "DEEPSEEK_API_KEY"}, baseSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "mcp_servers:") {
		t.Fatalf("rendered an MCP block with no servers:\n%s", rendered)
	}
}

// A benchmark's server is added to what the profile configures, and its host
// address (ClientURL) is used by ARIES's own client alone -- the harness is
// configured with the task-network address.
func TestMergeSeparatesTheHarnessFromAriesOwnClient(t *testing.T) {
	configured := []harness.MCPServerConfig{{Name: "docs", URL: "https://docs.example/mcp"}}
	provided := []core.MCPServer{
		{Name: "toolathlon", URL: "http://task-sandbox:10086/sse", ClientURL: "http://127.0.0.1:49173/sse", Transport: "sse", TimeoutSeconds: 1200},
		{Name: "unpublished", URL: "http://task-sandbox:10087/sse", Transport: "sse"},
	}
	render, clients := harness.Merge(configured, provided)
	if len(render) != 3 || render[1].URL != "http://task-sandbox:10086/sse" || render[2].Name != "unpublished" {
		t.Fatalf("render = %+v", render)
	}
	if len(clients) != 2 || clients[1].URL != "http://127.0.0.1:49173/sse" {
		t.Fatalf("clients = %+v", clients)
	}
	if clients[1].TimeoutSeconds != 1200 || clients[1].Transport != "sse" {
		t.Fatalf("client entry lost its transport or timeout: %+v", clients[1])
	}
}
