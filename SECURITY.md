# Security policy

This Runner is a local, single-operator automation tool. It is not a sandbox,
credential broker, hosted service, or security boundary around an AI harness.

## Supported code

Security fixes target the latest release and the current `main` branch. Older
releases may require upgrading rather than receiving a backport.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting flow:

https://github.com/cortexium-io/runner/security/advisories/new

Include the affected version or commit, impact, reproduction steps, and any
suggested mitigation. Do not put credentials, tokens, private repository
content, or personal data in a public issue.

## Local execution model

- The operator chooses the GitHub Project, repository, installed executables,
  trusted model/skill/time settings, environment, and operating-system account.
- GitHub CLI uses the operator's existing authentication. Codex CLI, Claude
  Code, and Pi CLI use their own existing authentication and configuration.
- Every role's execution policy is Runner-owned and suppresses user/project
  harness policy, plugins, ungranted MCP servers, browser integrations, delegation, and
  approval settings with native flags. Codex and Claude roles default to
  native isolation; host access is an explicit per-role choice for implementers
  and reviewers. Runner still owns their workspace identity, structured-result
  contract, repository integrity checks, and publication authority.
- Sandboxed Codex and Claude implementers and reviewers receive a default bounded
  development profile. Package commands remain inside the native filesystem
  sandbox; implementers receive network access only to the npm registry and
  loopback. Their sandbox filesystem profile permits the assigned workspace,
  minimum system runtime files, and the implementer's npm cache rather than
  ambient operator-home reads. Runner injects a pinned headless Chrome MCP with a temporary
  profile, mock keychain, three-tool surface, disabled telemetry/CrUX, redacted
  headers, disabled external name resolution, and URL patterns limited to
  localhost and 127.0.0.1. The exact MCP process is still a trusted local
  principal outside the harness shell sandbox. Runner starts it from a separate
  mode-`0700` host-owned directory with temporary npm configuration and a
  reusable private package cache that are not writable by the harness;
  a role can explicitly disable the default profile.
- A Codex role may additionally allowlist named local stdio MCP servers. Runner
  inspects the operator catalog from a private neutral directory in isolated
  mode, reconstructs only those configured server definitions, rejects inline
  environment values, and auto-approves their tools because role launches are
  non-interactive. The MCP subprocess is a separate trusted principal outside
  the Codex shell sandbox and can have the host access granted by its command;
  the operator must inspect and trust that definition. Doctor derives required
  MCP checks from role grants automatically. Claude and Pi role MCP grants are
  rejected until Runner has an equivalent isolated injection boundary.
- Project card content, issue bodies, and PR feedback are supplied as task
  context. Native installed skills and automatic repository-instruction
  discovery are suppressed for planner launches; Runner injects its pinned
  embedded bundled role instructions. A reviewer inspects the assigned
  repository from a neutral workspace and an implementer works in its assigned
  worktree. Sandboxed Codex uses scoped permission profiles with minimum
  runtime reads and only the assigned repository/worktree. Sandboxed Claude
  denies reads from the operator home directory except for the exact assigned
  repository/worktree and the implementer's npm cache; required system paths
  remain readable. Repository mutation during review or changes outside the
  implementation worktree fail integrity verification. After implementation,
  Runner materializes the exact candidate commit in a new private detached
  checkout that the implementation sandbox was never granted and gives only
  that checkout to Agent QA.
- Issue discussion enters an assignment only when its author matches the
  GitHub account currently authenticated in `gh`. Other issue participants
  cannot add instructions to an already approved Runner action. Runner-authored
  QA comments use the same authenticated account and remain idempotent context.
- For a Pi invocation, Runner writes a mode-`0600` temporary TypeScript
  extension containing only the result schema and result-tool definition. Pi
  disables discovered resources and explicitly loads only this result channel;
  Runner removes the file after the invocation. Runner also enables Pi's
  experimental strict JSON-schema sampling for its standard built-in tools in
  that child process without changing the operator's global Pi configuration.
  Pi JSON progress events are discarded while
  streaming, and only bounded result-tool start/end events are retained for
  validation.
- `doctor --fix` is an explicit local write operation. It can install missing
  embedded Runner role skills and replace differing copies for harnesses in the
  selected config, but it does not modify harness settings.
