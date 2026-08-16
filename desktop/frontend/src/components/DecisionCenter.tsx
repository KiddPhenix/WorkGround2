import { useEffect, useMemo, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { Check, Clock3, MessageCircleQuestion, Plus, Send, Settings2, Trash2, X } from "lucide-react";
import { app, onDecisionState } from "../lib/bridge";
import type { DecisionStateView, DecisionView } from "../lib/types";

interface Props {
  open: boolean;
  onClose: () => void;
}

const emptyChannel = { id: "", name: "微信主人", kind: "weixin", enabled: true, connectionId: "", domain: "", chatId: "", chatType: "dm" };
type DecisionDraft = { title: string; task: string; why: string; prompt: string; optionA: string; impactA: string; optionB: string; impactB: string };

export function DecisionCenter({ open, onClose }: Props) {
  const [state, setState] = useState<DecisionStateView | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
	const [notice, setNotice] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [draft, setDraft] = useState({ title: "", task: "", why: "", prompt: "", optionA: "", impactA: "", optionB: "", impactB: "" });
  const [channel, setChannel] = useState(emptyChannel);

  useEffect(() => {
    if (!open) return;
    let alive = true;
    void app.DecisionState().then((next) => alive && setState(next)).catch((cause) => alive && setError(String(cause)));
    const off = onDecisionState((next) => alive && setState(next));
    return () => { alive = false; off(); };
  }, [open]);

  const run = async (key: string, action: () => Promise<DecisionStateView>) => {
    setBusy(key);
    setError("");
    try { setState(await action()); } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); } finally { setBusy(""); }
  };

  const createDecision = async () => {
    setBusy("create");
    setError("");
    try {
      await app.CreateDecision({
        idempotencyKey: `desktop-manual:${crypto.randomUUID()}`,
        agentId: "desktop-user", threadId: "", workspaceRoot: "", sessionId: "",
        title: draft.title, taskSummary: draft.task, whyNow: draft.why,
        questions: [{ id: "choice", header: draft.title, prompt: draft.prompt, multiSelect: false, options: [
          { label: draft.optionA, impact: draft.impactA }, { label: draft.optionB, impact: draft.impactB },
        ] }],
        noAnswerPolicy: "任务保持暂停，不会自动替你选择。",
      });
      setState(await app.DecisionState());
      setCreateOpen(false);
      setDraft({ title: "", task: "", why: "", prompt: "", optionA: "", impactA: "", optionB: "", impactB: "" });
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); } finally { setBusy(""); }
  };

  if (!open) return null;
  return (
    <div className="decision-center__backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <section className="decision-center" role="dialog" aria-modal="true" aria-label="主人决策">
        <header className="decision-center__header">
          <div><span className="decision-center__eyebrow">全局对话通道</span><h2><MessageCircleQuestion size={20} />主人决策</h2></div>
          <div className="decision-center__header-actions">
            <button className="btn btn--primary btn--small" type="button" onClick={() => setCreateOpen((value) => !value)}><Plus size={14} />新建问题</button>
            <button className="decision-center__close" type="button" onClick={onClose} aria-label="关闭"><X size={18} /></button>
          </div>
        </header>

        {error && <div className="decision-center__error">{error}</div>}
		{notice && <div className="decision-center__notice">{notice}</div>}
        {createOpen && <CreateForm draft={draft} setDraft={setDraft} busy={busy === "create"} onCreate={() => void createDecision()} />}
        {!state ? <div className="decision-center__empty">正在读取决策队列…</div> : !state.available ? <div className="decision-center__error">{state.error || "DecisionBroker 不可用"}</div> : (
          <div className="decision-center__body">
            <main className="decision-center__main">
              <section className="decision-center__section">
                <SectionTitle icon={<Clock3 size={15} />} title="当前等待" count={state.active ? 1 : 0} />
                {state.active ? <DecisionCard key={state.active.id} value={state.active} busy={busy} onRun={run} /> : <div className="decision-center__empty"><Check size={18} />现在没有问题等你回答</div>}
              </section>
              <section className="decision-center__section">
                <SectionTitle title="后续队列" count={state.queue.length} />
                {state.queue.map((value, index) => <QueuedCard key={value.id} value={value} index={index} busy={busy} onRun={run} />)}
                {!state.queue.length && <div className="decision-center__hint">新问题会在当前问题结束后依次出现，不会同时轰炸你。</div>}
              </section>
              {!!state.deferred.length && <section className="decision-center__section"><SectionTitle title="稍后再问" count={state.deferred.length} />{state.deferred.map((value) => <QueuedCard key={value.id} value={value} busy={busy} onRun={run} deferred />)}</section>}
              <section className="decision-center__section decision-center__history">
                <SectionTitle title="最近结果" count={state.history.length} />
                {state.history.slice(0, 20).map((value) => <HistoryRow key={value.id} value={value} />)}
              </section>
            </main>
            <aside className="decision-center__aside">
              <SettingsPanel state={state} busy={busy} channel={channel} setChannel={setChannel} onRun={run} onError={setError} onNotice={setNotice} />
            </aside>
          </div>
        )}
      </section>
    </div>
  );
}

