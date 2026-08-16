import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('..', import.meta.url));
const tests = [
  'src/work/wailsAdapterV2.test.ts',
  'src/__tests__/work-v2-contract.test.ts',
  'src/__tests__/work-contract.test.ts',
  'src/__tests__/work-controller-mutation.test.ts',
  'src/work/presentation.test.ts',
  'src/work/components/presentation/WorkStatePanel.test.tsx',
  'src/work/components/presentation/WorkDefinitionOverview.test.tsx',
  'src/work/components/presentation/WorkAutoStartCountdown.test.tsx',
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
  'src/__tests__/work-restart-dialog.test.tsx',
  'src/__tests__/work-start-surface.test.tsx',
  'src/__tests__/work-page.test.tsx',
  'src/__tests__/app-work-integration.test.tsx',
  'src/__tests__/cornerstone-drawer.test.tsx',
  'src/__tests__/run-progress.test.tsx',
  'src/__tests__/browser-sensitive-settings.test.tsx',
  'src/__tests__/bridge-mock-approval-mode.test.ts',
  'src/__tests__/at-matches.test.ts',
  'src/__tests__/artifact-shelf-scale.test.tsx',
  'src/__tests__/artifact-projection.test.ts',
  'src/__tests__/artifact-image-display.test.tsx',
  'src/__tests__/composer-draft-key.test.ts',
  'src/__tests__/desktop-ui-stores.test.ts',
  'src/__tests__/desktop-ui-components.test.tsx',
  'src/__tests__/iris-fixture.test.ts',
  'src/__tests__/relay-settings-contract.test.ts',
  'src/__tests__/run-stream-placement.test.tsx',
  'src/__tests__/run-events.test.ts',
  'src/__tests__/session-background.test.tsx',
  'src/__tests__/structure-clarification-card.test.tsx',
  'src/__tests__/theme-iris.test.tsx',
  'src/__tests__/work-parse-date.test.ts',
  'src/__tests__/work-v2-recovery-entry.test.tsx',
  'src/__tests__/workbench-layout.test.ts',
];

const hook = new URL('./test-asset-hook.mjs', import.meta.url).href;
const concurrency = Math.min(4, tests.length);
let next = 0;
let failed = false;

function run(test) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, ['--import', 'tsx', '--import', hook, test], {
      cwd: root,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (data) => { stdout += data; });
    child.stderr.on('data', (data) => { stderr += data; });
    child.on('error', (error) => {
      stderr += `${error.message}\n`;
    });
    child.on('close', (code) => {
      if (code === 0) {
        process.stdout.write(`PASS ${test}\n`);
      } else {
        failed = true;
        process.stderr.write(`FAIL ${test}\n${stdout}${stderr}`);
      }
      resolve();
    });
  });
}

async function worker() {
  while (next < tests.length) {
    const test = tests[next++];
    await run(test);
  }
}

await Promise.all(Array.from({ length: concurrency }, worker));
if (failed) process.exit(1);
