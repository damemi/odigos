// Test the actual release image with an ephemeral session key and local TLS.
// RSA/key-generator compatibility is covered by the Go tests; CI never needs
// the production license signing key or a customer API key.
const { chromium } = require('playwright');
const { execFileSync } = require('node:child_process');
const { createHmac, randomBytes } = require('node:crypto');
const fs = require('node:fs');
const https = require('node:https');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert/strict');
const image = process.argv[2];
assert(image, 'Pass a built Docker image');
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'odigos-docs-test-'));
const sessionKey = randomBytes(48);
const cookieName = '__Host-odigos-docs';
let container, browser, proxy, upstreamPort;
const run = (command, args, options = {}) =>
  execFileSync(command, args, {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    ...options,
  }).trim();
const pass = (message) => console.log('PASS: ' + message);
(async () => {
  run('openssl', [
    'req',
    '-x509',
    '-newkey',
    'rsa:2048',
    '-nodes',
    '-days',
    '1',
    '-subj',
    '/CN=localhost',
    '-keyout',
    path.join(temp, 'key.pem'),
    '-out',
    path.join(temp, 'cert.pem'),
  ]);
  proxy = https.createServer(
    {
      key: fs.readFileSync(path.join(temp, 'key.pem')),
      cert: fs.readFileSync(path.join(temp, 'cert.pem')),
    },
    (request, response) => {
      const upstream = http.request(
        {
          hostname: '127.0.0.1',
          port: upstreamPort,
          path: request.url,
          method: request.method,
          headers: request.headers,
        },
        (result) => {
          response.writeHead(result.statusCode, result.headers);
          result.pipe(response);
        },
      );
      upstream.on('error', () => {
        response.writeHead(502);
        response.end();
      });
      request.pipe(upstream);
    },
  );
  await new Promise((resolve) => proxy.listen(0, '127.0.0.1', resolve));
  const origin = 'https://localhost:' + proxy.address().port;
  container = run(
    'docker',
    [
      'run',
      '--detach',
      '--read-only',
      '--user',
      '10001:10001',
      '--cap-drop=ALL',
      '--security-opt=no-new-privileges',
      '--publish',
      '127.0.0.1::8080',
      '--env',
      'DOCS_ORIGIN',
      '--env',
      'DOCS_SESSION_KEY_BASE64',
      image,
    ],
    {
      env: {
        ...process.env,
        DOCS_ORIGIN: origin,
        DOCS_SESSION_KEY_BASE64: sessionKey.toString('base64'),
      },
    },
  );
  upstreamPort = Number(run('docker', ['port', container, '8080/tcp']).split(':').at(-1));
  let healthy = false;
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      healthy = (await fetch('http://127.0.0.1:' + upstreamPort + '/healthz')).status === 200;
    } catch {}
    if (healthy) break;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  assert(
    healthy,
    'Container did not start as an arbitrary non-root user on a read-only filesystem',
  );
  run('docker', ['cp', container + ':/content', path.join(temp, 'content')]);
  console.log(run('python3', [path.join(__dirname, 'audit-build.py'), path.join(temp, 'content')]));
  pass('Release image starts and compiled content passes link/asset audit');
  browser = await chromium.launch({
    headless: true,
    ...(process.env.DOCS_BROWSER_CHANNEL ? { channel: process.env.DOCS_BROWSER_CHANNEL } : {}),
  });
  const context = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 1000 },
  });
  const page = await context.newPage();
  const errors = [],
    failures = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('response', (response) => {
    if (response.status() >= 500 || response.status() === 429)
      failures.push({ url: response.url(), status: response.status() });
  });
  for (const route of [
    '/enterprise/overview',
    '/central/overview',
    '/docs-runtime.js',
    '/pagefind/pagefind.js',
    '/images/enterprise',
    '/llms.txt',
  ]) {
    const response = await context.request.get(origin + route, { maxRedirects: 0 });
    assert.equal(response.status(), 401, route);
    assert(response.headers()['cache-control'].includes('no-store'));
  }
  await page.goto(origin + '/enterprise/setup/installation');
  assert(page.url().startsWith(origin + '/login?redirect='));
  await page.getByLabel('Enterprise API key', { exact: true }).fill('invalid-key');
  await page.getByRole('button', { name: 'Open enterprise docs', exact: true }).click();
  await page.getByRole('alert').waitFor();
  assert(!(await context.cookies()).some((cookie) => cookie.name === cookieName));
  pass('Anonymous content and invalid API keys are rejected');
  const sign = (expires) => {
    const header = Buffer.from(JSON.stringify({ alg: 'HS256', typ: 'JWT' })).toString('base64url');
    const claims = Buffer.from(
      JSON.stringify({
        iss: origin,
        aud: [new URL(origin).host],
        exp: expires,
        iat: Math.floor(Date.now() / 1000),
      }),
    ).toString('base64url');
    const body = header + '.' + claims;
    return body + '.' + createHmac('sha256', sessionKey).update(body).digest('base64url');
  };
  const install = async (expires) => {
    const value = sign(expires);
    await context.addCookies([
      {
        name: cookieName,
        value,
        domain: 'localhost',
        path: '/',
        secure: true,
        httpOnly: true,
        sameSite: 'Lax',
        expires,
      },
    ]);
    return value;
  };
  await install(Math.floor(Date.now() / 1000) + 180);
  await page.goto(origin + '/enterprise/setup/installation', { waitUntil: 'networkidle' });
  await page.getByRole('tab').nth(1).click();
  await page.waitForFunction(
    () => document.querySelectorAll('[role=tab]')[1]?.getAttribute('aria-selected') === 'true',
    null,
    { timeout: 5000 },
  );
  assert.equal((await context.request.get(origin + '/pagefind/pagefind.js')).status(), 200);
  await page.locator('#search-bar-entry').click();
  await page.getByLabel('Search documentation', { exact: true }).fill('installation');
  const result = page.locator('.odigos-docs-results a').first();
  await result.waitFor();
  const destination = new URL(await result.getAttribute('href'), origin).href;
  await result.click();
  await page.waitForURL(destination);
  await page.goto(origin + '/cli/odigos_sources?from=ci');
  assert(page.url().endsWith('/enterprise/cli/odigos_sources?from=ci'));
  await page.goto(origin + '/central/overview', { waitUntil: 'networkidle' });
  assert(page.url().startsWith(origin + '/central/overview'));
  assert(await page.locator('#content-area').isVisible());
  await page.goto(origin + '/enterprise/overview', { waitUntil: 'networkidle' });
  await page.locator('[data-component-part=products-selector-trigger]').click();
  await page.getByRole('menuitem').filter({ hasText: 'Central' }).click();
  await page.waitForURL('**/central/overview');
  await page.setViewportSize({ width: 390, height: 844 });
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await page.waitForURL('**/login');
  assert.equal((await context.request.get(origin + '/docs-runtime.js')).status(), 401);
  pass('Code tabs, private search, redirects, navigation, mobile layout and sign-out work');
  const expiredSession = await install(Math.floor(Date.now() / 1000) + 12);
  await page.goto(origin + '/enterprise/overview', { waitUntil: 'networkidle' });
  await page.waitForURL('**/login?redirect=**', { timeout: 20000 });
  for (const route of [
    '/enterprise/overview',
    '/central/overview',
    '/docs-runtime.js',
    '/pagefind/pagefind.js',
    '/llms.txt',
  ]) {
    // Replay the signed token explicitly so rejection tests server expiry,
    // independently of whether the browser has deleted the expired cookie.
    const response = await context.request.get(origin + route, {
      maxRedirects: 0,
      headers: { Cookie: cookieName + '=' + expiredSession },
    });
    assert.equal(response.status(), 401, route);
  }
  assert.deepEqual(errors, []);
  assert.deepEqual(failures, []);
  pass('An open page locks on session expiry; expired sessions cannot fetch content');
})()
  .catch((error) => {
    console.error(error.message.replace(/eyJ[\w-]+\.[\w-]+\.[\w-]+/g, '[redacted token]'));
    process.exitCode = 1;
  })
  .finally(async () => {
    if (browser) await browser.close();
    if (container) run('docker', ['rm', '--force', container]);
    if (proxy) await new Promise((resolve) => proxy.close(resolve));
    fs.rmSync(temp, { recursive: true, force: true });
  });
