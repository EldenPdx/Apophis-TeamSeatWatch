import { spawnSync } from 'node:child_process';

const race = process.argv[2] === '--race';
if (process.argv[2] && !race) {
  throw new Error('usage: node tools/run-integration.mjs [--race]');
}

// Existing destructive fixtures require these disposable loopback databases.
const groups = [
  ['runtime', 'tsw_ticket16_worker', ['-skip', '^TestProxy|^TestRedemption']],
  ['runtime', 'tsw_proxy_verify_clear_runtime', ['-run', '^TestProxy']],
  ['runtime', 'tsw_redemptions_test', ['-run', '^TestRedemption']],
  ['task', 'tsw_proxy_verify_clear_task', []],
  ['migrations', 'tsw_clear_migrations', []],
  ['mothersecret', 'tsw_clear_mother', []],
  ['rotationledger', 'tsw_clear_ledger', []],
  ['writerfence', 'tsw_clear_fence', []],
  ['workspace', 'tsw_clear_workspace', []],
];

for (const [name, database, selection] of groups) {
  const args = ['test', '-tags=integration', '-count=1', '-timeout=30m', ...(race ? ['-race'] : []), ...selection, `./internal/${name}`];
  process.stdout.write(`integration: ${name} (${database})\n`);
  const result = spawnSync('go', args, {
    stdio: 'inherit',
    env: {
      ...process.env,
      TSW_TEST_DATABASE_URL: `postgres://postgres@127.0.0.1:55411/${database}?sslmode=disable`,
    },
  });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}
