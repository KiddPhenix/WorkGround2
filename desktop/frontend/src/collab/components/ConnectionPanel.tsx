import { useEffect, useState, type FormEvent } from "react";
import { AlertTriangle, Link2, Plus, RadioTower, RefreshCw, Server, Trash2, UserRound } from "lucide-react";
import { useT } from "../../lib/i18n";
import { collabCopy } from "../copy";
import { loadCollaborationIdentity, newCollaborationIdentity, saveCollaborationIdentity } from "../identity";
import { parseCollaborationInvite } from "../invite";
import type { CollaborationRelayConfig, CollaborationRoom, CollaborationRoomQueryInput, CollaborationRoomQueryItem, CollaborationRoomQueryResult, CollaborationRoomVisibility, CollaborationRouteInput, CollaborationWorkspaceOption, HostCollaborationRoomInput, JoinCollaborationRoomInput } from "../types";

interface ConnectionPanelProps {
  sessionID?: string;
  status: string;
  error?: string;
  initial?: CollaborationRoom;
  workspaces: CollaborationWorkspaceOption[];
  workspaceRoot: string;
  onWorkspaceChange(root: string): void;
  sessionResolving: boolean;
  sessionError?: string;
  onRetrySession(): void;
  onHost(input: HostCollaborationRoomInput): Promise<void>;
  onJoin(input: JoinCollaborationRoomInput): Promise<void>;
  relayConfig?: CollaborationRelayConfig;
  roomQuery?: CollaborationRoomQueryResult;
  discoveryLoading?: boolean;
  discoveryError?: string;
  relaySaving?: boolean;
  relayConfigError?: string;
  onQueryRooms?(input: CollaborationRoomQueryInput): Promise<void>;
  onSaveRelayConfig?(input: CollaborationRelayConfig): Promise<void>;
  onClose(): void;
  onConnected?(): Promise<void> | void;
}

function readSaved(key: string, fallback: string, sessionID?: string): string {
  try {
    if (sessionID) {
      const scoped = localStorage.getItem(`collab:${sessionID}:${key}`);
      return scoped ?? fallback;
    }
    return localStorage.getItem(`collab:${key}`) || fallback;
  } catch { return fallback; }
}

