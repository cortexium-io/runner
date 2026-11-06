# Frontend report qualification

These isolated Vitest fixtures prove canonical file identity, an actual assertion
failure, explicit skip rejection, an omitted selected file and an unexpected file
matched by Vitest's CLI substring filter. They do not inspect any product source.

Run the owning Go test with `RUNNER_FRONTEND_NODE` and
`RUNNER_FRONTEND_MODULES` set to the installed configured Node 24 executable and
frontend dependency directory. The test copies these sources to disposable
scratch. On macOS it uses `/var/tmp` to exercise Node/Vite's `/private/var`
canonical identity. Normal Go tests skip this runtime-dependent qualification.
