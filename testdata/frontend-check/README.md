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

Each helper visit first runs the configured Node with the exact immutable
`vue-tsc/bin/vue-tsc.js --noEmit --pretty false --project tsconfig.json` entry,
then the selected Vitest files within the same eight-minute deadline. Complete
source type errors still run Vitest, retaining both kinds of failure for one
correction. Tool, project, execution and evidence gaps remain blocked. Compiler
output and Vitest JSON share the existing 8 MiB raw budget; the encoded artifact
remains limited to 16 MiB and domain/stdout summaries to 480 bytes.

The real-runtime fixture includes a valid Vue component, runtime-passing TS2322
and Vue type failures, and combined type/assertion failures. It verifies the
configured compiler identity and unchanged source configuration/runtime bytes.
The owning Go fixtures cover missing tools/configuration/dependencies, runtime
failure, cancellation/deadline and overflow without executing product code.
