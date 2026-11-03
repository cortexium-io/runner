# Runner examples

Each directory contains one example and its configuration:

| Directory | Demonstrates |
| --- | --- |
| [demo](demo/README.md) | Two deterministic steps, typed output handoff and mixed YAML/JSON configuration. |
| [codex](codex/README.md) | Input-only Codex execution followed by a deterministic consumer. |
| [workspace](workspace/README.md) | A supplied Go change request followed by a fixed Go check. |
| [command](command/README.md) | An explicit model-free command on committed, read-only source. |
| [reviewed-go](reviewed-go/README.md) | Implementation, focused check, read-only review and local candidate integration. |
| [tasks](tasks/README.md) | Dependency-ordered template expansion and joined results without models. |

Select an example's `instance.yaml` or `instance.json` with `serve --config`. Examples are not loaded
automatically. Test fixtures live under the repository-root
`testdata/` directory. Examples can be edited without changing unit-test fixtures.

[intake](intake/README.md) is the separate restricted client profile for chat
integration; it is not a service configuration.
