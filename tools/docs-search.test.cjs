const { test } = require('node:test');
const assert = require('node:assert/strict');
const search = require('../website/static/assets/docs-search.js');

const index = [
  { p: 'guide/', pt: 'Approvals', t: 'Approvals', k: '', a: '', b: 'Set retryTTLSeconds to configure the retry window. Protect secret.key on disk.' },
  { p: 'guide/', pt: 'Approvals', t: 'Retry window', k: '', a: 'retry-window' },
  { p: 'guide/', pt: 'Approvals', t: 'Retry a call', k: '', a: 'retry-call' },
  { p: 'cli/', pt: 'straza connect', t: 'straza connect', k: '', a: '', b: 'Connect to an upstream.' },
  { p: 'other/', pt: 'Overview', t: 'Overview', k: '', a: '', b: 'Use straza connect when connecting.' },
];

test('finds body-only configuration keys and returns a matching snippet', () => {
  const result = search(index, 'retryTTLSeconds');
  assert.equal(result.total, 1);
  assert.match(result.hits[0].snippet, /retryTTLSeconds/);
  assert.equal(search(index, 'secret.key').hits[0].entry.p, 'guide/');
});

test('groups heading matches by page and preserves the best heading anchor', () => {
  const result = search(index, 'retry window');
  assert.equal(result.total, 1);
  assert.equal(result.hits[0].entry.a, 'retry-window');
});

test('ranks command titles before incidental body mentions', () => {
  const result = search(index, 'STRAZA connect');
  assert.equal(result.hits[0].entry.p, 'cli/');
});

test('requires every query word and reports the total before truncation', () => {
  assert.equal(search(index, 'retry missing').total, 0);
  assert.equal(search(index, '  ').total, 0);
  const result = search(index, 'connect', 1);
  assert.equal(result.total, 2);
  assert.equal(result.hits.length, 1);
});

test('selects an excerpt containing the full question rather than the first isolated word', () => {
  const body = 'The password is printed once. ' + 'Unrelated operating details. '.repeat(30) + 'If the vaulted password is lost, follow the recovery procedure before signing in again.';
  const result = search([{ p: 'security/recovery/', pt: 'Recovery', t: 'Recovery', k: '', a: '', b: body }], 'lost password');
  assert.match(result.hits[0].snippet, /password is lost/);
  assert.ok(result.hits[0].snippet.length < 260);
});

test('filters by section before limiting results and preserves heading links', () => {
  const entries = [
    { p: 'guides/server/', pt: 'Add a server', t: 'Add a server', k: '', a: '', b: 'Configure a credential.' },
    { p: 'guides/server/', pt: 'Add a server', t: 'Credential', k: '', a: 'credential' },
    { p: 'reference/credential/', pt: 'Credential', t: 'Credential', k: '', a: '', b: 'Set a credential.' },
  ];
  const result = search(entries, 'credential', 1, 'guides');
  assert.equal(result.total, 1);
  assert.equal(result.hits[0].entry.p, 'guides/server/');
  assert.equal(result.hits[0].entry.a, 'credential');
  assert.equal(search(entries, 'credential', 10, 'security').total, 0);
});

test('published search finds real configuration keys and commands', { skip: !process.env.DOCS_SITE_DIR }, () => {
  const fs = require('node:fs');
  const path = require('node:path');
  const site = path.resolve(process.env.DOCS_SITE_DIR);
  const publishedSearch = require(path.join(site, 'assets/docs-search.js'));
  const pages = JSON.parse(fs.readFileSync(path.join(site, 'index.json'), 'utf8'));
  for (const query of ['retryTTLSeconds', 'secret.key']) {
    const result = publishedSearch(pages, query);
    assert.ok(result.total > 0, query + ' must be searchable');
    assert.ok(result.hits.some((hit) => hit.snippet.toLowerCase().includes(query.toLowerCase())));
    assert.equal(new Set(result.hits.map((hit) => hit.entry.p)).size, result.hits.length);
  }
  assert.equal(publishedSearch(pages, 'straza connect').hits[0].entry.p, 'reference/cli/straza/straza_connect/');
  assert.match(pages.find(page => page.p === 'get-started/' && !page.a).b, /Install the three binaries Server/);
  assert.match(pages.find(page => page.p === 'get-started/first-governed-session/' && !page.a).b, /Before you start The three binaries on your PATH/);
  const filtered = publishedSearch(pages, 'credential', 10, 'guides');
  assert.ok(filtered.total > 0);
  assert.ok(filtered.hits.every(hit => hit.entry.p.startsWith('guides/')));
  assert.match(publishedSearch(pages, 'lost password').hits.find(hit => hit.entry.p === 'security/keys-certificates-and-tokens/').snippet, /password is lost/);
});
