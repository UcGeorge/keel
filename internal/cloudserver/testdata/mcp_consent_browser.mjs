import assert from 'node:assert/strict';
import { chromium } from 'playwright';

let input = '';
for await (const chunk of process.stdin) input += chunk;
const config = JSON.parse(input);
const browser = await chromium.launch();
try {
  const page = await browser.newPage();
  page.setDefaultTimeout(10_000);
  page.setDefaultNavigationTimeout(10_000);
  const violations = [];
  const browserErrors = [];
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(message.text());
  });
  await page.exposeFunction('recordPolicyViolation', (directive) => violations.push(directive));
  await page.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (event) => {
      window.recordPolicyViolation(event.effectiveDirective);
    });
  });
  await page.goto(`${config.baseURL}/login`);
  await page.getByLabel('Email', { exact: true }).fill(config.email);
  await page.getByLabel('Password', { exact: true }).fill(config.password);
  await Promise.all([
    page.waitForURL((url) => url.pathname !== '/login'),
    page.getByRole('button', { name: 'Sign in', exact: true }).click(),
  ]);

  const results = [];
  for (const scope of ['keel:read', 'keel:full']) {
    for (const decision of ['deny', 'allow']) {
      const consent = new URL(config.authorizeURL);
      const state = `browser-${scope}-${decision}`;
      // Prove that the owner can override either client-requested scope.
      consent.searchParams.set('scope', scope === 'keel:read' ? 'keel:full' : 'keel:read');
      consent.searchParams.set('state', state);
      const response = await page.goto(consent.href);
      assert.equal(response.status(), 200);
      const headers = response.headers();
      assert.equal(headers['cache-control'], 'no-store');
      assert.equal(headers['referrer-policy'], 'no-referrer');
      assert.equal(headers['x-frame-options'], 'DENY');
      assert.match(headers['content-security-policy'], /frame-ancestors 'none'/);
      assert.match(headers['content-security-policy'], /base-uri 'self'/);

      // The link on the consent page must also remain usable.
      const [connectionsResponse] = await Promise.all([
        page.waitForResponse(`${config.baseURL}/account/agents`),
        page.waitForURL(`${config.baseURL}/account/agents`),
        page.getByRole('link', { name: 'Agent connections', exact: true }).click(),
      ]);
      assert.equal(connectionsResponse.status(), 200);
      assert.match(connectionsResponse.headers()['content-security-policy'], /form-action 'self'/);
      await page.goto(consent.href);
      assert.equal(await page.getByRole('radio', { name: 'Read only', exact: true }).isChecked(), true);
      await page.getByRole('radio', { name: scope === 'keel:full' ? 'Full access' : 'Read only', exact: true }).check();
      try {
        await Promise.all([
          page.waitForURL((url) => `${url.origin}${url.pathname}` === config.landingURL),
          page.getByRole('button', { name: decision === 'allow' ? 'Authorize connection' : 'Deny', exact: true }).click(),
        ]);
      } catch (error) {
        throw new Error(`${scope} ${decision} did not reach the callback; CSP violations: ${violations.join(', ')}; browser errors: ${browserErrors.join('\n')}`, { cause: error });
      }
      const returned = new URL(page.url()).searchParams;
      assert.equal(returned.get('state'), state);
      assert.equal(returned.get('iss'), config.baseURL);
      if (decision === 'allow') {
        assert.ok(returned.get('code'));
        assert.equal(returned.get('error'), null);
        results.push({ scope, code: returned.get('code') });
        await page.goto(`${config.baseURL}/account/agents`);
        const connection = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Browser regression agent', exact: true }) }).first();
        const original = scope === 'keel:full' ? 'Full access' : 'Read only';
        assert.equal(await connection.getByRole('radio', { name: original, exact: true }).isChecked(), true);
        // Change each connection in both directions and verify persisted UI state.
        for (const level of [scope === 'keel:full' ? 'Read only' : 'Full access', original]) {
          await connection.getByRole('radio', { name: level, exact: true }).check();
          const [saved] = await Promise.all([
            page.waitForResponse((response) => response.url() === `${config.baseURL}/account/agents/permissions` && response.request().method() === 'POST'),
            connection.getByRole('button', { name: 'Save permissions', exact: true }).click(),
          ]);
          assert.equal(saved.status(), 303);
          assert.match(await page.getByRole('status').textContent(), /Permissions updated/);
          await page.reload();
          assert.equal(await connection.getByRole('radio', { name: level, exact: true }).isChecked(), true);
        }
      } else {
        assert.equal(returned.get('error'), 'access_denied');
        assert.equal(returned.get('code'), null);
      }
    }
  }
  assert.deepEqual(violations, [], 'the flow must not violate the browser security policy');
  process.stdout.write(JSON.stringify(results));
} finally {
  await browser.close();
}