function SectionTitle({ icon, title, count }: { icon?: ReactNode; title: string; count: number }) {
  return <h3 className="decision-center__section-title">{icon}{title}<span>{count}</span></h3>;
}

function DecisionCard({ value, busy, onRun }: { value: DecisionView; busy: string; onRun: (key: string, action: () => Promise<DecisionStateView>) => Promise<void> }) {
  const initial = useMemo(() => Object.fromEntries(value.presentation.questions.map((question) => [question.id, [] as string[]])), [value.id]);
  const [selected, setSelected] = useState<Record<string, string[]>>(initial);
  const complete = value.presentation.questions.every((question) => (selected[question.id]?.length ?? 0) > 0);
  const source = value.origin.session_title || value.origin.agent_id || value.origin.workspace_root || value.origin.kind;
  const answer = () => onRun(`answer:${value.id}`, () => app.ResolveDecision({ decisionId: value.id, responder: "WorkGround2 桌面端", selections: value.presentation.questions.map((question) => ({ questionId: question.id, selected: selected[question.id] || [] })) }));
  return <article className="decision-card decision-card--active">
    <div className="decision-card__id">{value.id}</div>
    <h4>{value.presentation.title}</h4>
    <p><strong>来源：</strong>{source}</p>
    <p><strong>正在做：</strong>{value.presentation.task_summary}</p>
    <p><strong>为什么现在问：</strong>{value.presentation.why_now}</p>
    {value.presentation.questions.map((question) => <fieldset key={question.id} className="decision-card__question"><legend>{question.prompt}</legend>{question.options.map((option) => {
      const checked = selected[question.id]?.includes(option.label) ?? false;
      return <label key={option.label} className={`decision-option${checked ? " decision-option--selected" : ""}`}><input type={question.multi_select ? "checkbox" : "radio"} name={`${value.id}:${question.id}`} checked={checked} onChange={() => setSelected((current) => ({ ...current, [question.id]: question.multi_select ? (checked ? current[question.id].filter((item) => item !== option.label) : [...(current[question.id] || []), option.label]) : [option.label] }))} /><span><b>{option.label}</b><small>{option.impact}</small></span></label>;
    })}</fieldset>)}
    {value.presentation.recommendation && <div className="decision-card__recommendation">建议：{value.presentation.recommendation.option} · {value.presentation.recommendation.reason}</div>}
    <div className="decision-card__policy">未回答时：{value.presentation.no_answer_policy}</div>
    <div className="decision-card__actions"><button className="btn btn--primary" type="button" disabled={!complete || !!busy} onClick={() => void answer()}><Send size={14} />采用回答</button><button className="btn btn--secondary" type="button" disabled={!!busy} onClick={() => void onRun(`defer:${value.id}`, () => app.DeferDecision(value.id))}>稍后再问</button><button className="btn btn--ghost" type="button" disabled={!!busy} onClick={() => void onRun(`cancel:${value.id}`, () => app.CancelDecision(value.id))}>取消</button></div>
  </article>;
}

function QueuedCard({ value, index, deferred, busy, onRun }: { value: DecisionView; index?: number; deferred?: boolean; busy: string; onRun: (key: string, action: () => Promise<DecisionStateView>) => Promise<void> }) {
  return <article className="decision-queue-row"><span className="decision-queue-row__index">{deferred ? "稍后" : `#${(index ?? 0) + 1}`}</span><div><b>{value.presentation.title}</b><small>{value.presentation.task_summary}</small></div><div className="decision-queue-row__actions">{deferred && <button type="button" disabled={!!busy} onClick={() => void onRun(`resume:${value.id}`, () => app.ResumeDecision(value.id))}>重新排队</button>}<button type="button" disabled={!!busy} onClick={() => void onRun(`cancel:${value.id}`, () => app.CancelDecision(value.id))}>取消</button></div></article>;
}

function HistoryRow({ value }: { value: DecisionView }) {
  const answer = value.answer?.selections.flatMap((selection) => selection.selected).join("、");
  const decidedAt = value.decided_at ? new Date(value.decided_at).toLocaleString() : "";
  return <div className="decision-history-row"><span className={`decision-status decision-status--${value.status}`}>{value.status}</span><div><b>{value.presentation.title}</b><small>{answer ? `${value.responder?.label || "主人"}：${answer}${decidedAt ? ` · ${decidedAt}` : ""}` : value.last_error || value.id}</small></div></div>;
}

