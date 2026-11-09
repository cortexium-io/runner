# Writable qualification input

This disposable Go module intentionally lacks ParseSeats. Its six-case contract
is used only by the explicitly enabled native qualification. Execution tests copy
these files into their owned temporary repositories; Go does not discover this
root `testdata` directory as a normal package.
