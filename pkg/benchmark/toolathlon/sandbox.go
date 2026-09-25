package toolathlon

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hyscale-lab/aries/pkg/core"
	"github.com/hyscale-lab/aries/pkg/runner"
)

//go:embed portfwd.py
var forwarderScript []byte

//go:embed appready.py
var appReadyScript []byte

// Package-level vars, not consts, so tests can shrink them to avoid waiting
// out the real bounds.
var (
	preprocessTimeout = 20 * time.Minute
	// gatewayReadyAttempts covers the gateway's own startup, during which
	// it launches every MCP server the task needs (npx for the Node ones).
	gatewayReadyAttempts   = 120
	gatewayReadyDelay      = time.Second
	forwarderReadyAttempts = 40
	forwarderReadyDelay    = 250 * time.Millisecond
)

// gatewayStartScript backgrounds Toolathlon's MCP gateway from the project
// directory. Its arguments arrive as positional parameters, so no value is
// spliced into shell text: $1 bundle file, $2 port, $3 log file. The gateway
// binds 0.0.0.0 because its client is the harness in another container,
// reaching it over the per-task bridge network by the sandbox's alias; that
// network is internal to the task, so nothing else can. ARIES
// overrides the task image's entrypoint with `/bin/sleep infinity`, so this
// is the only place the gateway is launched, and the backgrounded process
// is reparented to PID 1 when the shell exits (see the SearXNG note in
// deepresearchbench/sandbox.go).
const gatewayStartScript = `cd ` + workspaceRoot + ` && nohup uv run python -m scripts.decoupled.container_tool_gateway ` +
	`--bundle_file "$1" --host 0.0.0.0 --port "$2" --debug >"$3" 2>&1 &`

// forwarderStartScript backgrounds the loopback forwarder: $1 script, $2 log
// file, then the forwarder's own flags.
const forwarderStartScript = `script=$1; log=$2; shift 2; nohup python3 "$script" "$@" >"$log" 2>&1 &`

// httpsProxyStartScript backgrounds Toolathlon's own HTTPS proxy for Canvas,
// from the pinned tree, in front of the forwarded HTTP port: $1 certificate
// directory, $2 log file. It serves https://localhost:20001 and forwards with
// the Host header Canvas's image was configured with, localhost:10001.
const httpsProxyStartScript = `cd ` + workspaceRoot + ` && nohup node ` + httpsProxyEntry + ` 20001 10001 localhost http "$1" >"$2" 2>&1 &`

// appReadyRunScript runs the readiness probe: $1 script, then its flags.
const appReadyRunScript = `script=$1; shift; exec python3 "$script" "$@"`

// Toolathlon's scripts run under `uv run` from the image's own PATH, which
// the sandbox does not resolve for a bare command name. The shell resolves
// it; the arguments are still passed positionally, never spliced into text.
const uvScript = `exec uv "$@"`

// uvCommand is `uv <args>` in the project directory.
func uvCommand(args ...string) core.Command {
	return core.Command{
		Path: "/bin/sh",
		Args: append([]string{"-c", uvScript, "aries-toolathlon-uv"}, args...),
		Dir:  workspaceRoot,
	}
}

