package core

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// MCPServerConfig defines the configuration for an external or in-harness MCP server.
type MCPServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// ValidateMCPServer verifies that an MCP server configuration specifies a valid name,
// does not define raw environment secrets in the profile, and specifies either a
// validated executable command or an absolute HTTP(S) URL.
func ValidateMCPServer(cfg MCPServerConfig) error {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		return errors.New("MCP server name is required")
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return fmt.Errorf("MCP server name %q contains invalid characters (must be alphanumeric, '-', or '_')", name)
	}

	if len(cfg.Env) > 0 {
		return fmt.Errorf("MCP server %q env is not supported: secrets must not appear in profiles, rendered configs, or artifacts", name)
	}

	hasCommand := strings.TrimSpace(cfg.Command) != ""
	hasURL := strings.TrimSpace(cfg.URL) != ""

	if !hasCommand && !hasURL {
		return fmt.Errorf("MCP server %q must specify either command or url", name)
	}
	if hasCommand && hasURL {
		return fmt.Errorf("MCP server %q cannot specify both command and url", name)
	}

	if hasCommand {
		cmd := strings.TrimSpace(cfg.Command)
		if strings.ContainsFunc(cmd, unicode.IsControl) {
			return fmt.Errorf("MCP server %q command contains invalid control characters", name)
		}
		for _, arg := range cfg.Args {
			if strings.ContainsFunc(arg, unicode.IsControl) {
				return fmt.Errorf("MCP server %q argument contains invalid control characters", name)
			}
		}
	}

	if hasURL {
		parsed, err := url.Parse(cfg.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("MCP server %q url must be absolute HTTP(S)", name)
		}
	}

	return nil
}
