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

The prepared 12 November producer follow-up adds fixed observations in those
same profiles. It does not load repository `*.browser.cjs` or
`*.runner-browser.js` files. Sudoku/Suguru entry order and unrelated assistance
settings are exercised in both directions and across reload; explicit
Light/Dark/System appearance is persisted, with System's media response observed.
Notes in Sudoku, Suguru, Futoshiki, Calcudoku and Kakuro use two concurrent trusted
Chromium CDP contacts, passive `isTrusted` pointer counts, exact visible/saved
notes, Undo/Redo and reload. WebKit proves sequential taps only and retains an
explicit simultaneous-contact capability gap. Existing zoom, drag, cancellation and all advertised geometry journeys are
retained. The acceptance follow-up additionally observes repeated note removal,
trusted Chromium hold/highlight/unlock, drag-away and touch cancellation without
entry. WebKit’s unsupported gesture variants stay explicit.

Fresh Shikaku singleton and rectangle commits use actual two-corner/Add controls
and verify exact coverage/history/reload, an additional rectangle after an edit,
actual replacement/Undo, revision cancellation and remove/Undo. Nonogram reveals an actionable hint,
observes an unrelated save in a second page in the same browser context and
requires the revealed content and board to survive. A separately labeled
page-local write-denial fixture creates dirty local progress; a real competing
save must retain both copies, expose replacement recovery and retire the stale
hint. The hook is restored and the owned second page closed in `finally`.

The F01–F15 acceptance follow-up uses only fixed trusted observations in the same
profiles. All nine choosers are measured in light/dark portrait and landscape,
including enabled-label contrast, selected/disabled and focus/hover states,
More options and hit-tested Play/Cancel. Gradients/images unsupported by the
numeric contrast observer block that proof. Representative Sudoku, Nonogram and
Kakuro journeys cross phone/tablet/desktop widths in both directions, settings
open/closed, retained notes/selection/progress, UI Zoom/Fit, controlled 125% root
text and actual phone/landscape entry/Undo. Root text scaling is not a native
accessibility setting. Chromium separately attempts intentional trusted pinch;
contacts without an observed scale change are an explicit capability gap.

Largest boards use explicit overview/Zoom controls, distant and adjacent trusted
selection, actual scrolling without entries, applicable entry/Undo and Fit. Panned
Nonogram interior/bottom-right cells require visible, correctly mapped admitted
row/column clues or selected-cell readouts; a current-viewport crop records that
context. Chromium uses bounded trusted CDP touch sequences. WebKit positioning
cannot use wheel input in mobile mode either; unavailable positioning is an
explicit capability block. Desktop mouse/wheel observations and native taps are
never described as finger panning.

Fixed historical long-hint codes are data references only. Normal UI admission
and checked responses must authenticate their current identity and establish the
actual long condition: Nonogram’s whole row of 25 changes or a multi-step
Calcudoku/Kakuro chain with at least 12 unique supporting locations. A concise
leading instruction, initially in-viewport hit-tested Apply/dismiss, every
supporting location, expandable checked evidence, actual Apply/Undo and dismiss
without editing are required. An owned one-request abort also exercises explicit
hint failure/recovery. Short or changed samples block instead of claiming long
hint proof. Header and control measurements complement current viewport images;
independent experience review still owns visual hierarchy, discovery and meaning.

Representative completion runs through ordinary Chromium mouse UI in all nine
families. During a normal reopen in the existing fresh support profile, a bounded
observer reads the same native admission publication the candidate UI consumes.
It binds request, origin, game, canonical code/revision, definition/answer digest
and visible geometry. Fixed cell/mark, rectangle and bridge actions enter the
admitted answer; there is no solver, direct API request, storage/success injection
or arbitrary recipe input. Actual solved status and celebration must appear,
View board is activated, and saved plus visible solved progress must survive
reload. A viewport crop records that actual result. Native-image qualification
owns answer validity; these observations prove entry and completion behavior.
Missing or changed admission schemas remain capability gaps. Synthetic recipe
fixtures qualify the producer, never a candidate's nine solved results.

