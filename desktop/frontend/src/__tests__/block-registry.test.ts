// Run: tsx src/__tests__/block-registry.test.ts

import { createRegistry } from '../components/work/blocks/registry';
import type {
  LazyLoader,
  RendererModule,
  RendererValidator,
  SchemaVersionSpec,
} from '../components/work/blocks/types';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  condition ? passed++ : failed++;
}

function equal<T>(actual: T, expected: T, label: string): void {
  ok(Object.is(actual, expected), `${label}${Object.is(actual, expected) ? '' : ` (expected ${String(expected)}, got ${String(actual)})`}`);
}

function throws(run: () => unknown, label: string): void {
  try {
    run();
    ok(false, label);
  } catch {
    ok(true, label);
  }
}

async function rejects(run: () => Promise<unknown>, label: string): Promise<void> {
  try {
    await run();
    ok(false, label);
  } catch {
    ok(true, label);
  }
}

const valid: RendererValidator = () => ({ valid: true });

function module(name: string): RendererModule {
  const Component: React.FC = () => null;
  Component.displayName = name;
  return { component: Component };
}

function loader(name: string): LazyLoader {
  return async () => module(name);
}

async function run(): Promise<void> {
  console.log('\n-- registration and support');
  {
    const registry = createRegistry();
    registry.register('list', { min: 1, max: 3 }, valid, loader('list-v1'));
    registry.register('list', { min: 5, max: 6 }, valid, loader('list-v5'));
    ok(registry.has('list', 1) && registry.has('list', 3), 'closed range includes both ends');
    equal(registry.support('list', 4).status, 'unsupported_schema', 'gap is unsupported schema');
    equal(registry.support('list', 7).status, 'future_schema', 'version above known maximum is future schema');
    equal(registry.support('ghost', 1).status, 'unknown_kind', 'unregistered kind is unknown');
    equal(registry.support('list', 0).status, 'unsupported_schema', 'invalid runtime schema is unsupported');
  }

  console.log('\n-- registration validation');
  for (const [kind, versions] of [
    ['', 1], [' list', 1], ['list ', 1], ['list', 0], ['list', -1], ['list', 1.5],
    ['list', Number.MAX_SAFE_INTEGER + 1], ['list', { min: 2, max: 1 }],
    ['list', { min: 1, max: Infinity }], ['list', null], ['path/escape', 1],
    ['module.name', 1], ['bad-kind', 1], ['BadKind', 1], ['bad\nkind', 1],
    [`a${'b'.repeat(64)}`, 1],
  ] as Array<[string, SchemaVersionSpec]>) {
    throws(() => createRegistry().register(kind, versions, valid, loader('bad')), `rejects invalid registration ${JSON.stringify([kind, versions])}`);
  }
  throws(
    () => createRegistry().register('list', 1, null as unknown as RendererValidator, loader('bad')),
    'rejects null validator',
  );
  throws(
    () => createRegistry().register('list', 1, valid, null as unknown as LazyLoader),
    'rejects null loader',
  );

  console.log('\n-- exact idempotency and conflicts');
  {
    const registry = createRegistry();
    const validate = () => ({ valid: true });
    const load = loader('same');
    registry.register('list', { min: 1, max: 2 }, validate, load);
    registry.register('list', { min: 1, max: 2 }, validate, load);
    ok(registry.has('list', 2), 'exact repeated registration is idempotent');
    throws(
      () => registry.register('list', { min: 2, max: 3 }, validate, load),
      'same loader with a different overlapping range conflicts',
    );
    throws(
      () => registry.register('list', { min: 1, max: 2 }, valid, load),
      'same range with a different validator conflicts',
    );
    throws(
      () => registry.register('list', 2, validate, loader('other')),
      'overlapping version with a different loader conflicts',
    );
    registry.register('list', 3, validate, loader('v3'));
    registry.register('chart', 1, validate, loader('chart'));
    ok(registry.has('list', 3) && registry.has('chart', 1), 'non-overlapping ranges and kinds coexist');
  }

  console.log('\n-- validate before load');
  {
    const registry = createRegistry();
    let validates = 0;
    let loads = 0;
    registry.register(
      'list',
      1,
      (_schema, data) => {
        validates++;
        return { valid: data === 'ok', reason: 'expected ok' };
      },
      async () => {
        loads++;
        return module('list');
      },
    );
    equal(registry.validate('list', 1, 'bad')?.valid, false, 'validator rejects invalid data synchronously');
    equal(validates, 1, 'validator invoked once');
    equal(loads, 0, 'validation does not lazy-load module');
    equal(registry.validate('ghost', 1, null), null, 'unknown renderer has no validator');
  }

  console.log('\n-- lazy load concurrency, cache, and retry');
  {
    const registry = createRegistry();
    let loads = 0;
    let release!: (value: RendererModule) => void;
    const pending = new Promise<RendererModule>((resolve) => { release = resolve; });
    registry.register('slow', 1, valid, () => {
      loads++;
      return pending;
    });
    const first = registry.resolve('slow', 1);
    const second = registry.resolve('slow', 1);
    await Promise.resolve();
    equal(loads, 1, 'concurrent resolves share one lazy import');
    const loaded = module('slow');
    release(loaded);
    const [one, two] = await Promise.all([first, second]);
    ok(one === loaded && two === loaded, 'concurrent callers receive the same module');
    equal(await registry.resolve('slow', 1), loaded, 'successful module is cached');
    equal(loads, 1, 'cache avoids a second import');
  }
  {
    const registry = createRegistry();
    let loads = 0;
    registry.register('flaky', 1, valid, async () => {
      loads++;
      if (loads === 1) throw new Error('first attempt');
      return module('flaky');
    });
    await rejects(() => registry.resolve('flaky', 1), 'failed import is observable');
    ok(await registry.resolve('flaky', 1) !== null, 'failed import can be retried');
    equal(loads, 2, 'retry starts one new import');
  }
  await rejects(async () => {
    const registry = createRegistry();
    registry.register('invalid', 1, valid, async () => ({} as RendererModule));
    await registry.resolve('invalid', 1);
  }, 'invalid module is rejected');
  await rejects(async () => {
    const registry = createRegistry();
    registry.register('nil', 1, valid, async () => null as unknown as RendererModule);
    await registry.resolve('nil', 1);
  }, 'null module is rejected');

  console.log('\n-- isolation');
  {
    const first = createRegistry();
    const second = createRegistry();
    first.register('list', 1, valid, loader('first'));
    ok(first.has('list', 1) && !second.has('list', 1), 'registry instances do not share entries');
  }

  process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed) process.exit(1);
}

run().catch((error) => {
  console.error('block registry test runner failed', error);
  process.exit(1);
});