export function ConnectionPanel({ sessionID, status, error, initial, workspaces, workspaceRoot, onWorkspaceChange, sessionResolving, sessionError, onRetrySession, onHost, onJoin, relayConfig = { preferLAN: true, connectTimeoutSeconds: 10, routeStableSeconds: 60, relays: [] }, roomQuery = { rooms: [] }, discoveryLoading = false, discoveryError, relaySaving = false, relayConfigError, onQueryRooms, onSaveRelayConfig, onClose, onConnected }: ConnectionPanelProps) {
  const c = collabCopy(useT());
  const [mode, setMode] = useState<"host" | "join" | "relay">("join");
  const [joinHost, setJoinHost] = useState(() => initial?.host || readSaved("host", "127.0.0.1", sessionID));
  const [listenHost, setListenHost] = useState(() => readSaved("listenHost", "0.0.0.0", sessionID));
  const [port, setPort] = useState(() => String(initial?.port || readSaved("port", "39170", sessionID)));
  const [room, setRoom] = useState(() => initial?.room || readSaved("room", "", sessionID));
  const [token, setToken] = useState("");
  const [connectionString, setConnectionString] = useState("");
  const [connectionError, setConnectionError] = useState("");
  const [savedIdentity] = useState(loadCollaborationIdentity);
  const [identity, setIdentity] = useState(() => savedIdentity || newCollaborationIdentity());
  const [identityOpen, setIdentityOpen] = useState(() => !savedIdentity);
  const [roomName, setRoomName] = useState(() => initial?.title || "");
  const [description, setDescription] = useState(() => initial?.description || "");
  const [lanEnabled, setLANEnabled] = useState(true);
  const [relayIDs, setRelayIDs] = useState<string[]>([]);
  const [visibility, setVisibility] = useState<CollaborationRoomVisibility>("private");
  const [tags, setTags] = useState("");
  const [capacity, setCapacity] = useState("");
  const [showOnlineCount, setShowOnlineCount] = useState(true);
  const [joinRoutes, setJoinRoutes] = useState<CollaborationRouteInput[]>([]);
  const [joinHostKey, setJoinHostKey] = useState("");
  const [joinRef, setJoinRef] = useState("");
  const busy = status === "connecting" || status === "syncing";
  const identityReady = Boolean(identity.memberName.trim() && identity.agentName.trim());
  const currentHost = mode === "host" ? listenHost : joinHost;
  const enabledRelays = relayConfig.relays.filter((relay) => relay.enabled);
  const discoverySelected = enabledRelays.some((relay) => relay.discovery && relayIDs.includes(relay.id));

  const importConnection = (value: string) => {
    setConnectionString(value);
    if (!value.trim()) {
      setConnectionError("");
      return;
    }
    try {
      const parsed = parseCollaborationInvite(value);
      setRoom(parsed.room);
      if (parsed.version === 2) {
        const lan = parsed.routes.find((route) => route.kind === "lan");
        setJoinHost(lan?.host || "");
        setPort(String(lan?.port || 0));
        setToken(parsed.roomToken || "");
        setJoinRoutes(parsed.routes);
        setJoinHostKey(parsed.hostKey);
        setJoinRef("");
      } else {
        setJoinHost(parsed.host);
        setPort(String(parsed.port));
        setToken(parsed.token || "");
        setJoinRoutes([]);
        setJoinHostKey("");
        setJoinRef("");
      }
      setConnectionError("");
    } catch {
      setConnectionError(c("connectionStringInvalid"));
    }
  };

  const selectRelay = (relayID: string, selected: boolean) => {
    const next = selected ? [...new Set([...relayIDs, relayID])] : relayIDs.filter((id) => id !== relayID);
    setRelayIDs(next);
    if (visibility !== "private" && !enabledRelays.some((relay) => relay.discovery && next.includes(relay.id))) setVisibility("private");
  };

  const selectDiscoveredRoom = (item: CollaborationRoomQueryItem) => {
    const lan = item.routes.find((route) => route.kind === "lan");
    setRoom(item.room);
    setJoinHost(lan?.host || "");
    setPort(String(lan?.port || 0));
    setToken("");
    setConnectionString("");
    setJoinRoutes(item.routes);
    setJoinHostKey(item.hostKey);
    setJoinRef(item.joinRef || "");
    setConnectionError("");
    setMode("join");
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!sessionID) return;
    if (!identityReady) {
      setIdentityOpen(true);
      return;
    }
    const saved = saveCollaborationIdentity(identity);
    try {
      localStorage.setItem(`collab:${sessionID}:host`, joinHost.trim());
      localStorage.setItem(`collab:${sessionID}:listenHost`, listenHost.trim());
      localStorage.setItem(`collab:${sessionID}:port`, port);
      localStorage.setItem(`collab:${sessionID}:room`, room.trim());
    } catch { /* private mode: the current form values remain usable */ }
    const shared = {
      port: Number(port), room: room.trim(), token: token.trim() || undefined,
      memberID: saved.memberID, memberName: saved.memberName, memberAvatar: saved.memberAvatar, memberRole: saved.memberRole,
      agentID: saved.agentID, agentName: saved.agentName, agentAvatar: saved.agentAvatar, agentRole: saved.agentRole, sessionID,
    };
    try {
      if (mode === "host") {
        const publishedRoomName = roomName.trim() || room.trim();
        if (!lanEnabled && relayIDs.length === 0) {
          setConnectionError(c("routeRequired"));
          return;
        }
        await onHost({
          ...shared,
          listenHost: listenHost.trim(),
          roomName: publishedRoomName,
          description: description.trim() || undefined,
          lanEnabled,
          relayIDs,
          preferLAN: relayConfig.preferLAN,
          visibility,
          advertisement: visibility === "private" ? undefined : {
            name: publishedRoomName,
            description: description.trim() || undefined,
            tags: tags.split(",").map((tag) => tag.trim()).filter(Boolean),
            capacity: Number(capacity) || undefined,
            showOnlineCount,
          },
        });
      } else if (mode === "join") await onJoin({ ...shared, host: joinHost.trim(), invite: connectionString.trim() || undefined, routes: joinRoutes.length ? joinRoutes : undefined, hostKey: joinHostKey || undefined, joinRef: joinRef || undefined });
      await onConnected?.();
    } catch { /* the controller keeps the recoverable error visible in this form */ }
  };

  return (
    <div className="collab-connect-shell">
      <header className="collab-connect-header">
        <div><span className="collab-eyebrow">{c("lan")}</span><h1>{c("title")}</h1><p>{c("subtitle")}</p></div>
        <button type="button" className="collab-quiet-button" onClick={onClose}>{c("close")}</button>
      </header>
      <main className="collab-connect-card">
        <div className="collab-mode-tabs" role="tablist" aria-label={c("title")}>
          <button type="button" role="tab" aria-selected={mode === "join"} onClick={() => setMode("join")}><Link2 size={16} />{c("join")}</button>
          <button type="button" role="tab" aria-selected={mode === "host"} onClick={() => setMode("host")}><RadioTower size={16} />{c("host")}</button>
          <button type="button" role="tab" aria-selected={mode === "relay"} onClick={() => setMode("relay")}><Server size={16} />{c("relayServer")}</button>
        </div>
        {mode === "relay" ? <RelayServerPanel
          config={relayConfig}
          roomQuery={roomQuery}
          loading={discoveryLoading}
          saving={relaySaving}
          discoveryError={discoveryError}
          configError={relayConfigError}
          onQueryRooms={onQueryRooms}
          onSave={onSaveRelayConfig}
          onSelectRoom={selectDiscoveredRoom}
        /> : <form onSubmit={submit} className="collab-connect-form">
          <label className="collab-field--wide"><span>{c("workspace")}</span><select value={workspaceRoot} onChange={(event) => onWorkspaceChange(event.target.value)} required autoComplete="off"><option value="">{c("workspacePlaceholder")}</option>{workspaces.map((workspace) => <option key={workspace.root} value={workspace.root}>{workspace.name} · {workspace.root}</option>)}</select></label>
          {!workspaceRoot && <div className="collab-warning"><AlertTriangle size={16} />{c("workspaceRequired")}</div>}
          {sessionResolving && <div className="collab-warning"><AlertTriangle size={16} />{c("workspacePreparing")}</div>}
          {sessionError && !sessionResolving && <div className="collab-error" role="alert">{sessionError} <button type="button" className="collab-quiet-button" onClick={onRetrySession}>{c("retry")}</button></div>}
          {mode === "host" && <fieldset className="collab-route-picker collab-field--wide">
            <legend>{c("connectionRoutes")}</legend>
            <label><input type="checkbox" checked={lanEnabled} onChange={(event) => setLANEnabled(event.target.checked)} /><span><b>{c("lanRoute")}</b><small>{c("lanRouteHint")}</small></span></label>
            {enabledRelays.map((relay) => <label key={relay.id}><input type="checkbox" checked={relayIDs.includes(relay.id)} onChange={(event) => selectRelay(relay.id, event.target.checked)} /><span><b>{relay.name || relay.id}</b><small>{relay.url}{relay.discovery ? ` · ${c("supportsDiscovery")}` : ""}</small></span></label>)}
            {enabledRelays.length === 0 && <small>{c("noRelayConfigured")}</small>}
          </fieldset>}
          {mode === "join" && <>
            <label className="collab-field--wide"><span>{c("connectionString")}</span><input name="connectionString" value={connectionString} onChange={(event) => importConnection(event.target.value)} placeholder="workground2://192.168.1.8:39170/room" autoComplete="off" /></label>
          </>}
          <label><span>{mode === "host" ? c("listenHost") : c("hostIP")}</span><input required={mode === "host" || joinRoutes.length === 0} value={currentHost} onChange={(event) => mode === "host" ? setListenHost(event.target.value) : setJoinHost(event.target.value)} autoComplete="off" /></label>
          <label><span>{c("port")}</span><input required={mode === "host" || joinRoutes.length === 0} type="number" min={mode === "host" ? 0 : joinRoutes.length ? 0 : 1} max="65535" value={port} onChange={(event) => setPort(event.target.value)} /></label>
          <label className="collab-field--wide"><span>{c("room")}</span><input name="room" required value={room} onChange={(event) => setRoom(event.target.value)} autoComplete="off" /></label>
          <label className="collab-field--wide"><span>{c("token")}</span><input value={token} onChange={(event) => setToken(event.target.value)} autoComplete="off" /></label>
          {mode === "host" && <fieldset className="collab-visibility collab-field--wide">
            <legend>{c("roomVisibility")}</legend>
            {(["private", "unlisted", "public"] as const).map((value) => <label key={value} className={value !== "private" && !discoverySelected ? "collab-option--disabled" : ""}><input type="radio" name="visibility" value={value} checked={visibility === value} disabled={value !== "private" && !discoverySelected} onChange={() => setVisibility(value)} /><span><b>{c(value === "private" ? "visibilityPrivate" : value === "unlisted" ? "visibilityUnlisted" : "visibilityPublic")}</b><small>{c(value === "private" ? "visibilityPrivateHint" : value === "unlisted" ? "visibilityUnlistedHint" : "visibilityPublicHint")}</small></span></label>)}
          </fieldset>}
          {mode === "host" && <details className="collab-advanced">
            <summary>{c("roomDetails")}</summary>
            <div className="collab-advanced-fields">
              <label><span>{c("roomName")}</span><input name="roomName" required={visibility === "public"} value={roomName || room} onChange={(event) => setRoomName(event.target.value)} /></label>
              <label><span>{c("roomDescription")}</span><input value={description} onChange={(event) => setDescription(event.target.value)} /></label>
              <label><span>{c("roomTags")}</span><input value={tags} onChange={(event) => setTags(event.target.value)} placeholder={c("roomTagsHint")} /></label>
              <label><span>{c("roomCapacity")}</span><input type="number" min="1" value={capacity} onChange={(event) => setCapacity(event.target.value)} /></label>
              <label className="collab-inline-check"><input type="checkbox" checked={showOnlineCount} onChange={(event) => setShowOnlineCount(event.target.checked)} /><span>{c("showOnlineCount")}</span></label>
            </div>
          </details>}
          <details
            className={`collab-advanced collab-identity${identityReady ? "" : " collab-identity--required"}`}
            open={identityOpen || !identityReady}
            onToggle={(event) => {
              if (!identityReady && !event.currentTarget.open) {
                event.currentTarget.open = true;
                setIdentityOpen(true);
                return;
              }
              setIdentityOpen(event.currentTarget.open);
            }}
          >
            <summary><UserRound size={14} />{c("localIdentity")}{!savedIdentity && <span>{c("firstIdentity")}</span>}</summary>
            <div className="collab-advanced-fields">
              {!savedIdentity && <p className="collab-identity-guide">{c("firstIdentityGuide")}</p>}
              <label><span>{c("memberName")}</span><input required value={identity.memberName} onChange={(event) => setIdentity({ ...identity, memberName: event.target.value })} autoComplete="name" /></label>
              <label><span>{c("memberRole")}</span><input value={identity.memberRole || ""} onChange={(event) => setIdentity({ ...identity, memberRole: event.target.value })} /></label>
              <label><span>{c("agentName")}</span><input required value={identity.agentName} onChange={(event) => setIdentity({ ...identity, agentName: event.target.value })} /></label>
              <label><span>{c("agentRole")}</span><input value={identity.agentRole || ""} onChange={(event) => setIdentity({ ...identity, agentRole: event.target.value })} /></label>
            </div>
          </details>
          {!token.trim() && <div className={`collab-warning${mode === "host" && visibility === "public" ? " collab-warning--high" : ""}`}><AlertTriangle size={16} />{mode === "host" && visibility === "public" ? c("publicNoTokenWarning") : c("noTokenWarning")}</div>}
          {(connectionError || (sessionID ? error : undefined)) && <div className="collab-error" role="alert">{connectionError || error}</div>}
          <button className="collab-primary-button" type="submit" disabled={busy || !sessionID}>{busy ? c("syncing") : mode === "host" ? c("create") : c("connect")}</button>
        </form>}
      </main>
    </div>
  );
}

