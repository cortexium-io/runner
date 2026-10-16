# Security policy

Runner is a local, single-operator automation tool. Its authority store and
operator account must be trusted. Harness isolation limits delegated work; Runner
does not provide a hostile multi-user security boundary.

## Reporting

Security fixes target the latest release and current main branch. Use
[GitHub private vulnerability reporting](https://github.com/cortexium-io/runner/security/advisories/new).
Include affected version/commit, impact and safe reproduction steps. Do not put
credentials, private code or vulnerability details in public issues.

## Execution and authority

The operator selects the repository, Codex executable, model profiles, quality
commands and optional GitHub destination. These tools and that configuration are
trusted inputs. Codex and GitHub CLI use their existing authentication; Runner
does not install credentials or change global harness settings.

Task text and model results do not grant authority. Exact previews bind approved
content, quality and repository/base. The local store owns lifecycle and consumed
allowances. Repository instructions cannot replace Runner's execution policy or
authorize a remote effect. Role instructions are injected by the execution adapter;
there is no automatically installed skill catalog.

Only sandboxed Codex is supported. Role launches suppress ambient policy and
extensions, constrain filesystem/network access and validate structured results.
Deterministic quality commands use an empty environment with an explicit PATH,
private home/temp roots and the same native sandbox boundary. Capability checks
fail closed when the required isolation cannot be established.

Implementation worktrees, read-only review candidates and disposable check copies
have separate responsibilities. Git operations validate recorded identity and
administration, reject object redirection and avoid repository hooks, filters and
signing in privileged candidate/publication paths. Private state/evidence paths
reject symlink substitution and unsafe permissions. These controls depend on the
operator account and installed executables remaining trustworthy.

## Remote effects and recovery

GitHub delivery uses an explicit configured destination and pinned evidence.
Unknown publication/merge outcomes require reconciliation. A consumed merge
dispatch is not repeated; completion requires the exact confirmed merge receipt.
Runner-owned merge additionally requires supported strict branch protection and
ordinary merge commits. It does not modify or bypass host protections.

Backups include private code, evidence and any sensitive files already in captured
roots. They are not encrypted. Store the entire bundle privately and retain the
matching binary. Restore requires original absent paths and the same host/user;
the original external guard establishes freshness. Without it, restored authority
is inspection-only. Never edit the database, copy a stale guard to regain authority
or restart older approval state after newer remote effects.

The replacement rejects legacy configuration and earlier experimental store
formats. Preserve old binaries/state separately, reconcile remote history, then
initialize fresh authority. See the [operator guide](docs/operator-reference.md).

## Distribution

Release builds use reviewed source, pinned workflow actions, checksummed archives
and a separate protected publication job. The installer requires HTTPS, validates
the selected archive's checksum and contents, checks the executable's version and
atomically replaces only the chosen binary. It does not install a service,
migrate state or install skills. Local packaging does not publish or install it.