// PrepareSandbox reproduces the container-side half of Toolathlon's
// decoupled runner, then hides the grader before any bridge exists:
//
//  1. copy the pinned project tree and the task directory into the image;
//  2. start the loopback forwarder if the task uses a self-hosted application;
//  3. run Toolathlon's preprocess, which seeds the workspace and the
//     applications and writes the task bundle;
//  4. keep a trusted copy of the bundle on the host and check its layout;
//  5. stash the grader and ground truth on the host, and prove them absent;
//  6. start the MCP gateway and wait for its health endpoint;
//  7. inventory the evaluator's runtime (runtime.go), the last thing before
//     the bridge exists, so Evaluate can tell whether the agent touched it.
func (b *Benchmark) PrepareSandbox(ctx context.Context, task core.Task, sandbox runner.Sandbox) error {
	if sandbox == nil {
		return errors.New("toolathlon preparation requires a live sandbox")
	}
	b.mu.RLock()
	details, loaded := b.details[task.ID]
	b.mu.RUnlock()
	if !loaded {
		return fmt.Errorf("toolathlon task %q was not loaded by Tasks", task.ID)
	}
	if err := VerifyRevision(ctx, b.root, b.revision); err != nil {
		return fmt.Errorf("reverify toolathlon checkout before preparation: %w", err)
	}

	hostDir := filepath.Join(b.outputDir, task.ID, "toolathlon")
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		return fmt.Errorf("create toolathlon artifact directory: %w", err)
	}
	timeline := newPrepareTimeline()
	defer timeline.write(filepath.Join(hostDir, prepareTimelineHostName))

	// A reused or dirty image must not pre-seed the two files Evaluate
	// trusts, nor carry a previous task's private state.
	if err := removePaths(ctx, sandbox, []string{trajectoryPath, evalResultPath, privateRoot, taskDirectoryPath(details.name)}); err != nil {
		return fmt.Errorf("clean sandbox before harness: %w", err)
	}
	if err := execOK(ctx, sandbox, core.Command{Path: "/bin/mkdir", Args: []string{"-p", "-m", "700", "--", privateRoot}}, "create private directory"); err != nil {
		return err
	}
	if err := execOK(ctx, sandbox, core.Command{Path: "/bin/mkdir", Args: []string{"-p", "--", taskRootPath}}, "create task root"); err != nil {
		return err
	}

	if err := b.installProject(ctx, sandbox, details.name, details.extraEntries, hostDir); err != nil {
		return err
	}
	timeline.mark("project_installed")
	if details.needsCredentials {
		if err := b.installCredentials(ctx, sandbox, hostDir); err != nil {
			return err
		}
		timeline.mark("credentials_installed")
	}
	if details.needsApplications {
		if err := b.startForwarder(ctx, sandbox, details); err != nil {
			return err
		}
		timeline.mark("forwarder_ready")
	}
	if details.companions {
		if err := b.awaitApplications(ctx, sandbox, details, hostDir); err != nil {
			return err
		}
		timeline.mark("applications_ready")
	}
	if err := b.runPreprocess(ctx, sandbox, details.name, hostDir); err != nil {
		return err
	}
	timeline.mark("preprocess_done")
	bundleHostPath := filepath.Join(hostDir, "task_bundle.json")
	if err := sandbox.Download(ctx, bundleContainerPath, bundleHostPath); err != nil {
		return fmt.Errorf("download task bundle: %w", err)
	}
	if err := validateBundleFile(bundleHostPath, details); err != nil {
		return fmt.Errorf("validate task bundle: %w", err)
	}
	stashed, err := stashArtifacts(ctx, sandbox, details.name, filepath.Join(hostDir, "artifact-stash.tar"))
	if err != nil {
		return fmt.Errorf("stash grader artifacts before harness: %w", err)
	}
	b.mu.Lock()
	details.stashed = stashed
	b.details[task.ID] = details
	b.mu.Unlock()

	timeline.mark("grader_stashed")
	if err := b.startGateway(ctx, sandbox); err != nil {
		return err
	}
	timeline.mark("gateway_ready")
	// The gateway read the bundle at startup; the container copy is not
	// needed again until evaluation re-injects the trusted host copy.
	if err := removePaths(ctx, sandbox, []string{bundleContainerPath}); err != nil {
		return err
	}
	if err := writeRuntimeManifest(ctx, sandbox, filepath.Join(hostDir, runtimeManifestHostName)); err != nil {
		return fmt.Errorf("inventory evaluator runtime before harness: %w", err)
	}
	timeline.mark("prepared")
	return nil
}

// prepareTimeline records when each preparation step finished, as absolute
// UTC times, so that preparation can be lined up with the sandbox's own
// records (companions.json) and the run's other phases. It is written even
// when preparation fails, up to the last step that finished.
type prepareTimeline struct {
	steps []prepareStep
}

type prepareStep struct {
	Step string    `json:"step"`
	At   time.Time `json:"at"`
}

func newPrepareTimeline() *prepareTimeline {
	return &prepareTimeline{steps: []prepareStep{{Step: "prepare_started", At: time.Now().UTC()}}}
}

