# Public Cortexium browser runtime preparation

This is operator-maintenance input only. The local candidate route remains
UNQUALIFIED/unactivated; this directory grants no build, pull, install or intake.

`prepare.mjs NEW_OWNED_CONTEXT PUBLIC_VENDOR_ROOT PUBLIC_SDK_NODE_MODULES`
constructs a new ordinary-file-only context from positively pinned Node26.13.1
linux-x64 and full Chrome for Testing158.0.8127.18 linux64 binaries, fixed Runner
JS, and Playwright/core1.65.0. It validates executable SHA256 and package versions,
normalizes installed modes and records every context file's hash/size. The package
lock pins public SDK integrity. Credentials, host state and App source are absent.
The full CFT ZIP SHA is an observed downloaded identity; no vendor checksum claim
is inferred. Retain original official download metadata and measured identities
with qualification evidence.

The Dockerfile uses one exact public MCR image digest plus COPY only. It needs no
RUN/package install/Go/Genny/Node build and works with the selected installed
legacy Docker builder; no buildx/host setup is installed. Qualified selection
would bind the actual resulting immutable Linux/amd64 image ID, fixed labels,
Node/full-CFT versions, executable/three-file driver SHA and ordinary config.
A mutable runtime tag is never authority. The actual version-only smoke does not
prove Chrome startup, nine UI journeys, isolation or resource/cleanup fit.

Candidate derivation is separate: NEVER-STARTED staging+verification containers,
fixed executable/manifest copies, Docker commit and actual file readback. It does
not use FROM a local config ID, a registry publication, source Dockerfile, source
commands, arbitrary mounts or operator secrets. No App source rebuild occurs.
The first actual derivation stopped at copied metadata validation. Its exact
owned derived image remains after nonforced cleanup timed out; do not replay
removal/qualification until the owning engine prerequisite is safely resolved.


Readback refusal also drains only the remaining private32MiB-bounded trusted
archive, within the earlier of the original context or a five-second refusal
deadline. Owned local pipe fixtures prove EOF completion, overflow, cancellation
and deadline settlement while preserving the original metadata error. They
prove no daemon-side cleanup or cause for fixture01's retained image.

Runtime admission keeps the declared combined memory/CPU/PID/storage bounds and
positive ownership cleanup. The selected engine's cgroup-v1 and zero-active-swap
restriction is explicit; missing/changed metrics or active swap blocks without
host changes. Actual App/browser adversarial/lifecycle/peak proof remains required.
