import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { sha256 } from '../../lib/attachDedup';
import { useT } from '../../lib/i18n';
import type { CornerstoneControllerPort } from '../../work/controller';
import { deriveCornerstoneAttention, useCornerstoneUIStore } from '../../work/cornerstoneStore';
import type {
  Cornerstone,
  CornerstoneDrawerUI,
  CornerstoneItemUI,
  CornerstoneMutationResult,
  CornerstoneRef,
  CornerstoneRetry,
  CornerstoneType,
  CornerstoneUIAction,
  WorkView,
} from '../../work/types';

const TYPE_LABELS: Record<CornerstoneType, string> = {
  instruction: '说明',
  file_ref: '文件引用',
  file_snapshot: '文件快照',
  decision: '决策',
  conclusion: '结论',
  source: '来源',
  policy: '策略',
  parameter: '参数',
};

const STATUS_LABELS: Record<Cornerstone['status'], string> = {
  active: '有效',
  stale: '有新版本',
  missing: '来源缺失',
  denied: '权限不足',
  invalid: '无效',
};

const EMPTY_DRAWER: CornerstoneDrawerUI = {
  byId: {},
  open: false,
  filterType: 'all',
  filterRequired: null,
};

const REF_KINDS: CornerstoneRef['kind'][] = ['inline', 'session_turn', 'workspace_file', 'artifact', 'url'];

type RepairContentCheck =
  | { ok: true; content: string }
  | { ok: false; message: string };

function normalizeRepairContent(content: string): string {
  return content.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
}

function secretRef(value: string): boolean {
  const lower = value.trim().toLowerCase();
  return lower.startsWith('${') || lower.startsWith('{{') || lower.startsWith('vault:')
    || lower.startsWith('secretref:') || lower.startsWith('secret-ref:')
    || lower.startsWith('$secret.') || lower.startsWith('ref:');
}

