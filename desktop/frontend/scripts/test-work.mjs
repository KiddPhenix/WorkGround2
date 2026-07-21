import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const root = fileURLToPath(new URL('..', import.meta.url));
const tsx = require.resolve('tsx/cli');
const tests = [
  'src/work/store.test.ts',
  'src/__tests__/block-registry.test.ts',
  'src/__tests__/block-host-fallback.test.tsx',
  'src/__tests__/block-host-review.test.tsx',
  'src/__tests__/block-host-broker-review.test.tsx',
  'src/__tests__/block-host-semantic-review.test.tsx',
  'src/__tests__/block-state-renderers.test.tsx',
  'src/__tests__/block-renderers.test.tsx',
  'src/__tests__/block-action-renderers.test.tsx',
  'src/__tests__/work-card.test.tsx',
  'src/__tests__/run-progress.test.tsx',
];

for (const test of tests) {
  const result = spawnSync(process.execPath, [tsx, test], {
    cwd: root,
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.status !== 0 || result.error) {
    process.stdout.write(result.stdout ?? '');
    process.stderr.write(result.stderr ?? '');
    if (result.error) process.stderr.write(`${result.error.message}\n`);
    process.exit(result.status ?? 1);
  }
  process.stdout.write(`PASS ${test}\n`);
}
