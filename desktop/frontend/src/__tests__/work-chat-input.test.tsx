import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { WorkChatInput, type WorkChatInputProps } from '../components/work/WorkChatInput';
import { WorkControlBar } from '../components/work/WorkControlBar';
import type { Item, LiveStream } from '../lib/useController';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else failed++;
}

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
  HTMLTextAreaElement: dom.window.HTMLTextAreaElement,
  Event: dom.window.Event,
  KeyboardEvent: dom.window.KeyboardEvent,
  CompositionEvent: dom.window.CompositionEvent,
  MouseEvent: dom.window.MouseEvent,
  requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
  cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
});
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
Object.assign(dom.window.HTMLElement.prototype, {
  attachEvent: () => undefined,
  detachEvent: () => undefined,
});

interface Harness {
  host: HTMLElement;
  root: Root;
  rerender: (props: Partial<WorkChatInputProps>) => Promise<void>;
  unmount: () => Promise<void>;
}

async function mount(overrides: Partial<WorkChatInputProps> = {}): Promise<Harness> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  let props: WorkChatInputProps = {
    disabled: false,
    composerSubmitKey: 'enter',
    onSend: () => undefined,
    displayItems: [],
    running: false,
    ...overrides,
  };
  const rerender = async (next: Partial<WorkChatInputProps>) => {
    props = { ...props, ...next };
    await act(async () => {
      root.render(<WorkChatInput {...props} />);
      await Promise.resolve();
    });
  };
  await rerender({});
  return {
    host,
    root,
    rerender,
    unmount: async () => {
      await act(async () => { root.unmount(); });
      host.remove();
    },
  };
}

function textarea(host: HTMLElement): HTMLTextAreaElement {
  const node = host.querySelector<HTMLTextAreaElement>('[data-testid="work-chat-textarea"]');
  if (!node) throw new Error('missing Work chat textarea');
  return node;
}

function reactProps<T extends Record<string, unknown>>(node: Element): T {
  const key = Object.keys(node).find((candidate) => candidate.startsWith('__reactProps$'));
  if (!key) throw new Error('missing React props');
  return (node as unknown as Record<string, T>)[key];
}

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
  });
}

async function type(host: HTMLElement, value: string): Promise<void> {
  const input = textarea(host);
  const setter = Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype, 'value')?.set;
  await act(async () => {
    setter?.call(input, value);
    reactProps<{ onChange: (event: { target: HTMLTextAreaElement }) => void }>(input).onChange({ target: input });
  });
}

async function key(host: HTMLElement, value: string, init: KeyboardEventInit = {}): Promise<void> {
  const input = textarea(host);
  await act(async () => {
    reactProps<{ onKeyDown: (event: {
      key: string;
      shiftKey: boolean;
      ctrlKey: boolean;
      metaKey: boolean;
      altKey: boolean;
      preventDefault: () => void;
    }) => void }>(input).onKeyDown({
      key: value,
      shiftKey: Boolean(init.shiftKey),
      ctrlKey: Boolean(init.ctrlKey),
      metaKey: Boolean(init.metaKey),
      altKey: Boolean(init.altKey),
      preventDefault: () => undefined,
    });
    await Promise.resolve();
  });
  await flush();
}

async function click(node: HTMLButtonElement | null): Promise<void> {
  if (!node) throw new Error('missing button');
  await act(async () => {
    reactProps<{ onClick: () => void | Promise<void> }>(node).onClick();
    await Promise.resolve();
  });
  await flush();
}

function assistant(id: string, text: string, streaming = false): Item {
  return { kind: 'assistant', id, text, reasoning: '', streaming };
}

