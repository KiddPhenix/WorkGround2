import React, { useEffect, useRef, useState } from 'react';
import type { SkillInfo, CreateSkillRequest, CreateSkillResult } from '../../types_v2';

export interface SkillBindingModalProps {
  nodeId: string;
  currentSkillName?: string;
  skills: SkillInfo[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  onSelect: (skillName: string) => Promise<void>;
  onClear: () => Promise<void>;
  onCreate: (input: CreateSkillRequest) => Promise<CreateSkillResult>;
  onClose: () => void;
}

export const SkillBindingModal: React.FC<SkillBindingModalProps> = ({
  currentSkillName,
  skills,
  loading,
  error,
  nodeId,
  onRetry,
  onSelect,
  onClear,
  onCreate,
  onClose,
}) => {
  const [search, setSearch] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [newName, setNewName] = useState('');
  const [newDesc, setNewDesc] = useState('');
  const [newBody, setNewBody] = useState('');
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const overlayRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const newNameRef = useRef<HTMLInputElement>(null);
  const createIntentIDs = useRef(new Map<string, string>());

  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { onClose(); e.preventDefault(); }
    };
    document.addEventListener('keydown', handleKey);
    return () => document.removeEventListener('keydown', handleKey);
  }, [onClose]);

  useEffect(() => {
    if (showCreate) newNameRef.current?.focus();
    else searchRef.current?.focus();
  }, [showCreate]);

  const enabledSkills = skills.filter((s) => s.enabled);
  const filtered = search.trim()
    ? enabledSkills.filter((s) =>
        s.name.toLowerCase().includes(search.toLowerCase()) ||
        s.description.toLowerCase().includes(search.toLowerCase()))
    : enabledSkills;

  const handleCreate = async () => {
    if (!newName.trim() || !newDesc.trim() || !newBody.trim()) {
      setCreateError('名称、描述和正文均为必填');
      return;
    }
    setCreating(true);
    setCreateError(null);
    try {
      const intentKey = [newName.trim(), newDesc.trim(), newBody.trim()].join('\u0000');
      let requestId = createIntentIDs.current.get(intentKey);
      if (!requestId) {
        requestId = `create-skill-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
        createIntentIDs.current.set(intentKey, requestId);
      }
      const result = await onCreate({
        name: newName.trim(),
        description: newDesc.trim(),
        body: newBody.trim(),
        scope: 'project',
        requestId,
      });
      if (result.error) {
        setCreateError(result.error.message ?? '创建失败');
      } else if (result.skill) {
        await onSelect(result.skill.name);
        createIntentIDs.current.delete(intentKey);
        onClose();
      } else {
        setCreateError('创建响应不完整，请重试');
      }
    } catch (e) {
      setCreateError(e instanceof Error ? e.message : String(e));
    } finally {
      setCreating(false);
    }
  };

  const handleSelect = async (skillName: string) => {
    if (saving) return;
    setSaving(true);
    setSaveError(null);
    try {
      await onSelect(skillName);
      onClose();
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const handleClear = async () => {
    if (saving) return;
    setSaving(true);
    setSaveError(null);
    try {
      await onClear();
      onClose();
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const handleOverlayClick = (e: React.MouseEvent) => {
    if (e.target === overlayRef.current) onClose();
  };

  return (
    <div className="modal-backdrop" ref={overlayRef} onClick={handleOverlayClick} data-testid="skill-modal-backdrop">
      <div className="modal" role="dialog" aria-modal="true" aria-label={`为步骤 ${nodeId} 绑定技能`} data-testid="skill-modal">
        <div className="modal-header">
          <h3>绑定技能</h3>
          <button type="button" className="modal-close-button" onClick={onClose} aria-label="关闭" data-testid="skill-modal-close">×</button>
        </div>

        <div className="modal-body">
          {!showCreate ? (
            <>
              <div className="wg2-sm-search">
                <input
                  ref={searchRef}
                  type="text"
                  className="wg2-sm-search-input"
                  placeholder="搜索技能..."
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  aria-label="搜索技能"
                  data-testid="skill-modal-search"
                />
              </div>

              {currentSkillName && (
                <div className="wg2-sm-current" data-testid="skill-modal-current">
                  当前绑定: <strong>{currentSkillName}</strong>
                  <button type="button" className="wg2-sm-clear-btn" onClick={() => void handleClear()} disabled={saving} data-testid="skill-modal-clear">清除</button>
                </div>
              )}

              {loading && <div className="wg2-sm-status" data-testid="skill-modal-loading">加载中…</div>}
              {error && !loading && (
                <div className="wg2-sm-status wg2-sm-error" data-testid="skill-modal-error">
                  {error}
                  <button type="button" className="wg2-sm-retry-btn" onClick={onRetry}>重试</button>
                </div>
              )}

              {saveError && <div className="wg2-sm-status wg2-sm-error" role="alert" data-testid="skill-modal-save-error">{saveError}</div>}

              {!loading && !error && (
                <ul className="wg2-sm-list" role="listbox" aria-label="可用技能" data-testid="skill-modal-list">
                  {filtered.length === 0 ? (
                    <li className="wg2-sm-empty">没有匹配的技能</li>
                  ) : (
                    filtered.map((sk) => (
                      <li
                        key={sk.name}
                        role="option"
                        aria-selected={sk.name === currentSkillName}
                        className={`wg2-sm-item${sk.name === currentSkillName ? ' wg2-sm-item-selected' : ''}`}
                        data-testid={`skill-modal-item-${sk.name}`}
                      >
                        <button type="button" className="wg2-sm-item-button" onClick={() => void handleSelect(sk.name)} disabled={saving}>
                          <span className="wg2-sm-item-name">{sk.name}</span>
                          <span className="wg2-sm-item-desc">{sk.description || '无描述'}</span>
                          <span className="wg2-sm-item-meta">
                            <span className="wg2-sm-scope">{sk.scope}</span>
                            {sk.runAs === 'subagent' && <span className="wg2-sm-runas">subagent</span>}
                          </span>
                        </button>
                      </li>
                    ))
                  )}
                </ul>
              )}

              <button
                type="button"
                className="wg2-sm-new-btn"
                onClick={() => { searchRef.current?.blur(); setShowCreate(true); }}
                data-testid="skill-modal-new-btn"
              >
                + 新建技能
              </button>
            </>
          ) : (
            <div className="wg2-sm-create" data-testid="skill-modal-create">
              <h4>新建技能</h4>
              <label>
                名称
                <input ref={newNameRef} type="text" value={newName} onInput={(e) => setNewName(e.currentTarget.value)} placeholder="my-skill" data-testid="skill-modal-create-name" />
              </label>
              <label>
                描述
                <input type="text" value={newDesc} onInput={(e) => setNewDesc(e.currentTarget.value)} placeholder="一句话描述这个技能的功能" data-testid="skill-modal-create-desc" />
              </label>
              <label>
                正文 (Markdown)
                <textarea value={newBody} onInput={(e) => setNewBody(e.currentTarget.value)} placeholder="技能内容…" rows={6} data-testid="skill-modal-create-body" />
              </label>
              {createError && <div className="wg2-sm-error" data-testid="skill-modal-create-error">{createError}</div>}
              <div className="wg2-sm-create-actions">
                <button type="button" onClick={() => setShowCreate(false)} disabled={creating}>返回</button>
                <button type="button" onClick={handleCreate} disabled={creating} data-testid="skill-modal-create-submit">
                  {creating ? '创建中…' : '创建并选择'}
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};
