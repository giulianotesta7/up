'use strict';

// PR content is data only. Resolve references within the workflow's repository.
async function validateIssue({ body, github, context }) {
  const visible = (body || '').replace(/<!--[\s\S]*?(?:-->|$)/g, '');
  const numbers = [...new Set([...visible.matchAll(/^\s*Closes #([0-9]+)\s*$/gim)]
    .map(match => Number(match[1]))
    .filter(number => Number.isSafeInteger(number) && number > 0))];
  if (!numbers.length) {
    throw new Error('Add a standalone line: Closes #<number>. Read CONTRIBUTING.md for the issue-first workflow.');
  }
  for (const issue_number of numbers) {
    try {
      const { data } = await github.rest.issues.get({ ...context.repo, issue_number });
      if (!data.pull_request) return issue_number;
    } catch (error) {
      if (error.status !== 404) throw error;
    }
  }
  throw new Error('Closes must reference at least one existing issue in this repository, not a pull request.');
}

module.exports = { validateIssue };
