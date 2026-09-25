# ToolSandbox

`ToolSandbox` owns the task environment. It starts one isolated environment for
a task, returns the narrow live capability needed by the selected bridge and
benchmark, and stops the environment with positive confirmation that owned
resources are absent.

## Boundary and lifecycle

The sandbox begins before bridge or harness startup and stays live through
independent benchmark evaluation. It does not own harness policy, tool
credentials, or benchmark scoring. Cleanup is idempotent, reverse-order, and
bounded after cancellation; failure to confirm resource absence remains a
cleanup failure.

The current implementation uses Docker through the Moby Go SDK. ARIES owns its
containers and networks and never shells out to Docker for lifecycle
operations. A pair-specific bridge may use a narrow sandbox capability such as
streaming command execution, but the harness does not receive Docker daemon
access.

## Companions

A benchmark may ask for companion containers beside the sandbox
(`core.Environment.Companions`): services its tools talk to, such as a web
application seeded for the task. The Docker implementation starts each one on
the task's own network under its aliases, before the task container, with no
published ports, bind mounts, or Docker socket, labelled
`aries.component=application` so the resource monitor samples it. Companions
are removed with their anonymous volumes when the sandbox stops, so every task
occurrence starts from a fresh copy and occurrences can run concurrently
without sharing state. Readiness is the benchmark's to check. On a pod-based
sandbox the same list maps to extra containers in the task pod.

## Customization & Contribution Guide

A new sandbox implementation must preserve exact command argument boundaries,
context-aware external operations, bounded cancellation cleanup, and positive
absence checks. Put it in a concrete package with an explicit constructor and
command switch, then test partial startup, live evaluation, idempotent stop,
resource ownership, and bridge-facing capabilities. Update the supported
reference and operational prerequisites. Do not add registration, discovery,
factories, reflection, DI, or generic plugins.