- If a configured harness cannot complete a task because a permission, tool,
  login, or other capability is unavailable, it should return `blocked`. Runner
  retains its reason and attempted-check evidence locally, writes only a fixed
  Runner classification to `Runner Result`, records the originating agent lane,
  and moves the card to `Blocked`. A retry preserves that bounded result as
  historical context but requires the new invocation to re-check environment
  and capability claims.
- Implementation starts in a separate Git branch and worktree. Codex and Claude
  Code implementers and reviewers use sandboxed access by default. Pi lacks a
  native OS sandbox for these roles, so Pi implementation and review require
  explicit host access. The operator is responsible for trusting and qualifying
  every selected harness and any role granted host access. Runner verifies the
  task worktree and active checkout after every invocation.
- On macOS and Linux, Runner requires the workspace-write root to be owned by
  its effective user with mode `0700`, traverses its ancestors without
  following symbolic links, and refuses locally replaceable path components.
  Private identity, quarantine, reuse, and cleanup operations revalidate that
  root and reject symlinked Runner state. Root-owned system ancestors and
  sticky temporary directories are accepted. This boundary addresses
  substitution by other local users; it does not protect against another
  process running as the same account, and no equivalent guarantee is provided
  on other operating systems.
- Commands that launch, probe, or edit role policy resolve an explicit operator
  config or the repository's `.cortexium/runner.json` default. On macOS/Linux
  Runner reads it without following links and requires a regular, single-link
  file owned by the effective user, not writable by group or others, and outside
  every implementation-workspace root. Other platforms fail closed. A
  repository-local config and `.gitignore` are not authorization boundaries.
- Repository snapshots use literal NUL-delimited Git output and pin the current
  worktree's administrative directory, index, HEAD and its loose or packed
  symbolic-ref chain, common config, and, when enabled by
  `extensions.worktreeConfig`, its per-worktree config path (including its
  absence) while semantic state is collected. The fingerprint covers index
  object, mode, stage, assume-unchanged, and skip-worktree state plus the
  exact current worktree registration and Git-directory relationship; unrelated
  worktree registrations are excluded to avoid volatile integrity failures.
- The snapshot also hashes ignored or concealed `.gitignore`, `.gitattributes`,
  and `.gitmodules` files; repository-local ignore/attribute and sparse-checkout
  files; alternates, graft and replacement metadata; and every default Git hook
  name. Indexed gitlinks always contribute their recorded object and filesystem
  state. Initialized submodules recursively contribute HEAD, index/status,
  protected metadata, and nested submodules without fetching or initialization;
  missing or symlinked indexed paths and non-empty deinitialized submodules fail
  closed. Agent QA compares the private candidate checkout, retained
  implementation worktree, and active checkout
  before and after review and does not continue to Runner commit, push, PR
  creation, or recoverable-worktree cleanup after drift.
- This no-follow snapshot boundary is enforced on the macOS/Linux release
  matrix. Snapshot traversal and payloads are bounded by the configured entry,
  individual-file, and aggregate limits. Git and GitHub subprocess output,
  Project pagination and collections, pull-request feedback, and public-intake
  mutations are capped before unbounded buffering or collection growth. Public
  intake runs after recovery, reconciliation, and approved execution, and an
  intake-local failure does not discard those trusted results. Ordinary
  snapshots continue to observe repository controls and external effective
  configuration. Candidate construction is the narrower privileged exception:
  it pins the linked-worktree administration, index, and object store, scrubs
  inherited Git selectors and external configuration, disables executable Git
  behavior, and stages literal worktree bytes without clean filters. After QA,
  Runner replays the immutable accepted tuple, fetches the approved base, and
  pushes the accepted commit OID to the tuple's full destination ref through
  the same pinned, configuration-scrubbed boundary. A literal GitHub URL,
  explicit credential helper, redirect controls, and exact refspec prevent
  repository remote, push-default, hook, signing, filter, and URL-rewrite
  configuration from selecting different publication behavior.
- The default workflow has Runner construct a clean committed candidate before
  agent QA, then push and create a pull request after acceptance. This is
  workflow behavior, not a claim that the harness is unable to perform those
  actions itself.

## GitHub authority and concurrency

`Runner Approval` is an operator-authenticated assertion, not a public content
digest. Runner creates one HMAC key per `runner_id` under the private local state
directory and stores it with mode `0600`. The assertion binds Project and item
identity, exact content, repository, role, planning metadata, and runtime values
that can steer execution, publication, completion, or cleanup, including result
history supplied to a later agent. Changing a bound value invalidates authority
and returns the item to assessment. Runner-authored result changes are parked in
assessment and re-authenticated before the destination lane becomes executable.
The assertion records what was approved; it does not establish that the task is
safe or correct. Losing the local key invalidates existing approvals; restore
that key or reassess and approve the work again.

