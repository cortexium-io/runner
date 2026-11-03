# Dependency-ordered work

This model-free example uses the ordinary service and one run. The second item
is listed first but executes after its declared predecessor. No native service,
account or source repository is needed.

```sh
cortexium-runner serve --config examples/tasks/instance.json --data /private/fresh-state
cortexium-runner run --data /private/fresh-state --project tasks --request-id tasks-1 \
  --inputs '{"tasks":[{"id":"second","depends_on":["first"],"inputs":{"text":"second result"}},{"id":"first","inputs":{"text":"first result"}}]}'
```

The compact result contains both outputs in its `results` manifest and the final
item's result in `last`. Definitions fix permitted roles and bindings; item data
supplies work and dependencies. The same expansion can consume a configured
planner's JSON text output. Items are serial within one owned run; independent
runs still use the existing instance concurrency policy.