func (t *prepareTimeline) mark(step string) {
	t.steps = append(t.steps, prepareStep{Step: step, At: time.Now().UTC()})
}

// write is best effort: the timeline is evidence about a run, and a failure
// to write it must not change the run's outcome.
func (t *prepareTimeline) write(path string) {
	if content, err := json.MarshalIndent(t.steps, "", "  "); err == nil {
		_ = os.WriteFile(path, append(content, '\n'), 0o600)
	}
}

// installProject uploads one archive of the pinned project tree and the task
// directory and extracts it over the image's own copy.
func (b *Benchmark) installProject(ctx context.Context, sandbox runner.Sandbox, taskName string, extras []string, hostDir string) error {
	archive := filepath.Join(hostDir, "project.tar")
	if err := writeProjectArchive(b.root, taskName, extras, archive); err != nil {
		return err
	}
	return extractArchive(ctx, sandbox, archive, archiveContainerPath, "project archive")
}

// installCredentials overlays the credentials directory on the project's
// configs/ (see credentials.go). The archive lives on the host only for
// the upload.
func (b *Benchmark) installCredentials(ctx context.Context, sandbox runner.Sandbox, hostDir string) error {
	archive := filepath.Join(hostDir, "credentials.tar")
	if err := writeCredentialsArchive(b.credentialsDir, archive); err != nil {
		return err
	}
	return extractArchive(ctx, sandbox, archive, credentialsArchiveContainerPath, "credentials archive")
}

// extractArchive uploads a host archive, extracts it at workspaceRoot, and
// removes both copies.
func extractArchive(ctx context.Context, sandbox runner.Sandbox, hostArchive, containerArchive, what string) error {
	defer os.Remove(hostArchive)
	if err := sandbox.Upload(ctx, hostArchive, containerArchive); err != nil {
		return fmt.Errorf("upload %s: %w", what, err)
	}
	if err := execOK(ctx, sandbox, core.Command{Path: tarPath, Args: []string{"-C", workspaceRoot, "-xf", containerArchive}}, "extract "+what); err != nil {
		return err
	}
	return removePaths(ctx, sandbox, []string{containerArchive})
}

// uploadScript stages one embedded program on the host and uploads it.
func (b *Benchmark) uploadScript(ctx context.Context, sandbox runner.Sandbox, content []byte, pattern, destination string) error {
	what := filepath.Base(destination)
	scriptHost, err := os.CreateTemp(b.outputDir, pattern)
	if err != nil {
		return fmt.Errorf("stage %s: %w", what, err)
	}
	defer os.Remove(scriptHost.Name())
	if _, err := scriptHost.Write(content); err != nil {
		scriptHost.Close()
		return fmt.Errorf("stage %s: %w", what, err)
	}
	if err := scriptHost.Close(); err != nil {
		return fmt.Errorf("stage %s: %w", what, err)
	}
	if err := sandbox.Upload(ctx, scriptHost.Name(), destination); err != nil {
		return fmt.Errorf("upload %s: %w", what, err)
	}
	return nil
}

// forwarderRoutes are the forwarder's flags: every application port to the
// shared deployment, or, with companions, each port of the task's own
// applications to the companion that serves it.
func (b *Benchmark) forwarderRoutes(details taskDetails) []string {
	if details.companions {
		var routes []string
		for _, application := range details.applications {
			for _, route := range applicationRoutes[application] {
				routes = append(routes, fmt.Sprintf("%d=%s:%d", route.port, route.companion, route.target))
			}
		}
		return []string{"--map", strings.Join(routes, ",")}
	}
	target := b.appHost
	if target == "" {
		target = "auto"
	}
	ports := make([]string, 0, len(applicationPorts))
	for _, port := range applicationPorts {
		ports = append(ports, strconv.Itoa(port))
	}
	return []string{"--target", target, "--ports", strings.Join(ports, ",")}
}

