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

The browser container is nonroot with a read-only root, no added capabilities,
no new privileges, private IPC/shared memory and bounded tmpfs. It has no host
mounts, socket, credentials, exposed ports or external network. Readiness uses
network `none`; candidate qualification joins only the exact owned candidate's
network namespace, which also has network `none`. A synthetic loopback TLS proxy
exercises the existing origin/forwarded-header contract. The candidate retains
its existing isolated database/report mounts; the browser cannot mount them.

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

The synthetic fixture covers both engines and 320/360/390 widths, real native
touch, exact rectangle paint, no page pan, Undo/Redo/reload and fitted large-board
editing. It rejects clipping, internal overflow, wrong entry scroll, unreachable
controls, single/wrong corners, wrong paint and ignored touch. These are runtime
maintenance fixtures, not product qualification or physical-device evidence.

Failed board-admission requests retain the browser's bounded network error
code and any observed HTTP status. A failed response body still blocks
qualification after successful headers. The fixture checks interruptions
before and after headers without replaying a board request.