async function main(): Promise<void> {
  process.stdout.write('\nWork chat input\n');

  {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <div className="wg2-work-bottom" data-testid="work-bottom">
          <WorkChatInput
            disabled={false}
            composerSubmitKey="enter"
            onSend={() => undefined}
            displayItems={[]}
            running={false}
          />
          <WorkControlBar
            workId="work-1"
            workState="draft"
            runs={[]}
            readonly={false}
            archived={false}
          />
        </div>,
      );
    });
    const bottom = host.querySelector('[data-testid="work-bottom"]');
    ok(bottom?.children[0]?.getAttribute('data-testid') === 'work-chat-input', 'chat is the first independent bottom region');
    ok(bottom?.children[1]?.getAttribute('data-testid') === 'work-control-bar', 'controls are a separate sibling region');
    ok(!host.textContent?.includes('工作怎么调整'), 'legacy adjustment prompt is removed');
    await act(async () => { root.unmount(); });
    host.remove();
  }

  {
    const sent: string[] = [];
    const h = await mount({ onSend: async (text) => { sent.push(text); } });
    await type(h.host, '排查为什么运行失败');
    await key(h.host, 'Enter');
    ok(sent[0] === '排查为什么运行失败', 'Enter uses the configured send action');
    ok(textarea(h.host).value === '', 'successful send clears the draft');
    ok(h.host.querySelector('[data-testid="work-chat-reply"]')?.textContent?.includes('等待回复中') ?? false, 'send opens a waiting reply popup');
    await h.unmount();
  }

  {
    const sent: string[] = [];
    const h = await mount({ composerSubmitKey: 'ctrl_enter', onSend: (text) => { sent.push(text); } });
    await type(h.host, '保留换行');
    await key(h.host, 'Enter');
    await key(h.host, 'Enter', { shiftKey: true });
    ok(sent.length === 0, 'plain and Shift+Enter do not send in Ctrl+Enter mode');
    await key(h.host, 'Enter', { ctrlKey: true });
    ok(sent.length === 1, 'Ctrl+Enter sends in Ctrl+Enter mode');
    await h.unmount();
  }

  {
    let sends = 0;
    const h = await mount({ onSend: () => { sends++; } });
    await type(h.host, '中文输入');
    await act(async () => {
      reactProps<{ onCompositionStart: () => void }>(textarea(h.host)).onCompositionStart();
    });
    await key(h.host, 'Enter');
    ok(sends === 0, 'IME Enter never submits');
    await act(async () => {
      reactProps<{ onCompositionEnd: () => void }>(textarea(h.host)).onCompositionEnd();
    });
    await click(h.host.querySelector<HTMLButtonElement>('[data-testid="work-chat-send"]'));
    ok(sends === 1, 'the dedicated send button submits');
    await h.unmount();
  }

  {
    const h = await mount({ onSend: async () => { throw new Error('发送失败，请重试'); } });
    await type(h.host, '不要丢失');
    await click(h.host.querySelector<HTMLButtonElement>('[data-testid="work-chat-send"]'));
    ok(textarea(h.host).value === '不要丢失', 'failed send preserves the draft');
    ok(h.host.querySelector('[role="alert"]')?.textContent === '发送失败，请重试', 'failed send is visible');
    await h.unmount();
  }

  {
    const old = assistant('old', '旧回复');
    const h = await mount({ displayItems: [old], onSend: () => undefined });
    ok(!h.host.querySelector('[data-testid="work-chat-reply"]'), 'mounting does not pop an old reply');
    await type(h.host, '新问题');
    await key(h.host, 'Enter');
    await h.rerender({ displayItems: [old, assistant('new', '正在排查', true)] });
    ok(h.host.querySelector('[data-testid="work-chat-reply-body"]')?.textContent === '正在排查', 'new assistant reply replaces the waiting state');
    await click(h.host.querySelector<HTMLButtonElement>('[aria-label="收起回复"]'));
    const live: LiveStream = { id: 'new', text: '正在排查，发现原因', reasoning: '', reasoningComplete: true };
    await h.rerender({ live });
    ok(!h.host.querySelector('[data-testid="work-chat-reply"]'), 'stream updates respect manual collapse');
    ok(Boolean(h.host.querySelector('[data-testid="work-chat-reply-peek"]')), 'collapsed reply can be expanded again');
    await click(h.host.querySelector<HTMLButtonElement>('[data-testid="work-chat-reply-peek"]'));
    const replyBody = h.host.querySelector<HTMLElement>('[data-testid="work-chat-reply-body"]');
    ok(replyBody?.textContent === live.text, 'expanded popup shows the latest streamed reply');
    ok(replyBody?.style.maxHeight === '', 'expanded popup height follows rendered content instead of newline count');
    await h.unmount();
  }

  // ── Regression: input stays enabled when running (e.g. Work waiting_input) ──
  {
    const h = await mount({ running: true, disabled: false });
    ok(!textarea(h.host).disabled, "textarea is enabled when running without disabled flag");
    const sendBtn = h.host.querySelector<HTMLButtonElement>('[data-testid="work-chat-send"]');
    ok(sendBtn !== null, "send button exists");
    await type(h.host, '继续讨论');
    ok(!textarea(h.host).disabled, "textarea remains enabled after typing while running");
    ok(reactProps<{ disabled: boolean }>(sendBtn!).disabled === false,
      "send button is enabled after typing while running");
    await h.unmount();
  }

  // ── Regression: disabled prop locks input even when running ──
  {
    const h = await mount({ running: false, disabled: true });
    ok(textarea(h.host).disabled, "textarea is disabled when disabled prop is true");
    ok(h.host.querySelector('[data-testid="work-chat-send"]')?.getAttribute('disabled') !== null,
      "send button is disabled when disabled prop is true");
    await h.unmount();
  }

  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

void main().catch((error) => {
  console.error(error);
  process.exit(1);
});
