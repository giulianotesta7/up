const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { validateIssue } = require('./pr-issue.cjs');

function api(resources = {}) {
  const calls = [];
  return {
    calls,
    rest: { issues: { get: async (args) => {
      calls.push(args);
      const value = resources[args.issue_number];
      if (value instanceof Error) throw value;
      if (!value) throw Object.assign(new Error('Not Found'), { status: 404 });
      return { data: value };
    } } },
  };
}
const context = { repo: { owner: 'owner', repo: 'up' } };

test('requires a standalone uncommented positive safe issue reference', async () => {
  for (const body of [null, '', 'Closes #<issue>', 'Fixes #1', 'Text Closes #1',
    'Closes #0', 'Closes #9007199254740992', 'Closes #1 extra',
    'Closes owner/other#1', 'Closes https://github.com/owner/up/issues/1',
    '<!-- Closes #1 -->', '<!--\nCloses #1', '<!-- hidden -->']) {
    const github = api();
    await assert.rejects(validateIssue({ body, github, context }), /standalone.*Closes/);
    assert.deepEqual(github.calls, []);
  }
});

test('accepts an existing issue without approval labels or assignees, including closed issues', async () => {
  const github = api({ 12: { number: 12, state: 'closed', labels: [], assignees: [] } });
  assert.equal(await validateIssue({ body: 'Why\n\n closes #12 \n', github, context }), 12);
  assert.deepEqual(github.calls, [{ owner: 'owner', repo: 'up', issue_number: 12 }]);
});

test('rejects PR references and missing issues', async () => {
  for (const resources of [{}, { 12: { pull_request: {} } }]) {
    await assert.rejects(validateIssue({ body: 'Closes #12', github: api(resources), context }), /existing issue/);
  }
});

test('deduplicates references and accepts the first real issue', async () => {
  const github = api({ 2: { pull_request: {} }, 3: { number: 3 } });
  assert.equal(await validateIssue({ body: 'Closes #1\nCloses #1\nCloses #2\nCloses #3', github, context }), 3);
  assert.deepEqual(github.calls.map(call => call.issue_number), [1, 2, 3]);
});

test('propagates non-404 API failures', async () => {
  const failure = Object.assign(new Error('Forbidden'), { status: 403 });
  await assert.rejects(validateIssue({ body: 'Closes #1', github: api({ 1: failure }), context }), failure);
});

test('edits are validated against the current body', async () => {
  const github = api({ 1: { number: 1 } });
  assert.equal(await validateIssue({ body: 'Closes #1', github, context }), 1);
  await assert.rejects(validateIssue({ body: '<!-- Closes #1 -->', github, context }), /standalone/);
});

test('workflow validates the fetched PR body and fails closed on PR API errors', async () => {
  const workflow = fs.readFileSync('.github/workflows/pr-issue.yml', 'utf8');
  const script = workflow.split('          script: |\n')[1].split('\n').map(line => line.slice(12)).join('\n');
  const run = new (Object.getPrototypeOf(async function () {}).constructor)('require', 'github', 'context', 'core', script);
  for (const [eventBody, currentBody, error, expected] of [
    ['Closes #1', '', null, /standalone/],
    ['', 'Closes #1', null, null],
    ['Closes #1', null, new Error('PR unavailable'), /PR unavailable/],
  ]) {
    const github = api({ 1: { number: 1 } });
    const calls = [];
    github.rest.pulls = { get: async args => {
      calls.push(args);
      if (error) throw error;
      return { data: { body: currentBody } };
    } };
    const messages = [];
    const workflowRequire = require('node:module').createRequire(`${process.cwd()}/workflow.cjs`);
    await run(workflowRequire, github, { ...context, payload: { pull_request: { number: 42, body: eventBody } } }, {
      info: message => messages.push(['info', message]),
      setFailed: message => messages.push(['failed', message]),
    });
    assert.deepEqual(calls, [{ owner: 'owner', repo: 'up', pull_number: 42 }]);
    assert.equal(messages.length, 1);
    assert.equal(messages[0][0], expected ? 'failed' : 'info');
    assert.match(messages[0][1], expected || /Linked issue: #1/);
  }
});

test('workflow runs only trusted base code with read-only permissions', () => {
  const workflow = fs.readFileSync('.github/workflows/pr-issue.yml', 'utf8');
  assert.match(workflow, /pull_request_target:/);
  for (const event of ['opened', 'edited', 'reopened', 'synchronize', 'ready_for_review']) {
    assert.ok(workflow.includes(event));
  }
  assert.match(workflow, /ref: \$\{\{ github.event.pull_request.base.sha \}\}/);
  assert.match(workflow, /persist-credentials: false/);
  assert.match(workflow, /contents: read/);
  assert.match(workflow, /issues: read/);
  assert.match(workflow, /pull-requests: read/);
  assert.doesNotMatch(workflow, /: write|pull_request\.head|\$\{\{[^}]*\.body/);
  assert.match(workflow, /node --test .github\/scripts\/pr-issue.test.cjs/);
});
