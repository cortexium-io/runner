# Fixed browser runtime

The operator can select `delivery.browser_image` as a full `sha256:<64 hex>`
image ID on its existing Docker engine. Runtime labels bind contract 2,
Playwright 1.64.1 and the exact embedded driver/fixture bytes. Startup independently
hashes the files; a label alone cannot turn stale scripts into current proof.
An absent or different image stops readiness before model work. Workflow tasks
cannot pull, build or replace this dependency.

This image uses the pinned Linux/amd64 Playwright Noble base in `Dockerfile` and
the integrity-locked npm dependency. Its Chromium headless shell is the executable
matched to that package. Linux uses Playwright's ordinary process model; the
single-process flags remain macOS-specific. Browser Node is part of the immutable
image and does not change the host's Node 24.21.0 frontend build contract.

For an authorized operator bootstrap, run `node prepare.mjs /absolute/new/context`
from this directory. Build that context on the configured engine, supplying the
printed digest as `--build-arg RUNNER_BROWSER_DRIVER_SHA256=<digest>`. Preserve the
reviewed runtime image and its full resulting image ID, then qualify that ID with
`TestDeliveryPlatformBrowserContainerReadiness` under a separate private
`-qualification/` policy. Install/select it only after that proof passes. Changes
to a baked script require a new image and qualification.

The mobile-evidence source update also binds `browser_experience.mjs` in the
driver digest and startup hash check. It exports actual viewport PNGs separately
from full-page diagnostics. Fixed state names are capped at 160 images within
the same byte ceilings; the declared review artifact remains at most 16MiB.
The new runtime's disposable browser proofs do not qualify the complete mobile
process: native Safari remains stopped pending safe host isolation and trusted
input proof. Do not switch the installed pilot or use this runtime to imply
native/physical Safari acceptance.

The browser container is nonroot with a read-only root, no added capabilities,
no new privileges, private IPC/shared memory and bounded tmpfs. It has no host
mounts, socket, credentials, exposed ports or external network. Readiness uses
network `none`; candidate qualification joins only the exact owned candidate's
network namespace, which also has network `none`. A synthetic loopback TLS proxy
exercises the existing origin/forwarded-header contract. The candidate retains
its existing isolated database/report mounts; the browser cannot mount them.
The proxy assigns one fixed synthetic client address per browser profile, so
independent desktop, mobile and support profiles exercise separate per-client
rate budgets. Requests within one profile share the same budget.

The profile fixes 2 CPUs, 2 GiB memory, 256 PIDs, 256 MiB shared memory, 256 MiB
temporary storage and 128 MiB work storage. Evidence is at most 64 MiB, with a
128 KiB receipt and 8 MiB per screenshot. The existing 20-minute phase deadline
and workflow/model limits remain unchanged. Inputs travel as bounded JSON on
stdin; a fixed entrypoint writes a strict tar archive on stdout before tmpfs
disappears. A successful container exit proves transport; Go independently
validates every acceptance obligation and runtime identity.

Docker inspection records the enforced profile before start. Creation, dispatch,
settlement and cleanup use the frozen returned container ID plus run, attempt
and image identities. Foreign resources remain preserved. Failed cleanup is
uncertain and takes precedence over source correction. The opt-in
`TestDeliveryPlatformBrowserContainerCancellation` cancels only after observing
the owned container running and verifies exact cleanup without replay.

The synthetic fixture covers both engines and 320/360/390 widths, trusted
emulated touch, exact rectangle paint, no page pan, Undo/Redo/reload and fitted large-board
editing. It rejects clipping, internal overflow, wrong entry scroll, unreachable
controls, single/wrong corners, wrong paint and ignored touch. These are runtime
maintenance fixtures, not product qualification or physical-device evidence.
Desktop geometry checks distinguish restored vertical scrolling from horizontal
overflow. Mobile still requires the board and primary controls in the viewport.
Failed layout assertions pass the measured viewport, frame, board and control
bounds to the correction role, with scroll sizes and minimum clue font size.
Failed Shikaku edits also retain intended and trusted native pointer coordinates,
hit cells, before/after board bounds, page scroll and exact expected/painted cell
counts. Passive observers retain at most 32 events and are removed after each
gesture. Missing samples and truncation stay explicit; diagnostic evidence does
not waive touch, paint, Undo/Redo or restored-state assertions.

Failed board-admission requests retain the browser's bounded network error
code and any observed HTTP status. For a Chromium POST that reports HTTP 200
followed by `net::ERR_ABORTED`, the driver requires an interactive board on
the exact canonical URL. Fresh entry also requires HTTP 200 for the main
document. Reload and Resume require matching saved and visible state. The
receipt records resolved aborts. A missing or changed board fails qualification,
and the driver never replays a puzzle request. The fixture checks interruptions
before and after headers and a restored board with an incorrect visible edit.
After New generates a puzzle once, the verifier observes admission on the
canonical route. A 429 can reopen that exact URL once after the bounded
server-directed delay, without issuing another New request.
