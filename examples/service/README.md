# macOS service ownership

`serve` remains the only Runner owner. The OS starts and restarts that same
executable; it does not dispatch work or add another coordination loop. A user
LaunchAgent keeps an authorized local pilot available after login/reboot instead
of relying on a terminal session. It runs as the account owning the existing
private configuration, authentication and state. It introduces no root service,
new credentials, remote listener, task, approval or execution allowance.

Use this example only when installation/service operation for the selected pilot
is authorized. Copy `macos.plist` to the owning user's `~/Library/LaunchAgents/`,
choose a unique label, and replace every example path with the qualified binary
and existing private pilot. Create the private log directory first. Keep the
installed plist outside Git; the example contains no host identity or secrets.

Before the first cutover, verify that no run is active, stop the previous owner
gracefully, and preserve a consistent database/configuration/token/binary backup.
An OS lock still rejects two owners of the same state. Validate the actual plist
with `plutil -lint`, then use `launchctl bootstrap gui/UID /absolute/plist` and
confirm the ordinary Runner status against that state and endpoint. Test a
controlled service restart only while no work is active; verify history and the
owner token are preserved. Normal service restart reconciles existing jobs and
never grants another attempt or repeats a release approval.

For an intentional stop or binary/configuration cutover, use `launchctl bootout`
for this label first. `KeepAlive` otherwise restarts an owner that exits. After
the qualified update, bootstrap the same plist and recheck status. Do not start a
second manual owner alongside it. Keep the OS-owned service logs private and
inspect them if status is unavailable; they are operational diagnostics, not a
second Runner event store. LaunchAgents start in the user session after login;
they do not promise availability before login or while the machine is asleep.
