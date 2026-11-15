# Protected qualification assets

These files are test inputs and fixed verification obligations, separate from the
runnable example configuration. Do not copy them into product write paths.

- saas-acceptance.md is the stable product goal used by every role.
- api-checks.mjs uses Node's HTTP client and real cookies; it has no dependencies.
- navigation-checks.mjs waits for the editor route and the loaded Name field
  before retaining its URL. Its focused Node test holds these events separately
  to reproduce Vue Router navigation finishing after a link click.
- qualify-saas.mjs owns temporary users/database, the served final executable and
  one headless configured Chrome for Testing through an existing Playwright module.
  It writes one evidence.tar.gz containing a bounded report and screenshots in
  its supplied Runner-owned scratch directory for the fixed command's declared
  artifact output. Optional expected-binary-sha256 binds continuation proof to
  the already accepted executable.

Run syntax checks with node --check. Real qualification runs as Runner's configured
shared command role after product implementation or through the supported
verification-only continuation builder in the example README.
Ordinary repository tests must not submit model turns or generate the product.

Run the focused navigation regression with:

~~~sh
node --test testdata/role-tasks/navigation-checks.test.mjs
~~~