func (b *Benchmark) startForwarder(ctx context.Context, sandbox runner.Sandbox, details taskDetails) error {
	if err := b.uploadScript(ctx, sandbox, forwarderScript, ".portfwd-*.py", forwarderScriptPath); err != nil {
		return err
	}
	args := append([]string{"-c", forwarderStartScript, "aries-toolathlon-portfwd", forwarderScriptPath, forwarderLogPath, "--ready", forwarderReadyPath}, b.forwarderRoutes(details)...)
	started, err := sandbox.Exec(ctx, core.Command{Path: "/bin/sh", Args: args})
	if err != nil {
		return fmt.Errorf("start loopback forwarder: %w", err)
	}
	if started.ExitCode != 0 {
		return fmt.Errorf("start loopback forwarder: exit code %d", started.ExitCode)
	}
	for attempt := 0; attempt < forwarderReadyAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("await loopback forwarder: %w", ctx.Err())
			case <-time.After(forwarderReadyDelay):
			}
		}
		probed, err := sandbox.Exec(ctx, core.Command{Path: "/bin/sh", Args: []string{"-c", `[ -s "$1" ]`, "aries-toolathlon-portfwd-ready", forwarderReadyPath}})
		if err == nil && probed.ExitCode == 0 {
			return nil
		}
	}
	return errors.New("loopback forwarder did not become ready before the bound")
}

// awaitApplications starts Canvas's HTTPS proxy when the task uses Canvas,
// then waits until every companion application answers through the
// forwarder at the protocol its MCP server speaks, and keeps the seconds
// each took in the run directory (app-ready.json).
func (b *Benchmark) awaitApplications(ctx context.Context, sandbox runner.Sandbox, details taskDetails, hostDir string) error {
	if slices.Contains(details.applications, "canvas") {
		command := core.Command{Path: "/bin/sh", Args: []string{"-c", httpsProxyStartScript, "aries-toolathlon-https-proxy", httpsProxyCertDir, httpsProxyLogPath}}
		if err := execOK(ctx, sandbox, command, "start Canvas HTTPS proxy"); err != nil {
			return err
		}
	}
	if err := b.uploadScript(ctx, sandbox, appReadyScript, ".appready-*.py", appReadyScriptPath); err != nil {
		return err
	}
	result, err := sandbox.Exec(ctx, core.Command{Path: "/bin/sh", Args: []string{
		"-c", appReadyRunScript, "aries-toolathlon-appready", appReadyScriptPath,
		"--apps", strings.Join(details.applications, ","),
		"--timeout", strconv.Itoa(b.readySeconds),
		"--out", appReadyResultPath,
	}})
	if err != nil {
		return fmt.Errorf("await applications: %w", err)
	}
	if err := sandbox.Download(ctx, appReadyResultPath, filepath.Join(hostDir, appReadyHostName)); err != nil && result.ExitCode == 0 {
		return fmt.Errorf("download application readiness: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("applications did not become ready: %s", strings.TrimSpace(result.Stdout+" "+result.Stderr))
	}
	return nil
}

// runPreprocess runs Toolathlon's container_preprocess with the same
// arguments its decoupled runner uses, minus the ones that only matter to
// its host-side agent loop, and keeps its full output on the host.
func (b *Benchmark) runPreprocess(ctx context.Context, sandbox runner.Sandbox, taskName, hostDir string) error {
	command := uvCommand(
		"run", "python", "-m", "scripts.decoupled.container_preprocess",
		"--eval_config", evalConfigPath,
		"--task_dir", taskPool+"/"+taskName,
		"--max_steps_under_single_turn_mode", strconv.Itoa(b.maxSteps),
		"--model_short_name", b.modelName,
		"--provider", "unified",
		"--bundle_file", bundleContainerPath,
		"--host_output_folder", taskRootPath,
		"--debug",
	)
	command.Env = map[string]string{"TOOLATHLON_OPENAI_BASE_URL": modelPlaceholderURL}
	command.Timeout = preprocessTimeout
	result, execErr := sandbox.Exec(ctx, command)
	logPath := filepath.Join(hostDir, "preprocess.log")
	if err := os.WriteFile(logPath, []byte(result.Stdout+result.Stderr), 0o600); err != nil {
		return fmt.Errorf("write preprocess log: %w", err)
	}
	if execErr != nil {
		return fmt.Errorf("run toolathlon preprocess: %w", execErr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("run toolathlon preprocess: exit code %d (see %s)", result.ExitCode, logPath)
	}
	return nil
}

func (b *Benchmark) startGateway(ctx context.Context, sandbox runner.Sandbox) error {
	port := strconv.Itoa(b.gatewayPort)
	started, err := sandbox.Exec(ctx, core.Command{
		Path: "/bin/sh",
		Args: []string{"-c", gatewayStartScript, "aries-toolathlon-gateway", bundleContainerPath, port, gatewayLogPath},
	})
	if err != nil {
		return fmt.Errorf("start MCP gateway: %w", err)
	}
	if started.ExitCode != 0 {
		return fmt.Errorf("start MCP gateway: exit code %d", started.ExitCode)
	}
	healthURL := "http://127.0.0.1:" + port + "/health"
	for attempt := 0; attempt < gatewayReadyAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("await MCP gateway readiness: %w", ctx.Err())
			case <-time.After(gatewayReadyDelay):
			}
		}
		probed, err := sandbox.Exec(ctx, core.Command{
			Path: "/bin/sh", Args: []string{"-c", `curl -sf -o /dev/null "$1"`, "aries-toolathlon-gateway-health", healthURL},
		})
		if err == nil && probed.ExitCode == 0 {
			return nil
		}
	}
	return errors.New("MCP gateway did not become ready before the bound")
}

