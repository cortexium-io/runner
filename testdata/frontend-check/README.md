# Frontend report qualification

These isolated Vitest fixtures prove canonical file identity, an actual assertion
failure, explicit skip rejection, an omitted selected file and an unexpected file
matched by Vitest's CLI substring filter. They do not inspect any product source.
The imported-fixture journey also proves that literal JSON imports from `docs/`
and `testdata/` execute in scratch without copying unrelated files or changing
the source. The Go boundary fixtures reject missing, outside-root and symlinked
JSON inputs before runtime preparation.

Run the owning Go test with `RUNNER_FRONTEND_NODE` and
`RUNNER_FRONTEND_MODULES` set to the installed configured Node 24 executable and
frontend dependency directory. The test copies these sources to disposable
scratch. On macOS it uses `/var/tmp` to exercise Node/Vite's `/private/var`
canonical identity. Normal Go tests skip this runtime-dependent qualification.
