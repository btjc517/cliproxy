import test from 'node:test';
import assert from 'node:assert/strict';

globalThis.window = { addEventListener() {} };
const { S } = await import('../../internal/api/dashboard/core.js');
const { accountBadge } = await import('../../internal/api/dashboard/screens/accounts.js');
test('sign-in action belongs to the errored subscription row', () => {
  S.data = {};
  const row = { id: 'original.json', email: 'owner@example.test', provider: 'claude', status: 'error', can_reauthenticate: true };
  for (const message of ['invalid grant (retrying)', 'invalid_grant', '']) {
    assert.match(accountBadge({ ...row, status_message: message }, new Set()), /data-reauth="original.json".*Sign in again as owner@example.test/);
  }
  const retry = new Date(Date.now() + 86400000).toISOString();
  assert.match(accountBadge({ ...row, status_message: 'invalid grant (retrying)', next_retry_after: retry }, new Set()), /data-reauth/);
  for (const change of [{ status: 'active' }, { disabled: true }, { can_reauthenticate: false }, { next_retry_after: retry }]) {
    assert.doesNotMatch(accountBadge({ ...row, ...change }, new Set()), /data-reauth/);
  }
});
