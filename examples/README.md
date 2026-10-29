# Runner examples

Each directory contains one example and its configuration:

| Directory | Demonstrates |
| --- | --- |
| [demo](demo/README.md) | Two deterministic steps, typed output handoff and mixed YAML/JSON configuration. |
| [codex](codex/README.md) | Input-only Codex execution followed by a deterministic consumer. |
| [workspace](workspace/README.md) | A supplied Go change request followed by a fixed Go check. |

Select an example's `instance.yaml` with `serve --config`. Examples are not loaded
automatically. Test fixtures live under the repository-root
`testdata/` directory. Examples can be edited without changing unit-test fixtures.