The process lock prevents two local Runner processes from polling the same
Project on one machine. GitHub Projects do not provide an atomic cross-machine
claim, so running one Project from multiple hosts is unsupported.

## Data and retention

Runner stores an append-only local attempt history in the operating system's
user configuration directory, or in `CORTEXIUM_RUNNER_STATE_DIR` when that
variable is set. Each attempt has start and completion records, with additional
start and completion records for fixed-name execution stages. Attempt records
can include the Project item ID and title, role, harness, configured model and
reasoning level, timestamps, outcome, failure class, retry disposition, concise
structured work and verification summaries, and token or cost counters reported
by the harness. Stage records are limited to attempt identity, fixed stage name,
timing, outcome, recovery enums, and reported usage. Runner does not put prompts,
transcripts, command arguments, raw failure diagnostics, raw harness responses,
or free-form stage payloads in this store, and idle polls do not create records.
An abrupt stop can leave an unfinished attempt or stage record.

The state directory is created with mode `0700` and the history file with mode
`0600`, but anyone with sufficient access to the local account or filesystem can
read it. Task titles and structured summaries can themselves be sensitive.
There is currently no automatic retention limit or rotation. Run
`cortexium-runner metrics` to see the exact `History` path. To clear it, stop
Runner and delete that one history file; the next attempt recreates it. A
configured rolling admission budget uses this same history, so deleting or
relocating the file also resets its evidence. Token and cost ceilings fail
closed when an attempt is unfinished or lacks the corresponding harness-reported
counter, and harness-time ceilings fail closed for unfinished attempts. Runner
refuses new agent claims if configured history cannot be read,
contains malformed records, or becomes incomplete after a metrics write
failure. Admission ceilings do not cancel attempts already in flight and
therefore are not hard per-invocation spend limits.

Runner also writes concise result or blocker text to configured GitHub Project
fields and prints execution results to the terminal. It retains implementation
worktrees only while work is unpublished and resumable, then removes them after
PR publication while retaining the task branch. It does not generate patch
handoffs or evidence manifests. Branches, open worktrees, GitHub issues, Project
fields, pull requests, and the local metrics history can contain repository or
task data.

Subprocesses run under the operator's account. Their own history, cache, log,
telemetry, and retention behavior belong to GitHub CLI and the selected AI
harness.

Raw stdout and stderr from a failed AI harness are local diagnostics and are not
written to GitHub Project fields. Provider capacity, session limits, retry
disposition, and retry timing are trusted only when parsed from an allowlisted
adapter-owned structured error object. The same phrases in model output or raw
stdout/stderr have no recovery authority. Project result text is reconstructed
from fixed, bounded Runner templates plus allowlisted enums and structured retry
fields; schema-valid model summaries, blockers, review evidence, prompts,
tokens, session data, and stack traces remain local.

Structured-result compatibility is local and representation-only. Runner may
unwrap one whole-response JSON object fence and remove the exact stray
top-level JSON Schema residue `"type":"object"` before strict decoding. Every
other unknown field, missing substantive field, malformed value, or semantic
contract failure is rejected without another model invocation. Model-authored
content never gains authority to rewrite conclusions, blockers, verdicts,
evidence, or recovery state.

Workspace integrity verification runs after every terminal harness result,
including command failure. If both execution and integrity verification fail,
local Runner output retains both causes while the integrity classification
blocks workflow continuation and remote diagnostic publication.

## Known limitations

- An approved task can still be malicious, ambiguous, or overly broad.
- Models and tools can make incorrect or destructive decisions.
- Host-access implementer and reviewer harnesses can read or change resources
  available to the operating-system account. Repository integrity checks do not
  contain external filesystem, process, network, browser, or credential effects.
- Native harness isolation is least-privilege policy, not a complete credential
  boundary. Sandboxed Claude roles retain access to required system paths, and
  the harness process itself still uses its existing authentication outside the
  child-command sandbox.
- The same-machine lock does not prevent a second host from racing a claim.
- Admission budgets are local to one Runner history and are not distributed
  quotas across hosts or provider accounts.
- Result summaries, branches, worktrees, and pull requests may expose
  information to anyone who can read the Project or local files.
