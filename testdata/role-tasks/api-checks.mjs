import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';

// Fixed product obligations, kept outside the product's write paths.
export async function checkAPI(baseURL, fixture, checks) {
  const origin = new URL(baseURL).origin;
  function session() {
    const cookies = new Map();
    return async function request(method, route, body) {
      const response = await fetch(new URL(route, origin), {
        method,
        redirect: 'manual',
        signal: AbortSignal.timeout(15_000),
        headers: {
          Origin: origin,
          ...(body === undefined ? {} : {'Content-Type': 'application/json'}),
          ...(cookies.size ? {Cookie: [...cookies].map(([key, value]) => key + '=' + value).join('; ')} : {})
        },
        body: body === undefined ? undefined : JSON.stringify(body)
      });
      for (const cookie of response.headers.getSetCookie()) {
        const pair = cookie.split(';', 1)[0];
        const index = pair.indexOf('=');
        if (index > 0) cookies.set(pair.slice(0, index), pair.slice(index + 1));
      }
      const chunks = [];
      let bytes = 0;
      for await (const chunk of response.body || []) {
        bytes += chunk.byteLength;
        if (bytes > 1 << 20) throw new Error('API response exceeded 1MiB');
        chunks.push(chunk);
      }
      const text = Buffer.concat(chunks).toString('utf8');
      let json;
      try { json = JSON.parse(text); } catch {}
      return {status: response.status, json, text};
    };
  }
  async function check(id, action) {
    try {
      await action();
      checks.push({id, status: 'passed'});
    } catch (error) {
      throw new Error(id + ': ' + error.message, {cause: error});
    }
  }
  function status(response, allowed) {
    assert.ok(allowed.includes(response.status), 'unexpected HTTP ' + response.status + '; expected ' + allowed.join('/'));
  }
  const denied = [400, 401, 403, 404, 422];
  const anon = session();
  const adminA = session();
  const memberA = session();
  const adminB = session();
  const actors = {adminA, memberA, adminB};
  await check('api.unauthenticated-crud-denied', async () => {
    status(await anon('GET', '/api/campaigns'), [401, 403]);
    status(await anon('POST', '/api/campaigns', {name: 'Unauthorized', description: 'No session', status: 'draft'}), [401, 403]);
  });
  for (const [key, request] of Object.entries(actors)) {
    await check('api.login-and-tenant.' + key, async () => {
      const user = fixture.users[key];
      status(await request('POST', '/api/auth/login', {
        email: user.email, password: user.password, tenant_id: user.tenant_id
      }), [200]);
      const me = await request('GET', '/api/auth/me');
      status(me, [200]);
      assert.equal(me.json?.user?.email, user.email);
      assert.equal(me.json?.user?.tenant_id, user.tenant_id);
      assert.equal(me.json?.user?.is_tenant_admin, user.admin);
    });
  }
  await check('api.foreign-membership-login-denied', async () => {
    const wrongTenant = session();
    status(await wrongTenant('POST', '/api/auth/login', {
      email: fixture.users.adminB.email,
      password: fixture.users.adminB.password,
      tenant_id: fixture.users.adminA.tenant_id
    }), denied);
    status(await wrongTenant('GET', '/api/campaigns'), [401, 403]);
  });

  const suffix = randomUUID().slice(0, 8);
  let campaign;
  await check('api.member-creates-draft', async () => {
    const created = await memberA('POST', '/api/campaigns', {
      name: 'API campaign ' + suffix,
      description: 'Private draft ' + suffix,
      status: 'draft'
    });
    status(created, [201]);
    campaign = created.json;
    assert.equal(typeof campaign?.id, 'string');
    assert.ok(campaign.id.length > 0);
    assert.equal(campaign.status, 'draft');
    const own = await memberA('GET', '/api/campaigns/' + campaign.id);
    status(own, [200]);
    assert.equal(own.json?.name, campaign.name);
    if (own.json.tenant_id !== undefined) assert.equal(own.json.tenant_id, fixture.users.memberA.tenant_id);
  });
  const route = '/api/campaigns/' + campaign.id;
  await check('api.tenant-list-isolation', async () => {
    const own = await adminA('GET', '/api/campaigns');
    const foreign = await adminB('GET', '/api/campaigns');
    status(own, [200]);
    status(foreign, [200]);
    assert.ok(Array.isArray(own.json) && own.json.some(item => item.id === campaign.id));
    assert.ok(Array.isArray(foreign.json));
    assert.ok(!foreign.json.some(item => item.id === campaign.id || item.name === campaign.name));
  });
  await check('api.foreign-id-read-edit-delete-publish-denied', async () => {
    status(await adminB('GET', route), [403, 404]);
    status(await adminB('PATCH', route, {name: 'Foreign overwrite'}), [403, 404]);
    status(await adminB('PUT', route, {
      name: 'Foreign overwrite', description: campaign.description, status: 'draft'
    }), [403, 404]);
    status(await adminB('DELETE', route), [403, 404]);
    status(await adminB('POST', route + '/publish', {}), [403, 404]);
    const own = await memberA('GET', route);
    status(own, [200]);
    assert.equal(own.json.name, campaign.name);
    assert.equal(own.json.status, 'draft');
  });
  await check('api.draft-public-preview-denied', async () => {
    status(await anon('GET', '/api/public/campaigns/' + campaign.id), [404]);
  });
  await check('api.member-edits-draft', async () => {
    const edited = await memberA('PATCH', route, {name: campaign.name + ' edited', description: 'Edited draft ' + suffix});
    status(edited, [200]);
    const saved = await adminA('GET', route);
    status(saved, [200]);
    assert.equal(saved.json?.name, campaign.name + ' edited');
    assert.equal(saved.json?.description, 'Edited draft ' + suffix);
    campaign = saved.json;
  });
  await check('api.own-tenant-put-and-readback', async () => {
    const replacement = {
      name: campaign.name + ' replaced', description: 'PUT replacement ' + suffix, status: 'draft'
    };
    status(await memberA('PUT', route, replacement), [200]);
    const saved = await adminA('GET', route);
    status(saved, [200]);
    assert.equal(saved.json?.name, replacement.name);
    assert.equal(saved.json?.description, replacement.description);
    assert.equal(saved.json?.status, 'draft');
    campaign = saved.json;
  });
  await check('api.tenant-field-spoof-cannot-move-record', async () => {
    const spoof = await memberA('PATCH', route, {tenant_id: fixture.users.adminB.tenant_id});
    status(spoof, [200, ...denied]);
    const own = await memberA('GET', route);
    status(own, [200]);
    assert.equal(own.json?.name, campaign.name);
    if (own.json.tenant_id !== undefined) assert.equal(own.json.tenant_id, fixture.users.memberA.tenant_id);
    status(await adminB('GET', route), [403, 404]);
  });
  await check('api.tenant-field-spoof-cannot-create-foreign-record', async () => {
    const spoof = await memberA('POST', '/api/campaigns', {
      name: 'Spoofed ' + suffix, description: 'Must remain tenant A', status: 'draft',
      tenant_id: fixture.users.adminB.tenant_id
    });
    status(spoof, [201, ...denied]);
    if (spoof.status === 201) {
      assert.equal(typeof spoof.json?.id, 'string');
      status(await memberA('GET', '/api/campaigns/' + spoof.json.id), [200]);
      status(await adminB('GET', '/api/campaigns/' + spoof.json.id), [403, 404]);
    }
    const foreign = await adminB('GET', '/api/campaigns');
    status(foreign, [200]);
    assert.ok(Array.isArray(foreign.json) && !foreign.json.some(item => item.name === 'Spoofed ' + suffix));
  });
  await check('api.generic-create-cannot-publish', async () => {
    const response = await memberA('POST', '/api/campaigns', {
      name: 'Bypass ' + suffix, description: 'Must not publish', status: 'published'
    });
    status(response, [201, ...denied]);
    if (response.status === 201) {
      const own = await memberA('GET', '/api/campaigns/' + response.json?.id);
      status(own, [200]);
      assert.equal(own.json?.status, 'draft');
      status(await anon('GET', '/api/public/campaigns/' + response.json.id), [404]);
    }
  });
  for (const [actor, request] of [['member', memberA], ['admin', adminA]]) {
    for (const method of ['PUT', 'PATCH']) {
      await check('api.generic-' + method.toLowerCase() + '-cannot-publish.' + actor, async () => {
        const response = await request(method, route, {
          name: campaign.name, description: campaign.description, status: 'published'
        });
        status(response, [200, ...denied]);
        const own = await adminA('GET', route);
        status(own, [200]);
        assert.equal(own.json?.status, 'draft');
        status(await anon('GET', '/api/public/campaigns/' + campaign.id), [404]);
      });
    }
  }
  await check('api.member-publish-action-denied', async () => {
    status(await memberA('POST', route + '/publish', {}), [403]);
    const own = await adminA('GET', route);
    status(own, [200]);
    assert.equal(own.json?.status, 'draft');
  });
  await check('api.admin-publishes-own-tenant', async () => {
    status(await adminA('POST', route + '/publish', {}), [200]);
    const own = await memberA('GET', route);
    status(own, [200]);
    assert.equal(own.json?.status, 'published');
  });
  await check('api.public-payload-is-published-and-sanitized', async () => {
    const preview = await anon('GET', '/api/public/campaigns/' + campaign.id);
    status(preview, [200]);
    assert.deepEqual(Object.keys(preview.json).sort(), ['description', 'id', 'name', 'status']);
    assert.equal(preview.json.id, campaign.id);
    assert.equal(preview.json.name, campaign.name);
    assert.equal(preview.json.description, campaign.description);
    assert.equal(preview.json.status, 'published');
    status(await anon('GET', '/api/public/campaigns/' + randomUUID()), [404]);
  });
  await check('api.own-tenant-delete-and-readback', async () => {
    status(await adminA('DELETE', route), [200, 204]);
    status(await adminA('GET', route), [404]);
    status(await memberA('GET', route), [404]);
    status(await anon('GET', '/api/public/campaigns/' + campaign.id), [404]);
    const remaining = await adminA('GET', '/api/campaigns');
    status(remaining, [200]);
    assert.ok(Array.isArray(remaining.json) && !remaining.json.some(item => item.id === campaign.id));
  });
}
