# Approved Jack's Break release

This first release operator implements the qualified Jack's Break PR/image/report
and migration 0017 contract. It reuses Runner's process, decisions, storage and
executor lifecycle. Other applications need their own qualified implementation.

`prepare → [approve merge] → merge → backup → [approve production] → stop → migrate → pin → deploy → verify`

Configuration rejects missing gates, mutable receipt bindings, starting midway,
task collections and automatic retries. The restricted intake cannot decide a
gate. `runner_decide` consumes the exact saved decision ID. Merge approval does
not approve production. Production approval excludes database restoration.

Copy `release.json` and `roles/` into the private project's configuration. Set
instance `release.command` to a fixed argv invoking `runner-release --config
/private/operator.json`, locally or over the existing strict SSH connection.
The operator policy fields are illustrated in `operator.example.json`.

```sh
go build -o /private/bin/cortexium-runner ./cmd/cortexium-runner
go build -o /private/bin/runner-release ./cmd/runner-release
```

Use the same reviewed commit on both hosts. Keep journals in a private 0700
directory and policy/environment/backup files mode 0600, outside Git. Model roles
never receive release credentials or select commands. The operator uses normal
GitHub/SSH authentication and the existing Compose MCP over stdio. Changing the
instance operator command requires restart. No additional scheduler is installed.

Preparation checks the existing qualification receipt chain, packaged executable,
remote report/image, exact PR head/base, generated ownership, strict/admin-enforced
branch protection, healthy container and data mount. Unchanged expensive proof is
reused. The protected merge requires the approved head; the merged source tree
must equal the qualified tree before that image can be deployed.

Before production approval, Runner verifies a consistent off-host backup, retains
the private environment, checks migration input and validates the exact main commit.
After approval it stops only the observed Games container and takes another backup
of stopped data. Migration uses the immutable image, the observed live environment,
resource bounds, no network, dropped caps and a read-only container filesystem.
The accepted host Python backup helper is reused; no Python source is introduced.

Fresh before/after snapshots must retain application tables and rows, remove only
the three retired stock tables, preserve old migration rows and add the exact 0017
checksum. Integrity, foreign keys and migration-lock identity/time are checked.
Pinning changes only image/report environment bindings. Deployment uses the exact
merged commit and unchanged controller policy. Live verification checks the fresh
service/container, executable hash, HTTPS health and readiness.

Every action retains its request identity, predecessor, policy hash and durable
key. Unsafe intent is written before dispatch. Lost GitHub acknowledgements are
inspected without repeating the mutation; Compose reconnects use the same request
and job IDs. A failed deployment that already started remains uncertain and needs
owner inspection. Client cancellation does not prove remote settlement.

Migration failure leaves Games stopped with recovery files retained. Database
restore, shared Docker restart, QA-helper cleanup and sibling changes require
separate authority. Operator journals/backups have separate retention from Runner's
run database; do not automatically remove recovery material.

The isolated model-free fixture covers both approvals, restart/rejoin, lost merge
acknowledgement, single mutation dispatch and migration failure blocking deployment.
Fixture approval grants no production authority. Live verification pauses at the
real gates until the owner approves their exact receipts.
