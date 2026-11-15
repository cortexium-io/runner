import assert from 'node:assert/strict';
import test from 'node:test';
import {openCampaignEditor} from './navigation-checks.mjs';

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return {promise, resolve};
}

test('editor capture waits for delayed router navigation and the loaded form', async () => {
  const route = deferred();
  const visible = deferred();
  const value = deferred();
  const waitingRoute = deferred();
  const waitingVisible = deferred();
  const waitingValue = deferred();
  const events = [];
  let url = 'http://127.0.0.1/campaigns';
  let clicked = false;
  let loaded = false;
  const destination = 'http://127.0.0.1/campaigns/123/edit';
  const input = {
    async waitFor(options) {
      assert.deepEqual(options, {state: 'visible'});
      events.push('wait-visible');
      waitingVisible.resolve();
      await visible.promise;
    }
  };
  const page = {
    getByRole(role, options) {
      assert.equal(role, 'link');
      assert.deepEqual(options, {name: 'Summer campaign', exact: true});
      return {
        async getAttribute(name) { assert.equal(name, 'href'); return '/campaigns/123/edit'; },
        async click() { clicked = true; events.push('click'); }
      };
    },
    async waitForURL(expected) {
      assert.equal(expected, destination);
      assert.equal(clicked, true);
      events.push('wait-url');
      waitingRoute.resolve();
      await route.promise;
      url = destination;
    },
    getByLabel(name, options) {
      assert.equal(name, 'Name');
      assert.deepEqual(options, {exact: true});
      return input;
    },
    url() {
      if (clicked) {
        assert.equal(loaded, true, 'editor URL captured before navigation/form readiness');
        events.push('capture');
      }
      return url;
    }
  };
  const expect = locator => ({
    async toHaveValue(expected) {
      assert.equal(locator, input);
      assert.equal(expected, 'Summer campaign');
      events.push('wait-value');
      waitingValue.resolve();
      await value.promise;
      loaded = true;
    }
  });
  const result = openCampaignEditor(page, 'Summer campaign', expect);
  await waitingRoute.promise;
  assert.deepEqual(events, ['click', 'wait-url']);
  route.resolve();
  await waitingVisible.promise;
  assert.deepEqual(events, ['click', 'wait-url', 'wait-visible']);
  visible.resolve();
  await waitingValue.promise;
  assert.deepEqual(events, ['click', 'wait-url', 'wait-visible', 'wait-value']);
  value.resolve();
  assert.equal(await result, destination);
  assert.deepEqual(events, ['click', 'wait-url', 'wait-visible', 'wait-value', 'capture']);
});
