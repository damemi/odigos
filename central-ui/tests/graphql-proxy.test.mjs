import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/graphql-proxy.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
const module = { exports: {} };
new Function('module', 'exports', js)(module, module.exports);
const { proxyGraphQL, GRAPHQL_PROXY_TIMEOUT_MS } = module.exports;
const request = () => new Request('http://localhost/graphql', { method: 'POST', headers: { authorization: 'Bearer test-token', 'content-type': 'application/json' }, body: JSON.stringify({ query: 'mutation CreateCloudConnector { createCloudConnector { created } }' }) });

test('proxy preserves backend GraphQL diagnostics, status and authentication', async () => {
  const body = { errors: [{ message: 'AccessDenied: caller cannot assume role', extensions: { provider: 'aws' } }] };
  const response = await proxyGraphQL(request(), 'http://central-backend:8081/', async (url, options) => {
    assert.equal(url, 'http://central-backend:8081/graphql');
    assert.equal(options.headers.get('authorization'), 'Bearer test-token');
    assert.ok(options.headers.get('x-request-id'));
    assert.match(options.body, /CreateCloudConnector/);
    return Response.json(body, { status: 500 });
  });
  assert.equal(response.status, 500);
  assert.deepEqual(await response.json(), body);
});

test('backend disconnect returns actionable GraphQL diagnostics and a correlation ID', async () => {
  const response = await proxyGraphQL(request(), 'http://central-backend:8081', async () => { throw new Error('socket hang up'); });
  assert.equal(response.status, 502);
  const { errors } = await response.json();
  assert.match(errors[0].message, /Unable to reach Central backend/);
  assert.equal(errors[0].extensions.code, 'CENTRAL_BACKEND_UNAVAILABLE');
  assert.equal(errors[0].extensions.requestId, response.headers.get('x-request-id'));
});

test('proxy timeout returns a structured failure instead of an empty HTTP 500', async () => {
  assert.ok(GRAPHQL_PROXY_TIMEOUT_MS > 100_000, 'must allow 90s verification and 10s persistence');
  const hold = setTimeout(() => {}, 1000);
  try {
    const response = await proxyGraphQL(request(), 'http://central-backend:8081', (_url, { signal }) => new Promise((_resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason), { once: true })), 5);
    assert.equal(response.status, 504);
    const { errors } = await response.json();
    assert.match(errors[0].message, /Provider verification may be taking too long/);
    assert.equal(errors[0].extensions.code, 'CENTRAL_BACKEND_TIMEOUT');
  } finally { clearTimeout(hold); }
});
