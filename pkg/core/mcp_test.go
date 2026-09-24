package core

import (
	"strings"
	"testing"
)

func TestValidateMCPServer(t *testing.T) {
	tests := []struct {
		name    string
		cfg     MCPServerConfig
		wantErr string
	}{
		{
			name: "valid command server",
			cfg: MCPServerConfig{
				Name:    "fetch",
				Command: "uvx",
				Args:    []string{"mcp-server-fetch"},
			},
		},
		{
			name: "valid url server",
			cfg: MCPServerConfig{
				Name: "remote-sse",
				URL:  "https://mcp.example.com/sse",
			},
		},
		{
			name: "valid url server with http",
			cfg: MCPServerConfig{
				Name: "local-sse",
				URL:  "http://127.0.0.1:8000/sse",
			},
		},
		{
			name: "missing name",
			cfg: MCPServerConfig{
				Command: "uvx",
			},
			wantErr: "MCP server name is required",
		},
		{
			name: "invalid name characters",
			cfg: MCPServerConfig{
				Name:    "bad server name!",
				Command: "uvx",
			},
			wantErr: "contains invalid characters",
		},
		{
			name: "neither command nor url",
			cfg: MCPServerConfig{
				Name: "noop",
			},
			wantErr: "must specify either command or url",
		},
		{
			name: "both command and url",
			cfg: MCPServerConfig{
				Name:    "both",
				Command: "uvx",
				URL:     "https://mcp.example.com/sse",
			},
			wantErr: "cannot specify both command and url",
		},
		{
			name: "command with control characters",
			cfg: MCPServerConfig{
				Name:    "ctrl-cmd",
				Command: "uvx\x00malicious",
			},
			wantErr: "command contains invalid control characters",
		},
		{
			name: "argument with control characters",
			cfg: MCPServerConfig{
				Name:    "ctrl-arg",
				Command: "uvx",
				Args:    []string{"safe", "arg\nbreak"},
			},
			wantErr: "argument contains invalid control characters",
		},
		{
			name: "invalid url scheme",
			cfg: MCPServerConfig{
				Name: "ftp-server",
				URL:  "ftp://mcp.example.com/sse",
			},
			wantErr: "url must be absolute HTTP(S)",
		},
		{
			name: "url server with transport and timeout",
			cfg: MCPServerConfig{
				Name:           "gateway",
				URL:            "http://task-sandbox:10086/sse",
				Transport:      "sse",
				TimeoutSeconds: 1200,
			},
		},
		{
			name: "unknown transport",
			cfg: MCPServerConfig{
				Name:      "gateway",
				URL:       "http://task-sandbox:10086/sse",
				Transport: "websocket",
			},
			wantErr: "transport must be sse or streamable-http",
		},
		{
			name: "transport on a command server",
			cfg: MCPServerConfig{
				Name:      "fetch",
				Command:   "uvx",
				Transport: "sse",
			},
			wantErr: "transport applies only to a url server",
		},
		{
			name: "negative timeout",
			cfg: MCPServerConfig{
				Name:           "gateway",
				URL:            "http://task-sandbox:10086/sse",
				TimeoutSeconds: -1,
			},
			wantErr: "timeout_seconds must not be negative",
		},
		{
			name: "env rejected for security isolation",
			cfg: MCPServerConfig{
				Name:    "env-server",
				Command: "uvx",
				Env:     map[string]string{"SECRET": "leak"},
			},
			wantErr: "env is not supported: secrets must not appear",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMCPServer(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateMCPServer() unexpected error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateMCPServer() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateMCPServer() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}
