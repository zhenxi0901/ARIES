package runner

import (
	"context"
	"errors"
	"io"

	"github.com/hyscale-lab/aries/pkg/core"
)

// ErrNotFound is returned (wrapped) by Sandbox.Download when the requested
// source path does not exist in the sandbox, distinguishing a legitimately
// absent file from a genuine download failure (network/daemon/permission
// errors, disk issues, etc.).
var ErrNotFound = errors.New("sandbox path not found")

// Benchmark owns task discovery and evaluation.
type Benchmark interface {
	Tasks(context.Context) ([]core.Task, error)
	PrepareSandbox(context.Context, core.Task, Sandbox) error
	Evaluate(context.Context, core.Task, Sandbox) (core.Evaluation, error)
}

// AgentHarness owns one task-local agent runtime.
type AgentHarness interface {
	Start(context.Context, core.HarnessRequest) error
	Run(context.Context, string) (core.HarnessResult, error)
	Stop(context.Context) error
}

// ToolSandbox owns the lifecycle of the live environment later inspected by
// evaluation.
type ToolSandbox interface {
	Start(context.Context, core.SandboxRequest) (Sandbox, error)
	Stop(context.Context, Sandbox) error
}

// Sandbox is the live capability returned by ToolSandbox, not a fifth
// substitutable component role.
type Sandbox interface {
	Exec(context.Context, core.Command) (core.CommandResult, error)
	Upload(context.Context, string, string) error
	Download(context.Context, string, string) error
}

// LimitedDownloader is an optional sandbox capability for downloads that must
// be rejected before more than maxBytes can be written to the host.
type LimitedDownloader interface {
	DownloadLimit(ctx context.Context, source, destination string, maxBytes int64) error
}

// StreamExecutor is an optional sandbox capability for keeping command output
// outside an agent-writable container filesystem.
type StreamExecutor interface {
	ExecStream(ctx context.Context, command core.Command, stdin io.Reader, stdout, stderr io.Writer) (core.CommandResult, error)
}

// SandboxAddressing is an optional sandbox capability: how a port the sandbox
// itself serves is addressed by the harness container, which shares the task's
// network (NetworkAlias), and by the ARIES process on the host
// (PublishedAddress), which does not. PublishedAddress answers only for ports
// the benchmark declared in core.Environment.PublishPorts. Docker answers with
// its network alias and a loopback host port; a Kubernetes deployment answers
// with a Service name and its own published address.
type SandboxAddressing interface {
	NetworkAlias() string
	PublishedAddress(ctx context.Context, containerPort int) (string, error)
}

// MCPServerProvider is an optional benchmark capability: the MCP servers the
// prepared sandbox serves for this task. The runner collects them after
// PrepareSandbox and hands them to the harness, so a benchmark whose tools are
// an MCP server inside its own sandbox needs no wiring of its own.
type MCPServerProvider interface {
	MCPServers(ctx context.Context, task core.Task, sandbox Sandbox) ([]core.MCPServer, error)
}

// ToolBridge grants and then positively revokes harness access to a sandbox.
// A nil Stop error is the positive revocation confirmation.
type ToolBridge interface {
	Start(context.Context, Sandbox) (core.ToolEndpoint, error)
	Stop(context.Context) error
}
