# Bounded routine operations

Fresh complete processes may set `infrastructure_retries: 1` (supported range
0–2; omitted means disabled). This permits one extra attempt per step visit only
after the trusted release adapter proves a settled, pre-dispatch connection
failure in readiness or isolated qualification. Every attempt still consumes
the process's original `max_attempts` and its normal deadline. The policy does
not change model limits, source correction rounds, benchmark thresholds or
protected approval gates. Existing runs retain their frozen definitions.

The operator already reconnects once when a command positively failed before
remote dispatch. A disconnected call rejoins its saved operation identity;
uncertain effects never authorize a replacement. `run` and `result --wait`
follow this self-reconciliation within the attempt deadline. An explicit unknown
operator response, exhausted allowance, owner decision or other unresolved
condition remains visible to the coordinator.
An identified deadline of the read-only source archive call also rejoins the
same saved readiness attempt. Ordinary Git exits and cancellation remain visible.

Source adoption, local readiness preparation and local publication preparation
have workers owned by the saved attempt, rather than by an individual observer.
Short observation calls join authentication, source materialization, archiving
and exact-ref publication preparation under the original deadline. Saved phase
markers distinguish local work from operator dispatch and a started push. Stop
settles owned work; it cannot start another push. A confirmed remote target
retains the known candidate, while an unacknowledged push whose target is still
old remains uncertain. These rules preserve effect ownership and grant no retry
allowance or release approval.

After a source writer settles, capture retains an owned, bounded snapshot across
short observation calls. Observations join the same in-flight capture within the
original attempt deadline. A positive call deadline or an occupied capture lease
also rejoins that capture with a short recheck. Git children inherit the lease;
source/checkpoint drift and ordinary Git errors remain actionable. The patch is
published only after capture completes. This uses the existing attempt and
storage limits and preserves the original worker result; it grants no new model
visit or approval.

The private delivery policy may enable:

```json
"retention": {"raw_archives": 2}
```

Before new readiness admission and each fresh qualification worker, this collects
only settled, owned, superseded
`source.tar.gz` files. It retains the configured 2–8 newest independently
reproduced packages, current and previous successful release packages, and
pending release/recovery dependencies. It authenticates each attempt against its
private operator journal, captured policy and independent reproduction proof;
then takes its worker lease and verifies the archive hash before deletion.
Symlinks, changed bytes, missing ownership and active workers are preserved.
Deletion intent and completion are retained under the private operator state's
`archive-retention/` directory. All run records, journals, reports, Git history,
compiled artifacts and backups remain intact. The existing 8 GiB limit and
admission reserves still apply; protected dependencies can legitimately prevent
further admission.

The same opt-in permits reuse of a verified build package for the exact same
reviewed head, base and publication with unchanged operator inputs. Retention
settings alone may differ. Runner copies hash-verified build artifacts into the
new attempt and records their origin; the copy has no dependency on an earlier
disposable archive. It independently reproduces the frozen executable again and
runs qualification, rehearsal, browser checks and approvals normally. Old runtime
reports and approval evidence are never copied as new proof.

Workspace cleanup parks known permanent integrity failures once with
`cleanup_blocked: true`, preserving the workspace and the failed run. Ordinary
I/O failures retain the existing delayed cleanup retry. After inspecting and
resolving the condition, an owner can request:

```sh
cortexium-runner retry --run RUN_ID --cleanup-only --request-id STABLE_ID
```

This retries proof and cleanup without changing the terminal result, consumed
attempts or limits. It cannot be combined with an execution allowance or profile.
The restricted intake still exposes only submission, result and status tools;
cleanup maintenance belongs to the owner.
