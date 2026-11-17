# Ordinary frontend reference and project verification

This example stages an exact owner brief through the existing fixed
`command/shared/workspace-read` executor. The command reads the immutable `{goal}`
file and writes one readable `owner-brief` JSON artifact containing the original
UTF-8 text, byte count, SHA-256 and an explicit reference-only label. URLs and
quoted instructions remain data; the command fetches nothing and produces no
candidate verification, image evidence or approval. Validation failures return
bounded diagnostics and a strict `domain.json` containing only `outcome` and
`reason`. Existing output files are never overwritten.

An operator prepares an overlay without touching the live configuration:

```sh
node examples/frontend-reference/configure.mjs \
  --instance /absolute/current/instance.json \
  --node /absolute/qualified/node \
  --native-binary /absolute/qualified/codex \
  --root /absolute/new/private/staged-root
```

Stage near the existing configuration so the complete project references fit the
unchanged 16 KiB loader bound. The builder retains existing definitions by
reference and copies the small stager and launcher once to read-only,
hash-recorded tool paths. It preserves other projects, routes, defaults, source
selection, storage, concurrency and native/release gates. An explicitly selected
global Codex binary is recorded separately as a cold pin; qualify its protocol
and legacy permissions before installation. Models and effort stay unchanged.
It adds `stage-frontend-reference`, whose
only input is `request: text` and whose output is `review_evidence: artifact`.
Submit the exact same request text to this process and the complete frontend
process, using stable separate request IDs. Rejoin normal results and pass the
returned artifact ID; restricted intake never receives filesystem paths, commands
or artifact-store access. The stager uses no source ref or checkout.

The protected complete frontend validator requires the existing process ID
`deliver-planned-frontend`. The overlay therefore selects a cloned definition
under that same ID for future submissions. Original files, original roles and
already frozen runs remain intact. New planner/owner/source-review/mandatory
review role IDs retain their existing settings, models and budgets; future prose
judges the current request rather than unrelated historical defect IDs. The
protected suffix and its bindings stay unchanged.

The connected source prefix runs the original focused checker unchanged as
`focused-check`, then the fixed `frontend-project-check` profile against the
authenticated current source and patch. A narrow Runner test-author card must
create `frontend/verification/project-experience.mjs` and meaningful assertions;
maintenance supplies no product verifier. The fixed launcher invokes that entry
in a separate Node child with `--source`, `--scratch`, `--patch`, and `--reference`.
The script writes `project-result.json` with status (`passed`, `failed`, or
`blocked`), reason (at most512 UTF-8 bytes), optional summary (at most4096 bytes),
and files (up to64 unique flat regular JSON/PNG names). Each file is bounded to
8MiB and the compressed canonical archive to16MiB. Encoded launcher metadata is
bounded to128KiB; producer and decoder share a16MiB+256KiB expansion bound with
exact encoded metadata and USTAR header/padding accounting. Invalid candidate
metadata retains a repair diagnostic. The script owns browser/context/server
cleanup in `finally`; the worker owns its scratch/process lifecycle. Runtime
paths are fixed `RUNNER_PROJECT_*` environment values. Source is read-only; the
verifier's explicit network grant is broad, not a loopback-only security claim.
Go checks reuse the focused profile's existing `GOCACHE`, or the configured pinned
Go runtime's normal shared cache when it is unset. Only that canonical cache
directory receives a runtime write grant; sibling paths, source and coordinator
state stay protected. No cache is copied into source or retained evidence.

The launcher catches syntax errors, exceptions, unsuccessful exits and invalid
contracts. It retains bounded diagnostics and source/patch/owner/verifier/runtime
identities. Repairable script defects return `changes_requested`; invalid owner
authority blocks. It does not certify the assertions' adequacy. After the existing
measurement step, a separate native image role personally inspects all declared
project images and writes a hash-bound judgment. This role explicitly uses native
tools, lean context, no skills, read-only source and no network, with the existing
review model/effort/budgets.

The fixed assembly step retains the original focused JSON byte-for-byte as
`focused.json`, a compact project manifest with canonical artifact identity/SHA,
and `project-review.json`. It requires current matching patch/owner/verifier/
archive identities, exact inspected image names and an approving judgment with
no project observation gaps. The original project archive remains canonical;
assembly also retains exact bounded report/PNG components under generated names,
with original-name/component/hash/size mappings and the exact project launcher.
Focused/project failures still
produce the canonical checks artifact for the existing correction owner, without
pretending a later image review ran. Original eight-input owner/review contracts
remain unchanged. Mandatory review personally views its sealed nine-family
images and reads the independent supplemental judgment without claiming to have
viewed those supplemental images itself.

An owner may explicitly recover an old compact-aggregate handoff with
`recover-evidence-handoff`. It admits only the hash/size-bound original archive
to named future source/review roles through protected read-only inputs. The
blocked owner must have left the accepted source unchanged; the saved check and
archive must identify that same candidate and declared owner reference. Recovery
consumes the next use of the active correction phase, retaining the blocked
attempt, every earlier grant and the original inputs. It grants no allowance or
approval; the new owner and all fresh downstream checks/reviews remain ordinary
Runner execution.

All configured focused checks, broad nine-family qualification, authenticated
candidate export, independent image judgment, native Safari gate and protected
release decisions remain mandatory. In particular, this intake does not produce
controlled ad captures or qualify native Safari. The future project verifier and
image role must supply current request-specific observations; missing requested
observations cannot approve. Project approval covers its declared phone/desktop
scope, while native Safari coverage remains a separate downstream gate. The
historical fixed-report staging process remains available unchanged.

Focused qualification:

```sh
node --test examples/frontend-reference/*.test.mjs
make check PACKAGES=./examples/frontend-reference
```

The Go fixture loads the full protected process, checks frozen definition size,
and executes only the new fixed Node profile in a disposable macOS sandbox. It
proves exact text/digest, retained provenance, cleanup, idempotent replay and
same-project artifact consumption with a deterministic reader. It performs no
product checks, model calls or release actions. Set
`RUNNER_FRONTEND_REFERENCE_NODE` to the selected pinned Node runtime and
`RUNNER_FRONTEND_REFERENCE_CONFIG` to the prepared overlay to validate and qualify
its exact fixed profile before installation. Installation remains operator-owned.

Opt-in source qualifications use disposable synthetic HTML, not product code:

```sh
RUNNER_FRONTEND_REFERENCE_CONFIG=/absolute/staged/config/instance.json \
RUNNER_SOURCE_SHARED_BROWSER=1 \
make check PACKAGES=./internal/runner/execution \
  TESTS='^TestSourceProjectFixedLauncher'

RUNNER_SOURCE_NATIVE_CODEX=/absolute/qualified/codex \
make check PACKAGES=./internal/runner/execution \
  TESTS='^(TestSourceNativeImageProfile|TestSourceLegacyColdPin)'
```

The first qualifies exact fixed argv/launcher/assembly with configured headless
Chrome for Testing phone/desktop interactions, current source binding and cleanup.
Its image judgment is explicitly a deterministic transport fixture. The second
qualifies actual native image/source filesystem permissions and legacy app-server
readiness/permission probes with no model turn. These establish neither AI visual
judgment nor product acceptance. Darwin is qualified; Linux needs its own runtime
proof. The parent batch owns the complete model-free workflow gate once.
