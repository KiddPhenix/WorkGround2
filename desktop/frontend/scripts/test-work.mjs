import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const root = fileURLToPath(new URL('..', import.meta.url));
const tsx = require.resolve('tsx/cli');
const tests = [
  'src/__tests__/work-v2-contract.test.ts',
  'src/__tests__/work-contract.test.ts',
  'src/__tests__/work-controller-mutation.test.ts',
  'src/work/presentation.test.ts',
  'src/work/components/presentation/WorkStatePanel.test.tsx',
  'src/work/components/presentation/WorkDefinitionOverview.test.tsx',
  'src/work/components/presentation/WorkInformationPanel.test.tsx',
  'src/work/store.test.ts',
  'src/work/components/v2/ResultShelf.test.tsx',
  'src/work/components/v2/ExecutionList.test.tsx',
  'src/work/components/v2/input/WorkInputHost.test.tsx',
  'src/work/components/v2/discussion/discussion.test.tsx',
  'src/__tests__/block-registry.test.ts',
  'src/__tests__/block-host-fallback.test.tsx',
  'src/__tests__/block-host-review.test.tsx',
  'src/__tests__/block-host-broker-review.test.tsx',
  'src/__tests__/block-host-semantic-review.test.tsx',
  'src/__tests__/block-state-renderers.test.tsx',
  'src/__tests__/block-renderers.test.tsx',
  'src/__tests__/block-action-renderers.test.tsx',
  'src/__tests__/work-card.test.tsx',
  'src/__tests__/linked-session-card.test.tsx',
  'src/__tests__/work-chat-input.test.tsx',
  'src/__tests__/work-control-bar.test.tsx',
  'src/__tests__/work-start-surface.test.tsx',
  'src/__tests__/work-page.test.tsx',
  'src/__tests__/app-work-integration.test.tsx',
  'src/__tests__/cornerstone-drawer.test.tsx',
  'src/__tests__/run-progress.test.tsx',
];

let failed = false;
for (const test of tests) {
  const args = test.endsWith('app-work-integration.test.tsx')
    ? ['--import', 'tsx', '--import', new URL('./test-asset-hook.mjs', import.meta.url).href, test]
    : [tsx, test];
  const result = spawnSync(process.execPath, args, {
    cwd: root,
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.status !== 0 || result.error) {
    process.stderr.write(`FAIL ${test}\n`);
    process.stdout.write(result.stdout ?? '');
    process.stderr.write(result.stderr ?? '');
    if (result.error) process.stderr.write(`${result.error.message}\n`);
    failed = true;
    continue;
  }
  process.stdout.write(`PASS ${test}\n`);
}

if (failed) process.exit(1);