function relayNeedsInsecureConsent(value: string): boolean {
  try {
    const url = new URL(value);
    if (url.protocol !== "ws:") return false;
    const host = url.hostname.toLowerCase();
    if (host === "localhost" || host === "::1") return false;
    const parts = host.split(".").map(Number);
    return parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255) || parts[0] !== 127;
  } catch {
    return value.trim().toLowerCase().startsWith("ws://");
  }
}

function RelayServerPanel({ config, roomQuery, loading, saving, discoveryError, configError, onQueryRooms, onSave, onSelectRoom }: {
  config: CollaborationRelayConfig;
  roomQuery: CollaborationRoomQueryResult;
  loading: boolean;
  saving: boolean;
  discoveryError?: string;
  configError?: string;
  onQueryRooms?(input: CollaborationRoomQueryInput): Promise<void>;
  onSave?(input: CollaborationRelayConfig): Promise<void>;
  onSelectRoom(item: CollaborationRoomQueryItem): void;
}) {
  const c = collabCopy(useT());
  const [draft, setDraft] = useState(config);
  const [query, setQuery] = useState("");
  const [saved, setSaved] = useState(false);
  useEffect(() => setDraft(config), [config]);
  const dirty = JSON.stringify(draft) !== JSON.stringify(config);
  const invalidRelay = draft.relays.some((relay) => !relay.id.trim() || !relay.url.trim());
  const insecureRelay = draft.relays.some((relay) => relayNeedsInsecureConsent(relay.url) && !relay.allowInsecure);
  const discoveryReady = config.relays.some((relay) => relay.enabled && relay.discovery);
  const patchRelay = (index: number, patch: Partial<CollaborationRelayConfig["relays"][number]>) => {
    setSaved(false);
    setDraft((current) => ({ ...current, relays: current.relays.map((relay, i) => i === index ? { ...relay, ...patch } : relay) }));
  };
  const addRelay = () => {
    setSaved(false);
    setDraft((current) => {
      const ids = new Set(current.relays.map((relay) => relay.id));
      let index = current.relays.length + 1;
      while (ids.has(`relay-${index}`)) index++;
      return { ...current, relays: [...current.relays, { id: `relay-${index}`, name: "Local Relay", url: "ws://127.0.0.1:8443/relay/v1/connect", enabled: true, priority: 100, discovery: true, allowInsecure: false }] };
    });
  };
  const save = async () => {
    if (!onSave) return;
    setSaved(false);
    try {
      await onSave(draft);
      setSaved(true);
    } catch { /* controller keeps the validation or persistence error visible */ }
  };
  const search = async () => {
    try {
      await onQueryRooms?.({ query: query.trim() || undefined, limit: 20 });
    } catch { /* controller keeps the recoverable discovery error visible */ }
  };

  return <section className="collab-relay-panel" role="tabpanel" aria-label={c("relayServer")}>
    <div className="collab-relay-panel__intro">
      <div><strong>{c("relayServer")}</strong><small>{c("relayServerHint")}</small></div>
      <button type="button" className="collab-quiet-button" disabled={saving} onClick={addRelay}><Plus size={14} />{c("addRelay")}</button>
    </div>
    <label className="collab-relay-prefer"><input type="checkbox" checked={draft.preferLAN} disabled={saving} onChange={(event) => { setSaved(false); setDraft((current) => ({ ...current, preferLAN: event.target.checked })); }} />{c("preferLAN")}</label>
    {draft.relays.length === 0 && <div className="collab-relay-empty">{c("noRelayServers")}</div>}
    <div className="collab-relay-list">{draft.relays.map((relay, index) => {
      const insecure = relayNeedsInsecureConsent(relay.url);
      return <article className="collab-relay-card" key={index}>
        <div className="collab-relay-card__head"><strong>{relay.name?.trim() || relay.id || c("unnamedRelay")}</strong><button type="button" className="collab-icon-button" aria-label={c("removeRelay")} disabled={saving} onClick={() => { setSaved(false); setDraft((current) => ({ ...current, relays: current.relays.filter((_, i) => i !== index) })); }}><Trash2 size={14} /></button></div>
        <div className="collab-relay-fields">
          <label><span>{c("relayID")}</span><input value={relay.id} disabled={saving} onChange={(event) => patchRelay(index, { id: event.target.value })} /></label>
          <label><span>{c("relayName")}</span><input value={relay.name || ""} disabled={saving} onChange={(event) => patchRelay(index, { name: event.target.value })} /></label>
          <label className="collab-field--wide"><span>{c("relayURL")}</span><input placeholder="wss://relay.example.com/relay/v1/connect" value={relay.url} disabled={saving} onChange={(event) => patchRelay(index, { url: event.target.value, ...(!relayNeedsInsecureConsent(event.target.value) ? { allowInsecure: false } : {}) })} /></label>
          <label><span>{c("relayPriority")}</span><input type="number" min="0" max="1000" value={relay.priority} disabled={saving} onChange={(event) => patchRelay(index, { priority: Number(event.target.value) || 0 })} /></label>
          <label><span>{c("relayTokenEnv")}</span><input placeholder="WORKGROUND2_RELAY_TOKEN" value={relay.accessTokenEnv || ""} disabled={saving} onChange={(event) => patchRelay(index, { accessTokenEnv: event.target.value })} /></label>
        </div>
        <div className="collab-relay-options">
          <label><input type="checkbox" checked={relay.enabled} disabled={saving} onChange={(event) => patchRelay(index, { enabled: event.target.checked })} />{c("relayEnabled")}</label>
          <label><input type="checkbox" checked={relay.discovery} disabled={saving} onChange={(event) => patchRelay(index, { discovery: event.target.checked })} />{c("supportsDiscovery")}</label>
        </div>
        {insecure && <label className={`collab-warning collab-relay-insecure${relay.allowInsecure ? "" : " collab-warning--high"}`}><input type="checkbox" checked={Boolean(relay.allowInsecure)} disabled={saving} onChange={(event) => patchRelay(index, { allowInsecure: event.target.checked })} /><span><b>{c("allowInsecureRelay")}</b><small>{c("insecureRelayWarning")}</small></span></label>}
      </article>;
    })}</div>
    {(configError || invalidRelay || insecureRelay) && <div className="collab-error" role="alert">{configError || (invalidRelay ? c("relayFieldsRequired") : c("insecureRelayRequired"))}</div>}
    <div className="collab-relay-save"><span>{saved && !dirty ? c("relaySaved") : ""}</span><button type="button" className="collab-primary-button" disabled={saving || !dirty || invalidRelay || insecureRelay || !onSave} onClick={() => void save()}>{saving ? c("savingRelay") : c("saveRelay")}</button></div>
    <section className="collab-discovery" aria-label={c("activeRooms")}>
      <div><strong>{c("activeRooms")}</strong><span><input value={query} onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); void search(); } }} placeholder={c("searchRooms")} /><button type="button" className="collab-quiet-button" disabled={loading || !discoveryReady} onClick={() => void search()}><RefreshCw size={14} />{loading ? c("searching") : c("search")}</button></span></div>
      {!discoveryReady && <small>{dirty ? c("saveDiscoveryRelayFirst") : c("noDiscoveryRelay")}</small>}
      {discoveryError && <p className="collab-invite-error">{discoveryError}</p>}
      {discoveryReady && roomQuery.rooms.length > 0 && <div className="collab-discovery-list">{roomQuery.rooms.map((item) => <button type="button" key={item.publicRoomId} onClick={() => onSelectRoom(item)}><span><b>{item.name}</b><small>{item.description || item.room}</small><em>{(item.tags || []).join(" · ")}</em></span><span>{item.onlineCount !== undefined ? c("onlineCount", { n: item.onlineCount }) : c("hostReachable")}{item.requiresToken ? ` · ${c("tokenRequired")}` : ""}</span></button>)}</div>}
      {!loading && discoveryReady && roomQuery.rooms.length === 0 && <small>{c("noActiveRooms")}</small>}
    </section>
  </section>;
}