// taskBundle is the part of Toolathlon's phase bundle the adapter checks.
type taskBundle struct {
	SchemaVersion      int             `json:"schema_version"`
	TaskDir            string          `json:"task_dir"`
	NeededMCPServers   json.RawMessage `json:"needed_mcp_servers"`
	ContainerPaths     bundlePaths     `json:"container_paths"`
	ResolvedTaskConfig json.RawMessage `json:"resolved_task_config"`
}

type bundlePaths struct {
	TaskRoot       string `json:"task_root"`
	AgentWorkspace string `json:"agent_workspace"`
	LogFile        string `json:"log_file"`
}

// validateBundleFile is the adapter's equivalent of the checks the decoupled
// runner makes on the bundle it preserves outside the container: the layout
// Evaluate relies on must be the one preprocess actually produced.
func validateBundleFile(hostPath string, details taskDetails) error {
	content, err := os.ReadFile(hostPath)
	if err != nil {
		return err
	}
	return validateBundle(content, details)
}

func validateBundle(content []byte, details taskDetails) error {
	var bundle taskBundle
	if err := json.Unmarshal(content, &bundle); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if bundle.SchemaVersion != 2 {
		return fmt.Errorf("unsupported schema version %d", bundle.SchemaVersion)
	}
	if want := taskPool + "/" + details.name; bundle.TaskDir != want {
		return fmt.Errorf("task_dir %q; want %q", bundle.TaskDir, want)
	}
	if bundle.ContainerPaths != (bundlePaths{TaskRoot: taskRootPath, AgentWorkspace: agentWorkspacePath, LogFile: trajectoryPath}) {
		return fmt.Errorf("container paths %+v do not match the fixed layout", bundle.ContainerPaths)
	}
	var resolved map[string]json.RawMessage
	if len(bundle.ResolvedTaskConfig) == 0 || json.Unmarshal(bundle.ResolvedTaskConfig, &resolved) != nil || resolved == nil {
		return errors.New("resolved_task_config is missing")
	}
	servers, err := serverNames(bundle.NeededMCPServers)
	if err != nil {
		return fmt.Errorf("needed_mcp_servers: %w", err)
	}
	want := slices.Clone(details.servers)
	slices.Sort(servers)
	slices.Sort(want)
	if !slices.Equal(servers, want) {
		return fmt.Errorf("needed_mcp_servers %v; task config says %v", servers, want)
	}
	return nil
}

func execOK(ctx context.Context, sandbox runner.Sandbox, command core.Command, what string) error {
	result, err := sandbox.Exec(ctx, command)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s: exit code %d: %s", what, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}
