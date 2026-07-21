import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { JSDOM } from 'jsdom';
import React, { act, useEffect, type ReactElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { WorkCard } from '../components/work/WorkCard';
import type { WorkCardBackSlots } from '../components/work/WorkCardBack';
import { WorkFlipControl } from '../components/work/WorkFlipControl';
import type { WorkControllerPort, WorkUIPreference } from '../work/controller';
import { useWorkStore, useWorkUIStore } from '../work/store';
import type { BlockInstance, WorkView, WorkViewEvent } from '../work/types';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else failed++;
}

function eq<T>(actual: T, expected: T, label: string): void {
  ok(actual === expected, `${label}${actual === expected ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function setupDOM(): JSDOM {
  const dom = new JSDOM('<!doctype html><html><body></body></html>', {
    pretendToBeVisual: true,
    url: 'http://localhost/',
  });
  Object.assign(globalThis, {
    IS_REACT_ACT_ENVIRONMENT: true,
    window: dom.window,
    document: dom.window.document,
    Node: dom.window.Node,
    Element: dom.window.Element,
    HTMLElement: dom.window.HTMLElement,
    SVGElement: dom.window.SVGElement,
    Event: dom.window.Event,
    MouseEvent: dom.window.MouseEvent,
    KeyboardEvent: dom.window.KeyboardEvent,
    MutationObserver: dom.window.MutationObserver,
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
  });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
  return dom;
}

const dom = setupDOM();

async function settle(delay = 30): Promise<void> {
  await act(async () => { await new Promise<void>((resolveWait) => setTimeout(resolveWait, delay)); });
}

async function interact(action: () => void): Promise<void> {
  await act(async () => {
    action();
    await new Promise<void>((resolveWait) => setTimeout(resolveWait, 30));
  });
}

interface Mounted {
  host: HTMLDivElement;
  root: Root;
  cleanup: () => Promise<void>;
}

async function mount(element: ReactElement): Promise<Mounted> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host, { onCaughtError: (error) => { throw error; } });
  await act(async () => { root.render(element); });
  await settle();
  return {
    host,
    root,
    cleanup: async () => {
      await act(async () => { root.unmount(); });
      host.remove();
    },
  };
}

function reset(): void {
  useWorkStore.getState().clearAll();
  useWorkUIStore.getState().clearAll();
}

class TestPort implements WorkControllerPort {
  private listeners = new Map<string, (event: WorkViewEvent) => void>();
  readonly preferences = new Map<string, WorkUIPreference>();
  readonly operations: string[] = [];
  preferenceFailures = 0;
  snapshotFailures = 0;

  subscribe(workID: string, onEvent: (event: WorkViewEvent) => void): () => void {
    this.listeners.set(workID, onEvent);
    return () => this.listeners.delete(workID);
  }

  async fetchSnapshot(workID: string): Promise<WorkView> {
    this.operations.push(`snapshot:${workID}`);
    if (this.snapshotFailures-- > 0) throw new Error('snapshot unavailable');
    const view = useWorkStore.getState().works[workID];
    if (!view) throw new Error(`no projection for ${workID}`);
    return view;
  }

  async readUIPreference(workID: string): Promise<WorkUIPreference | null> {
    this.operations.push(`read:${workID}`);
    return this.preferences.get(workID) ?? null;
  }

  async writeUIPreference(workID: string, preference: WorkUIPreference): Promise<void> {
    this.operations.push(`write:${preference.activeFace}`);
    if (this.preferenceFailures-- > 0) throw new Error('preference unavailable');
    this.preferences.set(workID, preference);
  }
}

function makeBlock(id: string): BlockInstance {
  return {
    id,
    kind: 'notice',
    schemaVersion: 1,
    revision: 1,
    status: 'ready',
    title: id,
    data: { text: id },
    source: { provider: 'controller', mode: 'snapshot', verified: true },
    fallback: { summary: id },
    createdAt: '2026-07-20T10:00:00Z',
    updatedAt: '2026-07-20T10:00:00Z',
  };
}

function makeView(workID: string, overrides: Partial<WorkView['work']> = {}): WorkView {
  const blueprintRef = { id: 'blueprint:test', schemaVersion: 1, version: 1 };
  return {
    schemaVersion: 1,
    revision: 1,
    work: {
      schemaVersion: 1,
      id: workID,
      name: workID,
      state: 'ready',
      archiveState: 'active',
      blueprintRef,
      definitionSnapshot: {
        schemaVersion: 1,
        revision: 1,
        blueprintRef,
        promptTemplate: '',
        workflow: { stages: [] },
        blockSpecs: [],
        digest: 'digest',
      },
      blocks: [],
      placements: [],
      prompt: '',
      cornerstones: [],
      runs: [],
      createdWith: { workSchemaVersion: 1, eventSchemaVersion: 1, rendererSetVersion: 1 },
      createdAt: '2026-07-20T10:00:00Z',
      updatedAt: '2026-07-20T10:00:00Z',
      ...overrides,
    },
  };
}

async function testFacesAndFixedWorkspace(): Promise<void> {
  reset();
  const view = makeView('work-faces', { state: 'running', blocks: [makeBlock('block-a')] });
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort();
  const mounts: Record<string, number> = {};
  const unmounts: Record<string, number> = {};
  const Probe = ({ name }: { name: string }) => {
    useEffect(() => {
      mounts[name] = (mounts[name] ?? 0) + 1;
      return () => { unmounts[name] = (unmounts[name] ?? 0) + 1; };
    }, [name]);
    return <div data-probe={name}>{name}</div>;
  };
  const backSlots: WorkCardBackSlots = {
    transcript: <Probe name="transcript" />,
    runApproval: <Probe name="run" />,
    artifactShelf: <Probe name="artifact" />,
    queue: <Probe name="queue" />,
    composer: <Probe name="composer" />,
  };
  const mounted = await mount(
    <WorkCard
      workID="work-faces"
      port={port}
      backSlots={backSlots}
      cornerstoneEntry={<Probe name="cornerstone" />}
      addonPanel={<Probe name="addon" />}
    />,
  );

  const front = mounted.host.querySelector<HTMLElement>('[data-testid="work-face-front"]')!;
  const back = mounted.host.querySelector<HTMLElement>('[data-testid="work-face-back"]')!;
  const outer = mounted.host.querySelector('[data-testid="work-outer-header"]');
  const transcript = mounted.host.querySelector('[data-probe="transcript"]');
  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  ok(Boolean(outer && front && back && transcript), 'workspace and both real faces render');
  eq(flip.textContent?.trim(), '会话', 'front flip entry says 会话');
  eq(flip.getAttribute('aria-controls'), back.id, 'flip control references target face');
  eq(front.getAttribute('aria-hidden'), 'false', 'front is exposed');
  ok(back.hasAttribute('inert'), 'inactive back is inert');

  await interact(() => flip.click());
  eq(useWorkUIStore.getState().cardByWork['work-faces'].activeFace, 'back', 'flip updates UI preference only');
  eq(useWorkStore.getState().works['work-faces'].work.state, 'running', 'running Work is not interrupted');
  eq(mounted.host.querySelector('[data-testid="work-outer-header"]'), outer, 'outer workspace keeps DOM identity');
  eq(mounted.host.querySelector('[data-probe="transcript"]'), transcript, 'session surface keeps DOM identity');
  eq(mounts.transcript, 1, 'session surface mounts once');
  eq(unmounts.transcript ?? 0, 0, 'session surface does not unmount on flip');
  eq(mounts.addon, 1, 'AddOn mounts once outside flip');
  eq(unmounts.addon ?? 0, 0, 'AddOn does not unmount on flip');
  ok(front.hasAttribute('inert'), 'inactive front becomes inert');
  ok(!back.hasAttribute('inert'), 'active back is interactive');
  eq(flip.textContent?.trim(), '工作流', 'back flip entry says 工作流');
  eq(port.preferences.get('work-faces')?.activeFace, 'back', 'last face is persisted by Work ID');
  await mounted.cleanup();
}

async function testScrollExpandedAndDraft(): Promise<void> {
  reset();
  const view = makeView('work-local', {
    blocks: [makeBlock('block-local')],
    placements: [{ blockId: 'block-local', slot: 'primary', order: 0, collapsed: true }],
  });
  useWorkStore.getState().applySnapshot(view);
  const ui = useWorkUIStore.getState();
  ui.ensureCard('work-local');
  ui.setScroll('work-local', 'front', { scrollTop: 37, scrollLeft: 4 });
  ui.setScroll('work-local', 'back', { scrollTop: 91, scrollLeft: 7 });
  ui.setExpanded('work-local', 'front', 'block-local', true);
  ui.setDraft('work-local', 'back', 'saved draft');
  const slots: WorkCardBackSlots = {
    transcript: <div>Transcript</div>,
    composer: ({ draft, onDraftChange }) => (
      <button type="button" data-testid="draft-editor" onClick={() => onDraftChange('changed draft')}>{draft}</button>
    ),
  };
  const mounted = await mount(<WorkCard workID="work-local" port={new TestPort()} backSlots={slots} />);
  const front = mounted.host.querySelector<HTMLElement>('[data-testid="work-face-front"]')!;
  const back = mounted.host.querySelector<HTMLElement>('[data-testid="work-face-back"]')!;
  eq(front.scrollTop, 37, 'front scroll restores from per-Work UI state');
  eq(front.scrollLeft, 4, 'front horizontal scroll restores');
  eq(back.scrollTop, 91, 'back scroll restores while mounted inactive');
  eq(back.scrollLeft, 7, 'back horizontal scroll restores');
  eq(mounted.host.querySelector('[data-testid="draft-editor"]')?.textContent, 'saved draft', 'draft restores into existing Composer adapter');
  eq(mounted.host.querySelector('[data-testid="work-block-block-local"]')?.classList.contains('wg2-work-block-expanded'), true, 'expanded state restores');

  await interact(() => {
    front.scrollTop = 144;
    front.scrollLeft = 9;
    front.dispatchEvent(new dom.window.Event('scroll', { bubbles: true }));
  });
  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  await interact(() => flip.click());
  await interact(() => {
    back.scrollTop = 288;
    back.scrollLeft = 11;
    back.dispatchEvent(new dom.window.Event('scroll', { bubbles: true }));
    mounted.host.querySelector<HTMLButtonElement>('[data-testid="draft-editor"]')!.click();
  });
  await interact(() => flip.click());
  const local = useWorkUIStore.getState().cardByWork['work-local'];
  eq(local.faces.front.scroll.scrollTop, 144, 'front scroll writes to its own face state');
  eq(local.faces.back.scroll.scrollTop, 288, 'back scroll writes to its own face state');
  eq(local.faces.back.draft, 'changed draft', 'Composer draft survives face switching');
  eq(front.scrollTop, 144, 'front DOM scroll is unchanged after round trip');
  eq(local.faces.front.expanded['block-local'], true, 'expanded state survives face switching');
  await mounted.cleanup();
}

async function testDeepLinkOrderAndMissingReason(): Promise<void> {
  reset();
  const view = makeView('work-deep');
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort();
  port.preferences.set('work-deep', { activeFace: 'front' });
  const operations = port.operations;
  const slots: WorkCardBackSlots = {
    transcript: <button type="button" data-work-target-id="message-7" onFocus={() => operations.push('focus:message-7')}>message</button>,
  };
  const mounted = await mount(
    <WorkCard workID="work-deep" port={port} backSlots={slots} deepLink={{ face: 'back', targetID: 'message-7' }} />,
  );
  await settle();
  eq(useWorkUIStore.getState().cardByWork['work-deep'].activeFace, 'back', 'deep link switches to requested face');
  eq((document.activeElement as HTMLElement | null)?.dataset.workTargetId, 'message-7', 'deep link focuses requested target');
  const readIndex = operations.indexOf('read:work-deep');
  const writeIndex = operations.indexOf('write:back');
  const focusIndex = operations.indexOf('focus:message-7');
  ok(readIndex >= 0 && readIndex < writeIndex && writeIndex < focusIndex, 'preference restore, face switch, and focus happen in order');
  await mounted.cleanup();

  reset();
  useWorkStore.getState().applySnapshot(makeView('work-late'));
  const late = await mount(
    <WorkCard
      workID="work-late"
      port={new TestPort()}
      backSlots={{ transcript: <div data-testid="late-transcript" /> }}
      deepLink={{ face: 'back', targetID: 'late-message' }}
    />,
  );
  const lateTarget = document.createElement('button');
  lateTarget.dataset.workTargetId = 'late-message';
  late.host.querySelector('[data-testid="late-transcript"]')!.appendChild(lateTarget);
  await settle();
  eq(document.activeElement, lateTarget, 'deep link recovers when session target hydrates late');
  ok(!late.host.querySelector('[data-testid="work-deeplink-missing"]'), 'late target does not produce a stale-target error');
  await late.cleanup();

  reset();
  useWorkStore.getState().applySnapshot(makeView('work-missing', { blocks: [makeBlock('known')] }));
  const missing = await mount(
    <WorkCard workID="work-missing" port={new TestPort()} deepLink={{ face: 'front', targetID: 'removed' }} />,
  );
  await settle();
  const alert = missing.host.querySelector('[data-testid="work-deeplink-missing"]');
  ok(Boolean(alert), 'invalid deep link exposes a visible reason');
  ok(alert?.textContent?.includes('不存在') ?? false, 'invalid deep link explains that target is gone');
  ok(Boolean(missing.host.querySelector('[data-testid="work-card-front"]')), 'invalid deep link keeps a readable Work overview');
  await missing.cleanup();
}

async function testPreferenceFailureRetry(): Promise<void> {
  reset();
  useWorkStore.getState().applySnapshot(makeView('work-pref'));
  const port = new TestPort();
  port.preferenceFailures = 1;
  const mounted = await mount(<WorkCard workID="work-pref" port={port} />);
  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  await interact(() => flip.click());
  eq(useWorkUIStore.getState().cardByWork['work-pref'].activeFace, 'back', 'failed persistence does not roll back local face');
  const error = mounted.host.querySelector<HTMLElement>('[data-testid="work-pref-error"]');
  ok(Boolean(error && error.textContent?.includes('preference unavailable')), 'preference failure is explicit');
  await interact(() => error.querySelector<HTMLButtonElement>('button')!.click());
  ok(!mounted.host.querySelector('[data-testid="work-pref-error"]'), 'preference retry clears the visible failure');
  eq(port.preferences.get('work-pref')?.activeFace, 'back', 'preference retry persists current face idempotently');
  await mounted.cleanup();
}

async function testArchiveAndUnavailableSession(): Promise<void> {
  reset();
  useWorkStore.getState().applySnapshot(makeView('work-archive', { archiveState: 'archived', state: 'completed' }));
  const mounted = await mount(
    <WorkCard
      workID="work-archive"
      port={new TestPort()}
      backSlots={{ transcript: <div data-testid="archive-transcript">history</div>, composer: <div data-testid="archive-composer" /> }}
    />,
  );
  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  ok(!flip.disabled, 'archived Work remains flippable');
  await interact(() => flip.click());
  ok(Boolean(mounted.host.querySelector('[data-testid="archive-transcript"]')), 'archived Work can browse existing transcript');
  ok(!mounted.host.querySelector('[data-testid="archive-composer"]'), 'archived Work does not expose Composer');
  ok(Boolean(mounted.host.querySelector('[data-testid="work-back-readonly-notice"]')), 'archive readonly state is visible');
  await mounted.cleanup();

  reset();
  useWorkStore.getState().applySnapshot(makeView('work-no-session'));
  const unavailable = await mount(<WorkCard workID="work-no-session" port={new TestPort()} />);
  ok(Boolean(unavailable.host.querySelector('[data-testid="work-session-unavailable"]')), 'missing session integration fails visibly');
  ok(!unavailable.host.querySelector('[data-testid^="work-back-placeholder-"]'), 'no parallel placeholder chat surfaces are created');
  await unavailable.cleanup();
}

async function testPlacementAndFlipAccessibility(): Promise<void> {
  reset();
  useWorkStore.getState().applySnapshot(makeView('work-order', {
    blocks: [makeBlock('later'), makeBlock('first')],
    placements: [
      { blockId: 'later', slot: 'primary', order: 2 },
      { blockId: 'first', slot: 'attention', order: 1 },
    ],
  }));
  const mounted = await mount(<WorkCard workID="work-order" port={new TestPort()} />);
  const blocks = [...mounted.host.querySelectorAll<HTMLElement>('[data-block-id]')].map((node) => node.dataset.blockId);
  eq(blocks.join(','), 'first,later', 'front honors placement order and passes placement to BlockHost');
  const button = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  eq(button.tagName, 'BUTTON', 'flip entry uses native keyboard semantics');
  eq(button.type, 'button', 'flip entry cannot submit an enclosing form');
  button.focus();
  eq(document.activeElement, button, 'flip entry is keyboard focusable');
  await mounted.cleanup();

  let target: string | undefined;
  const control = await mount(<WorkFlipControl activeFace="front" onFlip={(face) => { target = face; }} />);
  await interact(() => control.host.querySelector<HTMLButtonElement>('button')!.click());
  eq(target, 'back', 'native activation emits the target face once');
  await control.cleanup();
}

function testMotionCSSContract(): void {
  const css = readFileSync(resolve(process.cwd(), 'src/styles.css'), 'utf8');
  ok(/\.wg2-work-face\s*\{[\s\S]*?transition:\s*transform 240ms ease/.test(css), 'standard face transition is 240ms');
  ok(/prefers-reduced-motion:\s*reduce[\s\S]*?transition:\s*opacity 150ms ease/.test(css), 'reduced motion uses a fade');
  ok(/max-width:\s*480px[\s\S]*?\.wg2-work-face\s*\{[\s\S]*?transition:\s*none/.test(css), 'narrow layout uses an ordinary switch');
}

async function testUnknownWorkRetry(): Promise<void> {
  reset();
  const port = new TestPort();
  port.snapshotFailures = 1;
  const mounted = await mount(<WorkCard workID="unknown" port={port} />);
  ok(Boolean(mounted.host.querySelector('[data-testid="work-card-unknown"]')), 'unknown Work has a readable fallback');
  ok(Boolean(mounted.host.querySelector('button')), 'unknown Work exposes retry');
  await mounted.cleanup();
}

async function main(): Promise<void> {
  console.log('\nWorkCard M1 Tests\n');
  await testFacesAndFixedWorkspace();
  await testScrollExpandedAndDraft();
  await testDeepLinkOrderAndMissingReason();
  await testPreferenceFailureRetry();
  await testArchiveAndUnavailableSession();
  await testPlacementAndFlipAccessibility();
  testMotionCSSContract();
  await testUnknownWorkRetry();
  console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total\n`);
  if (failed > 0) process.exit(1);
}

void main().catch((error) => {
  console.error(error);
  process.exit(1);
});