function hasSecretLikeContent(content: string): boolean {
  if (/(?:^|[^a-z0-9_])(?:sk-|ghp_|github_pat_|xoxb-)[a-z0-9_=-]{12,}/i.test(content)) return true;
  if (/(?:^|[^A-Za-z0-9_])AKIA[A-Z0-9]{16}(?:$|[^A-Za-z0-9_])/.test(content)) return true;
  if (/-----BEGIN [^-\r\n]*PRIVATE KEY-----/.test(content)) return true;
  if (/(?:^|[^A-Za-z0-9_])eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_=-]{12,}/.test(content)) return true;
  const auth = content.match(/authorization[ \t]*[:=][ \t]*["']?(?:bearer|basic)[ \t]+([^ \t\r\n"',;]+)/i);
  if (auth?.[1] && !secretRef(auth[1])) return true;
  const assignment = /(?:api[_-]?key|secret|token|password|passwd|credential|private[_-]?key|access[_-]?key)[ \t]*([=:])[ \t]*/ig;
  for (let match = assignment.exec(content); match; match = assignment.exec(content)) {
    let value = content.slice(assignment.lastIndex).split(/[;\r\n]/, 1)[0].trim();
    const quote = value[0] === '"' || value[0] === "'" ? value[0] : '';
    if (quote) value = value.slice(1).split(quote, 1)[0].trim();
    if (value && !secretRef(value) && (match[1] === '=' || !!quote || !/[ \t]/.test(value))) return true;
  }
  return false;
}

async function checkRepairContent(content: string, acceptedDigest: string): Promise<RepairContentCheck> {
  const normalized = normalizeRepairContent(content);
  if (normalized.length === 0) return { ok: false, message: '请输入快照原始内容。' };
  if (hasSecretLikeContent(normalized)) {
    return { ok: false, message: '输入疑似包含敏感凭据，未发送；请改用 Secret 引用。' };
  }
  if (!/^sha256:[0-9a-f]{64}$/.test(acceptedDigest)) {
    return { ok: false, message: '已接受的 digest 格式无效，请刷新 Work 后重试。' };
  }
  const digest = await sha256(new Blob([normalized], { type: 'text/plain;charset=utf-8' }));
  if (!digest) return { ok: false, message: '当前环境无法校验内容 digest，未发送。' };
  if (`sha256:${digest}` !== acceptedDigest) {
    return { ok: false, message: '内容 digest 与已接受快照不匹配，未发送。' };
  }
  return { ok: true, content: normalized };
}

function requestId(action: CornerstoneUIAction): string {
  const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `cornerstone-${action}-${suffix}`;
}

function shortDigest(digest: string): string {
  return digest ? digest.slice(0, 12) : '—';
}

function refLabel(ref: CornerstoneRef): string {
  switch (ref.kind) {
    case 'inline': return '内联内容';
    case 'session_turn': return '会话片段';
    case 'workspace_file': return '工作区文件';
    case 'artifact': return 'Artifact';
    case 'url': return 'URL';
  }
}

function refValue(ref: CornerstoneRef): string {
  switch (ref.kind) {
    case 'session_turn': return `${ref.sessionId ?? ''}${ref.turn === undefined ? '' : `#${ref.turn}`}`;
    case 'workspace_file': return ref.path ?? '';
    case 'artifact': return ref.artifactId ?? '';
    case 'url': return ref.url ?? '';
    default: return '';
  }
}

function makeRef(kind: CornerstoneRef['kind'], value: string): CornerstoneRef | null {
  const trimmed = value.trim();
  if (kind === 'inline') return { kind };
  if (!trimmed) return null;
  switch (kind) {
    case 'workspace_file': return { kind, path: trimmed };
    case 'artifact': return { kind, artifactId: trimmed };
    case 'url': return { kind, url: trimmed };
    case 'session_turn': {
      const [sessionId, turnText] = trimmed.split('#', 2);
      const turn = turnText === undefined ? undefined : Number(turnText);
      if (!sessionId || (turn !== undefined && !Number.isSafeInteger(turn))) return null;
      return { kind, sessionId, turn };
    }
    default: return null;
  }
}

function statusHint(cornerstone: Cornerstone): string | null {
  if (cornerstone.tombstone) return '已移除，可撤销恢复。';
  switch (cornerstone.status) {
    case 'stale': return '已解析到新版本；接受前仍使用上次确认内容。';
    case 'missing': return '来源不可达，请修复引用或冻结最后已知内容。';
    case 'denied': return '当前无权读取来源，请修复权限或引用。';
    case 'invalid': return '内容或引用校验失败。';
    default: return null;
  }
}

export interface CornerstoneDrawerProps {
  workId: string;
  view: WorkView;
  port: CornerstoneControllerPort;
  readonly?: boolean;
  /** The owning WorkControllerAdapter is the sole business-store entry. */
  onApplyMutationResult: (result: CornerstoneMutationResult) => Promise<void>;
}

export const CornerstoneDrawer: React.FC<CornerstoneDrawerProps> = ({
  workId,
  view,
  port,
  readonly = false,
  onApplyMutationResult,
}) => {
  const t = useT();
  const drawer = useCornerstoneUIStore((state) => state.byWork[workId] ?? EMPTY_DRAWER);
  const ui = useCornerstoneUIStore;
  const openerRef = useRef<HTMLButtonElement>(null);
  const focusTimerRef = useRef<number | null>(null);
  const [newType, setNewType] = useState<CornerstoneType>('instruction');
  const [newTitle, setNewTitle] = useState('');
  const [newContent, setNewContent] = useState('');
  const [newMode, setNewMode] = useState<'live_ref' | 'snapshot'>('snapshot');
  const [newRefKind, setNewRefKind] = useState<CornerstoneRef['kind']>('inline');
  const [newRefValue, setNewRefValue] = useState('');
  const [newRequired, setNewRequired] = useState(false);
  const [newTags, setNewTags] = useState('');

  const work = view.work;
  const attention = useMemo(() => deriveCornerstoneAttention(view), [view]);
  const filtered = useMemo(() => work.cornerstones.filter((cornerstone) => {
    if (drawer.filterType !== 'all' && cornerstone.type !== drawer.filterType) return false;
    if (drawer.filterRequired !== null && cornerstone.required !== drawer.filterRequired) return false;
    return true;
  }), [drawer.filterRequired, drawer.filterType, work.cornerstones]);

  const closeDrawer = useCallback(() => {
    if (!ui.getState().byWork[workId]?.open) return;
    ui.getState().setOpen(workId, false);
    if (focusTimerRef.current !== null) window.clearTimeout(focusTimerRef.current);
    focusTimerRef.current = window.setTimeout(() => {
      focusTimerRef.current = null;
      openerRef.current?.focus();
    }, 0);
  }, [ui, workId]);

  useEffect(() => {
    if (!drawer.open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      event.stopPropagation();
      closeDrawer();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [closeDrawer, drawer.open]);

  useEffect(() => () => {
    if (focusTimerRef.current !== null) window.clearTimeout(focusTimerRef.current);
  }, []);

  const execute = useCallback(async (
    cornerstoneId: string,
    action: CornerstoneUIAction,
    invoke: ((context: { requestId: string; expectedRevision: number }) => ReturnType<NonNullable<CornerstoneControllerPort['refreshCornerstone']>>) | undefined,
    retry?: CornerstoneRetry | null,
  ): Promise<boolean> => {
    const store = ui.getState();
    if (store.byWork[workId]?.byId[cornerstoneId]?.pendingAction) return false;
    if (!invoke) {
      store.setError(workId, cornerstoneId, '该操作尚未连接到 Work Controller。');
      return false;
    }

    const context = retry ?? {
      action,
      requestId: requestId(action),
      expectedRevision: view.revision,
    };
    store.setPending(workId, cornerstoneId, action, context.requestId);
    store.setError(workId, cornerstoneId, null);
    try {
      const result = await invoke(context);
      await onApplyMutationResult(result);
      if (result.ok) {
        const latest = ui.getState();
        latest.setConflict(workId, cornerstoneId, null);
        latest.setRetry(workId, cornerstoneId, null);
        return true;
      }

      const latest = ui.getState();
      if (result.error.kind === 'revision_conflict') {
        latest.setConflict(workId, cornerstoneId, result.error.latestSnapshot ?? null);
        latest.setError(workId, cornerstoneId, '版本冲突：已加载最新状态，草稿仍保留，可重试。');
        latest.setRetry(workId, cornerstoneId, {
          action,
          requestId: requestId(action),
          expectedRevision: result.error.actualRevision,
        });
      } else {
        latest.setError(
          workId,
          cornerstoneId,
          result.error.retryable ? '网络请求失败，保留原请求标识，可安全重试。' : '该操作当前不可用。',
        );
        latest.setRetry(
          workId,
          cornerstoneId,
          result.error.retryable
            ? { action, requestId: context.requestId, expectedRevision: context.expectedRevision }
            : null,
        );
      }
      return false;
    } catch {
      const latest = ui.getState();
      latest.setError(workId, cornerstoneId, '请求失败，状态未确认；可使用同一请求标识重试。');
      latest.setRetry(workId, cornerstoneId, { action, requestId: context.requestId, expectedRevision: context.expectedRevision });
      return false;
    } finally {
      ui.getState().setPending(workId, cornerstoneId, null, null);
    }
  }, [onApplyMutationResult, ui, view.revision, workId]);

  const handlePin = useCallback(async (retry?: CornerstoneRetry | null) => {
    const title = newTitle.trim();
    const ref = makeRef(newRefKind, newRefValue);
    const store = ui.getState();
    store.ensureItem(workId, '__new__');
    if (!title || !ref || (newMode === 'snapshot' && newRefKind === 'inline' && !newContent.trim())) {
      store.setError(workId, '__new__', '请填写标题、有效引用；内联快照还需要内容。');
      return;
    }
    const succeeded = await execute('__new__', 'pin', port.pinCornerstone && ((context) => port.pinCornerstone!({
      workId,
      type: newType,
      title,
      content: newContent,
      ref,
      mode: newMode,
      required: newRequired,
      tags: newTags.split(',').map((tag) => tag.trim()).filter(Boolean),
      ...context,
    })), retry);
    if (succeeded) {
      setNewTitle('');
      setNewContent('');
      setNewRefValue('');
      setNewTags('');
    }
  }, [execute, newContent, newMode, newRefKind, newRefValue, newRequired, newTags, newTitle, newType, port, ui, workId]);

  const handleAction = useCallback(async (
    cornerstone: Cornerstone,
    action: Exclude<CornerstoneUIAction, 'pin'>,
    ref?: CornerstoneRef,
    retry?: CornerstoneRetry | null,
  ) => {
    const base = (context: { requestId: string; expectedRevision: number }) => ({
      workId,
      cornerstoneId: cornerstone.id,
      ...context,
    });
    switch (action) {
      case 'refresh':
        await execute(cornerstone.id, action, port.refreshCornerstone && ((context) => port.refreshCornerstone!(base(context))), retry);
        break;
      case 'validate': {
        const validate = port.validateCornerstone ?? port.refreshCornerstone;
        await execute(cornerstone.id, action, validate && ((context) => validate(base(context))), retry);
        break;
      }
      case 'freeze':
        await execute(cornerstone.id, action, port.freezeCornerstone && ((context) => port.freezeCornerstone!({ ...base(context), useLastKnown: cornerstone.status !== 'active' })), retry);
        break;
      case 'accept':
        await execute(cornerstone.id, action, port.acceptCornerstone && ((context) => port.acceptCornerstone!(base(context))), retry);
        break;
      case 'repair': {
        const itemUI = ui.getState().byWork[workId]?.byId[cornerstone.id];
        const isSnapshotBlobRepair = cornerstone.mode === 'snapshot' && !!cornerstone.ref.blobDigest;
        let repairContent: string | undefined;
        if (isSnapshotBlobRepair) {
          const checked = await checkRepairContent(itemUI?.draftContent ?? '', cornerstone.digest);
          if (!checked.ok) {
            const store = ui.getState();
            store.setError(workId, cornerstone.id, checked.message);
            store.setRetry(workId, cornerstone.id, null);
            return;
          }
          repairContent = checked.content;
        }
        const repairRef = isSnapshotBlobRepair ? undefined : (ref || undefined);
        const succeeded = await execute(cornerstone.id, action, port.repairCornerstone && ((context) => port.repairCornerstone!({
          ...base(context),
          ref: repairRef,
          content: repairContent,
        })), retry);
        if (succeeded && isSnapshotBlobRepair) ui.getState().setDraft(workId, cornerstone.id, { content: null });
        break;
      }
      case 'remove':
        await execute(cornerstone.id, action, port.removeCornerstone && ((context) => port.removeCornerstone!(base(context))), retry);
        break;
      case 'undo':
        await execute(cornerstone.id, action, port.undoCornerstone && ((context) => port.undoCornerstone!(base(context))), retry);
        break;
    }
  }, [execute, port, workId]);

  const newItem = drawer.byId.__new__;
  const blocked = Boolean(view.runBlock?.blocked || view.assessment?.blocking);
  const degraded = Boolean(view.assessment?.degraded);
  const hasAttention = blocked || degraded;

  return (
    <div className={`cornerstone-drawer${drawer.open ? ' cornerstone-drawer--open' : ''}`} role="complementary" aria-label="Cornerstone Drawer" data-testid="cornerstone-drawer">
      <button
        ref={openerRef}
        type="button"
        className="cornerstone-drawer__toggle"
        onClick={() => drawer.open ? closeDrawer() : ui.getState().setOpen(workId, true)}
        aria-expanded={drawer.open}
        aria-controls={`cornerstone-drawer-${workId}`}
      >
        <span aria-hidden="true">◆</span>
        <span>基石</span>
        <span className="cornerstone-drawer__count">{work.cornerstones.filter((item) => !item.tombstone).length}</span>
        {hasAttention && <span className="cornerstone-drawer__attention-badge" aria-label={blocked ? '存在运行阻断' : '存在降级基石'}>!</span>}
      </button>

      {drawer.open && (
        <section id={`cornerstone-drawer-${workId}`} className="cornerstone-drawer__body" aria-label="基石列表" data-testid="cornerstone-drawer-body">
          <header className="cornerstone-drawer__header">
            <button
              type="button"
              className="cornerstone-drawer__back"
              onClick={closeDrawer}
              data-testid="cornerstone-drawer-back"
            >
              <span aria-hidden="true">←</span>
              <span>{t('work.cornerstoneBack')}</span>
            </button>
          </header>
          <div className="cornerstone-drawer__filters">
            <label>
              类型
              <select value={drawer.filterType} onChange={(event) => ui.getState().setFilterType(workId, event.target.value as CornerstoneType | 'all')}>
                <option value="all">全部</option>
                {Object.entries(TYPE_LABELS).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
              </select>
            </label>
            <label>
              必需性
              <select value={drawer.filterRequired === null ? 'all' : drawer.filterRequired ? 'required' : 'optional'} onChange={(event) => ui.getState().setFilterRequired(workId, event.target.value === 'all' ? null : event.target.value === 'required')}>
                <option value="all">全部</option>
                <option value="required">必需</option>
                <option value="optional">可选</option>
              </select>
            </label>
          </div>

          {!readonly && (
            <div className="cornerstone-drawer__pin-form" data-testid="cornerstone-pin-form">
              <h4>Pin 新基石</h4>
              <div className="cornerstone-drawer__pin-row">
                <select aria-label="基石类型" value={newType} onChange={(event) => setNewType(event.target.value as CornerstoneType)}>
                  {Object.entries(TYPE_LABELS).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
                </select>
                <input aria-label="基石标题" value={newTitle} onChange={(event) => setNewTitle(event.target.value)} placeholder="标题" />
                <select aria-label="保存模式" value={newMode} onChange={(event) => setNewMode(event.target.value as 'live_ref' | 'snapshot')}>
                  <option value="snapshot">快照</option>
                  <option value="live_ref">实时引用</option>
                </select>
                <select aria-label="引用类型" value={newRefKind} onChange={(event) => setNewRefKind(event.target.value as CornerstoneRef['kind'])}>
                  {REF_KINDS.map((kind) => <option key={kind} value={kind}>{refLabel({ kind })}</option>)}
                </select>
                {newRefKind !== 'inline' && <input aria-label="引用值" value={newRefValue} onChange={(event) => setNewRefValue(event.target.value)} placeholder="引用位置" />}
                <textarea aria-label="基石内容" value={newContent} onChange={(event) => setNewContent(event.target.value)} placeholder="快照内容或最后已知内容" />
                <input aria-label="基石标签" value={newTags} onChange={(event) => setNewTags(event.target.value)} placeholder="标签，逗号分隔" />
                <label><input type="checkbox" checked={newRequired} onChange={(event) => setNewRequired(event.target.checked)} />必需</label>
                <button type="button" onClick={() => void handlePin()} disabled={newItem?.pendingAction === 'pin'}>Pin</button>
              </div>
              {newItem?.pendingAction && <p className="cornerstone-item__pending" role="status">正在 Pin…</p>}
              {newItem?.error && <OperationError item={newItem} onRetry={() => void handlePin(newItem.retry)} />}
            </div>
          )}

          {filtered.length === 0 ? (
            <p className="cornerstone-drawer__empty">{work.cornerstones.length ? '没有符合筛选条件的基石。' : '尚未 Pin 基石。'}</p>
          ) : (
            <ul className="cornerstone-drawer__list">
              {filtered.map((cornerstone) => (
                <CornerstoneItem
                  key={cornerstone.id}
                  cornerstone={cornerstone}
                  uiState={drawer.byId[cornerstone.id]}
                  readonly={readonly}
                  onDraft={(draft) => ui.getState().setDraft(workId, cornerstone.id, draft)}
                  onAction={handleAction}
                />
              ))}
            </ul>
          )}

          {hasAttention && (
            <div className="cornerstone-drawer__attention-summary" role="alert" data-testid="cornerstone-attention-summary">
              <strong>{blocked ? '权威评估阻止运行。' : '部分可选基石降级可用，不阻止运行。'}</strong>
              <ul>{attention.items.map((item, index) => <li key={`${item.cornerstoneId}:${index}`}>{item.title}：{item.reason}</li>)}</ul>
            </div>
          )}
        </section>
      )}
    </div>
  );
};

const OperationError: React.FC<{ item: CornerstoneItemUI; onRetry: () => void }> = ({ item, onRetry }) => (
  <div className="cornerstone-item__error" role="alert">
    <span>{item.error}</span>
    {item.retry && <button type="button" onClick={onRetry}>重试</button>}
  </div>
);

interface CornerstoneItemProps {
  cornerstone: Cornerstone;
  uiState?: CornerstoneItemUI;
  readonly: boolean;
  onDraft: (draft: { title?: string | null; content?: string | null }) => void;
  onAction: (
    cornerstone: Cornerstone,
    action: Exclude<CornerstoneUIAction, 'pin'>,
    ref?: CornerstoneRef,
    retry?: CornerstoneRetry | null,
  ) => Promise<void>;
}

const CornerstoneItem: React.FC<CornerstoneItemProps> = ({ cornerstone, uiState, readonly, onDraft, onAction }) => {
  const repairKind = (uiState?.draftTitle as CornerstoneRef['kind'] | null) ?? cornerstone.ref.kind;
  const repairValue = uiState?.draftContent ?? refValue(cornerstone.ref);
  const repairRef = makeRef(repairKind, repairValue);
  const pending = uiState?.pendingAction ?? null;
  const hint = statusHint(cornerstone);
  const needsRepair = ['stale', 'missing', 'denied', 'invalid'].includes(cornerstone.status);
  const retry = uiState?.retry;
  const snapshotBlobRepair = cornerstone.mode === 'snapshot' && !!cornerstone.ref.blobDigest && !cornerstone.tombstone;
  const repairDraftContent = uiState?.draftContent ?? '';
  const canRepair = snapshotBlobRepair ? !!repairDraftContent.trim() : !!repairRef;

  const retryAction = () => {
    if (!retry || retry.action === 'pin') return;
    void onAction(cornerstone, retry.action, retry.action === 'repair' && !snapshotBlobRepair ? repairRef ?? undefined : undefined, retry);
  };

  return (
    <li className={`cornerstone-item cornerstone-status--${cornerstone.status}${cornerstone.tombstone ? ' cornerstone-item--removed' : ''}`} data-testid={`cornerstone-item-${cornerstone.id}`}>
      <div className="cornerstone-item__header">
        <span className="cornerstone-item__type">{TYPE_LABELS[cornerstone.type]}</span>
        <span className="cornerstone-item__status">{cornerstone.tombstone ? '已移除' : STATUS_LABELS[cornerstone.status]}</span>
        {cornerstone.required && <span className="cornerstone-item__required">必需</span>}
        <span className="cornerstone-item__mode">{cornerstone.mode === 'live_ref' ? '实时引用' : '快照'}</span>
      </div>
      <h5 className="cornerstone-item__title">{cornerstone.title}</h5>
      <div className="cornerstone-item__meta">
        <span title="内容摘要">digest {shortDigest(cornerstone.digest)}</span>
        <span>{refLabel(cornerstone.ref)}</span>
        <span>来源 {cornerstone.provenance.kind}</span>
      </div>
      {!!cornerstone.tags?.length && <div className="cornerstone-item__tags">{cornerstone.tags.map((tag) => <span key={tag}>{tag}</span>)}</div>}
      {hint && <p className="cornerstone-item__hint">{hint}</p>}
      {uiState?.conflictSnapshot && (
        <p className="cornerstone-item__conflict" role="status">
          最新状态：{STATUS_LABELS[uiState.conflictSnapshot.status]}，revision 已刷新。
        </p>
      )}
      {needsRepair && !cornerstone.tombstone && !readonly && (
        <div className="cornerstone-item__repair">
          {snapshotBlobRepair ? (
            <>
              <textarea
                aria-label={`修复 ${cornerstone.title} 的快照内容`}
                value={repairDraftContent}
                onChange={(event) => onDraft({ content: event.target.value })}
                placeholder="输入与当前 digest 匹配的快照原始内容以修复"
                rows={3}
                className="cornerstone-item__repair-content"
                spellCheck={false}
              />
              <div className="cornerstone-item__repair-meta">
                <span title="内容摘要">digest {shortDigest(cornerstone.digest)}</span>
                <span>来源 {cornerstone.provenance.kind}</span>
                <span className="cornerstone-item__repair-risk">内容会先在本地校验，正文不会写入日志；必须与已接受 digest 匹配。</span>
              </div>
            </>
          ) : (
            <>
              <select aria-label={`修复 ${cornerstone.title} 的引用类型`} value={repairKind} onChange={(event) => onDraft({ title: event.target.value })}>
                {REF_KINDS.map((kind) => <option key={kind} value={kind}>{refLabel({ kind })}</option>)}
              </select>
              {repairKind !== 'inline' && <input aria-label={`修复 ${cornerstone.title} 的引用值`} value={repairValue} onChange={(event) => onDraft({ content: event.target.value })} />}
            </>
          )}
        </div>
      )}
      {pending && <p className="cornerstone-item__pending" role="status">正在{pending}…</p>}
      {uiState?.error && <OperationError item={uiState} onRetry={retryAction} />}
      {!readonly && (
        <div className="cornerstone-item__actions" role="group" aria-label={`${cornerstone.title} 操作`}>
          {cornerstone.tombstone ? (
            <button type="button" disabled={!!pending} onClick={() => void onAction(cornerstone, 'undo')}>撤销移除</button>
          ) : (
            <>
              <button type="button" disabled={!!pending} onClick={() => void onAction(cornerstone, 'validate')}>校验</button>
              {cornerstone.mode === 'live_ref' && <button type="button" disabled={!!pending} onClick={() => void onAction(cornerstone, 'refresh')}>刷新</button>}
              {cornerstone.mode === 'live_ref' && <button type="button" disabled={!!pending} onClick={() => void onAction(cornerstone, 'freeze')}>冻结</button>}
              {cornerstone.status === 'stale' && <button type="button" disabled={!!pending} onClick={() => void onAction(cornerstone, 'accept')}>接受新版本</button>}
              {needsRepair && <button type="button" disabled={!!pending || !canRepair} onClick={() => void onAction(cornerstone, 'repair', snapshotBlobRepair ? undefined : (repairRef ?? undefined))}>{snapshotBlobRepair ? '修复快照' : '修复引用'}</button>}
              <button type="button" className="cornerstone-item__action--danger" disabled={!!pending} onClick={() => void onAction(cornerstone, 'remove')}>移除</button>
            </>
          )}
        </div>
      )}
    </li>
  );
};

export default CornerstoneDrawer;