First-party privacy navigation and absence of optional tags at the ineligible
loopback origin are observed. Controlled CMP fixtures exercise initial, declined,
reopened and returning UI plus covered controls. Neither establishes real vendor
behavior. Current origin-only routing and the candidate's network `none` remain
unchanged. Real CMP proof additionally requires the application's canonical
`https://jacksbreak.com` origin, enabled `jacks-google` meta flag and vendor traffic.
The unenabled proposal is an owned disposable canonical-origin/TLS mapping with
only GET
`https://fundingchoicesmessages.google.com/i/pub-6251559185872178?ers=1`
initially allowed; redirects and other subresources stay denied and their bounded
host/path/status facts inform an explicit scope decision. Analytics and ads stay
denied. Required vendor dependencies, region/state coverage and isolated resource
ownership need approval before implementation. No general egress, production
identity/data, personal profile or consent bypass follows from the proposal.

Missing contracts emit `BrowserCapabilityError`/blocked scenario facts; they do
not consume source-repair loops. Proven mismatches in available UI contracts use
the existing assertion/correction path and require fresh downstream checks and
qualification. The independent experience reviewer must block absent actionable
proof, including vendor and native gaps. The follow-up is prepared separately
from active `42dc660` / browser `7eee84d4…`; no policy or live runtime cutover is
part of it. See [the continuation boundary](../../../docs/coordinator.md#producer-upgrade-and-retained-source-boundary-12-november-2026).

Earlier d563b42 dependency qualified on disposable containers (not activated):
`sha256:bca52cb69d0107c789ff0450df4c324bdbb1e2521bb1af747541d1c3afd134b9`,
driver `86a951b5228a6b98344da705e8e6c26602f4ecb007117bff271eff4cdc5f5dbd`.
Both-engine readiness passed in 106.91s and observed-dispatch cancellation in
3.04s. The retained fixture records 34 Chromium and 19 WebKit case results,
including nine Chromium completion paths, both solved-status forms and rejection
of unavailable trusted input as a capability gap. Its 31 actual viewport images
are synthetic maintenance evidence. Existing independent image-review/native
input proofs are reused; no new model fixture or native-host attempt was made.

The final acceptance dependency is prepared separately (not activated):
`sha256:9e160712f9e22b60dd703ec24de5db6f7ab76e0368807bb0bfa0fd5751c3af93`,
driver `58b183a60636d3f696bc0712c2afcaf14cb981b0e34a53b81ce9d5c706654cdc`.
Readiness passed in 168.85s under the same 3-minute watchdog; observed-dispatch
cancellation and exact cleanup passed in 2.66s. Chromium records 34 retained
scenario results plus 23 acceptance cases; WebKit records 14 retained scenario
results plus its explicit scrolling capability refusal. The 38 actual viewport
PNGs total 815,585 bytes and are synthetic maintenance evidence. Long-chain
calibration covers Nonogram, Calcudoku placement, Kakuro UI-prepared note removal,
wrong Apply, every supporting location and exact checked effects/Undo. No candidate
was executed. The earlier watchdog/uncertain settlement failure remains preserved;
read-only inspection later established its exact owned container absent.

The acceptance capture matrix has at most 160 images in the existing 46 profiles.
The final phone crop also records the tablet-to-phone transition; duplicate crops
and repeated metadata are omitted. PNG dimensions must equal the observed CSS
viewport before those dimensions are stored once. Fixed scenario witnesses have
a 12KiB allocation inside the unchanged 128KiB receipt cap. The earlier d563b42
metadata projection was 127,325 bytes; the acceptance follow-up consolidates repeated
view fields and bounded state witnesses within that same ceiling. Scenario rows
use fixed engine/game/case code tables declared in the same receipt; their last
column is the complete bounded proof. They are data, never executable recipes.
Observation array position binds to the same viewport-image index. Shared chooser
contracts are recorded once, with per-journey hashes and measured minimum contrast.
The conservative 160-image/57-scenario projection is 128,489 bytes, with 11,933
scenario bytes inside its 12KiB allocation. Projections are not candidate execution
or universal size guarantees.
Actual receipt and 16MiB artifact guards remain authoritative; insufficient
capacity fails without truncation, allowance changes or dropped obligations.

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