function CreateForm({ draft, setDraft, busy, onCreate }: { draft: DecisionDraft; setDraft: Dispatch<SetStateAction<DecisionDraft>>; busy: boolean; onCreate: () => void }) {
  const field = (key: keyof DecisionDraft, label: string, placeholder: string) => <label><span>{label}</span><input value={draft[key]} placeholder={placeholder} onChange={(event) => setDraft((current) => ({ ...current, [key]: event.target.value }))} /></label>;
  const ready = (["title", "task", "why", "prompt", "optionA", "impactA", "optionB", "impactB"] as Array<keyof DecisionDraft>).every((key) => draft[key].trim());
  return <div className="decision-create"><h3>创建一个独立问题</h3><div className="decision-create__grid">{field("title", "标题", "例如：确定主角图策略")}{field("task", "任务背景", "我正在为活动页生成主视觉")}{field("why", "为什么现在问", "下一步会锁定角色一致性")}{field("prompt", "要决定什么", "生成新的还是复用主角图？")}{field("optionA", "选项 A", "复用主角图")}{field("impactA", "A 的影响", "角色稳定，创意变化较少")}{field("optionB", "选项 B", "生成新图")}{field("impactB", "B 的影响", "创意空间大，需重新确认一致性")}</div><button className="btn btn--primary" type="button" disabled={!ready || busy} onClick={onCreate}>加入全局决策队列</button></div>;
}

function SettingsPanel({ state, busy, channel, setChannel, onRun, onError, onNotice }: { state: DecisionStateView; busy: string; channel: typeof emptyChannel; setChannel: Dispatch<SetStateAction<typeof emptyChannel>>; onRun: (key: string, action: () => Promise<DecisionStateView>) => Promise<void>; onError: (value: string) => void; onNotice: (value: string) => void }) {
  const [mode, setMode] = useState(state.settings.externalMode);
  const [until, setUntil] = useState(state.settings.localOnlyUntil?.slice(0, 16) || "");
  const [grace, setGrace] = useState(state.settings.smartGraceSec || 30);
  const saveSettings = () => onRun("settings", () => app.SaveDecisionSettings({ externalMode: mode, localOnlyUntil: until ? new Date(until).toISOString() : "", smartGraceSec: grace }));
  const saveChannel = () => onRun("channel", () => app.SaveDecisionChannel(channel));
  return <><section className="decision-settings"><h3><Settings2 size={15} />外部发送</h3><label><span>模式</span><select value={mode} onChange={(event) => setMode(event.target.value)}><option value="smart">智能：本机宽限后发送</option><option value="always">始终发送</option><option value="local_only_until">暂时只在本机</option><option value="off">关闭外部发送</option></select></label>{mode === "smart" && <label><span>本机宽限（秒）</span><input type="number" min={0} value={grace} onChange={(event) => setGrace(Number(event.target.value))} /></label>}{mode === "local_only_until" && <label><span>恢复时间（留空=无限期）</span><input type="datetime-local" value={until} onChange={(event) => setUntil(event.target.value)} /></label>}<button className="btn btn--secondary btn--small" type="button" disabled={!!busy} onClick={() => void saveSettings()}>保存发送策略</button></section>
    <section className="decision-settings"><h3>对话通道</h3>{state.channels.map((item) => <div className="decision-channel" key={item.id}><div><b>{item.name}</b><small>{item.kind} · {item.chat_id}</small></div><button type="button" onClick={() => void app.TestDecisionChannel(item.id).catch((cause) => onError(String(cause)))}>测试</button><button type="button" aria-label="删除" onClick={() => void onRun(`delete:${item.id}`, () => app.DeleteDecisionChannel(item.id))}><Trash2 size={13} /></button></div>)}<label><span>名称</span><input value={channel.name} onChange={(event) => setChannel((current) => ({ ...current, name: event.target.value }))} /></label><label><span>连接 ID</span><input value={channel.connectionId} placeholder="微信连接的 adapter ID" onChange={(event) => setChannel((current) => ({ ...current, connectionId: event.target.value }))} /></label><label><span>Chat ID</span><input value={channel.chatId} placeholder="接收问题的个人/群 Chat ID" onChange={(event) => setChannel((current) => ({ ...current, chatId: event.target.value }))} /></label><button className="btn btn--secondary btn--small" type="button" disabled={!channel.chatId.trim() || !!busy} onClick={() => void saveChannel()}>添加微信通道</button></section>
    <section className="decision-settings"><h3>给其他 Agent 使用</h3><p className="decision-center__hint">安装后，Codex 等 Agent 可以通过 $ask-workground2-owner 创建问题和分段长等待。</p><button className="btn btn--secondary btn--small" type="button" onClick={() => void app.InstallDecisionSkill().then((result) => onNotice(`Skill 已安装到 ${result.skillPath}`)).catch((cause) => onError(String(cause)))}>安装 / 更新 Codex Skill</button></section>
    <div className="decision-center__long-wait">问题默认没有技术超时。关闭应用或隔几天回来，队列仍会保留；回答前来源任务会重新检查现场。</div></>;
}
