import { useState, type FormEvent } from "react";
import { AlertTriangle, Link2, RadioTower, UserRound } from "lucide-react";
import { useT } from "../../lib/i18n";
import { collabCopy } from "../copy";
import { loadCollaborationIdentity, newCollaborationIdentity, saveCollaborationIdentity } from "../identity";
import { parseCollaborationInvite } from "../invite";
import type { CollaborationRoom, HostCollaborationRoomInput, JoinCollaborationRoomInput } from "../types";

interface ConnectionPanelProps {
  sessionID: string;
  status: string;
  error?: string;
  initial?: CollaborationRoom;
  onHost(input: HostCollaborationRoomInput): Promise<void>;
  onJoin(input: JoinCollaborationRoomInput): Promise<void>;
  onClose(): void;
  onConnected?(): Promise<void> | void;
}

function readSaved(key: string, fallback: string): string {
  try { return localStorage.getItem(`collab:${key}`) || fallback; } catch { return fallback; }
}

export function ConnectionPanel({ sessionID, status, error, initial, onHost, onJoin, onClose, onConnected }: ConnectionPanelProps) {
  const c = collabCopy(useT());
  const [mode, setMode] = useState<"host" | "join">("join");
  const [joinHost, setJoinHost] = useState(() => initial?.host || readSaved("host", "127.0.0.1"));
  const [listenHost, setListenHost] = useState(() => readSaved("listenHost", "0.0.0.0"));
  const [port, setPort] = useState(() => String(initial?.port || readSaved("port", "39170")));
  const [room, setRoom] = useState(() => initial?.room || readSaved("room", ""));
  const [token, setToken] = useState("");
  const [connectionString, setConnectionString] = useState("");
  const [connectionError, setConnectionError] = useState("");
  const [savedIdentity] = useState(loadCollaborationIdentity);
  const [identity, setIdentity] = useState(() => savedIdentity || newCollaborationIdentity());
  const [identityOpen, setIdentityOpen] = useState(() => !savedIdentity);
  const [roomName, setRoomName] = useState(() => initial?.title || "");
  const [description, setDescription] = useState(() => initial?.description || "");
  const busy = status === "connecting" || status === "syncing";
  const identityReady = Boolean(identity.memberName.trim() && identity.agentName.trim());
  const currentHost = mode === "host" ? listenHost : joinHost;

  const importConnection = (value: string) => {
    setConnectionString(value);
    if (!value.trim()) {
      setConnectionError("");
      return;
    }
    try {
      const parsed = parseCollaborationInvite(value);
      setJoinHost(parsed.host);
      setPort(String(parsed.port));
      setRoom(parsed.room);
      setToken(parsed.token || "");
      setConnectionError("");
    } catch {
      setConnectionError(c("connectionStringInvalid"));
    }
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!identityReady) {
      setIdentityOpen(true);
      return;
    }
    const saved = saveCollaborationIdentity(identity);
    try {
      localStorage.setItem("collab:host", joinHost.trim());
      localStorage.setItem("collab:listenHost", listenHost.trim());
      localStorage.setItem("collab:port", port);
      localStorage.setItem("collab:room", room.trim());
    } catch { /* private mode: the current form values remain usable */ }
    const shared = {
      port: Number(port), room: room.trim(), token: token.trim() || undefined,
      memberID: saved.memberID, memberName: saved.memberName, memberRole: saved.memberRole,
      agentID: saved.agentID, agentName: saved.agentName, agentRole: saved.agentRole, sessionID,
    };
    try {
      if (mode === "host") await onHost({ ...shared, listenHost: listenHost.trim(), roomName: roomName.trim() || undefined, description: description.trim() || undefined });
      else await onJoin({ ...shared, host: joinHost.trim() });
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
        </div>
        <form onSubmit={submit} className="collab-connect-form">
          {mode === "join" && <label className="collab-field--wide"><span>{c("connectionString")}</span><input value={connectionString} onChange={(event) => importConnection(event.target.value)} placeholder="workground2://192.168.1.8:39170/room" autoComplete="off" /></label>}
          <label><span>{mode === "host" ? c("listenHost") : c("hostIP")}</span><input required value={currentHost} onChange={(event) => mode === "host" ? setListenHost(event.target.value) : setJoinHost(event.target.value)} autoComplete="off" /></label>
          <label><span>{c("port")}</span><input required type="number" min={mode === "host" ? 0 : 1} max="65535" value={port} onChange={(event) => setPort(event.target.value)} /></label>
          <label className="collab-field--wide"><span>{c("room")}</span><input required value={room} onChange={(event) => setRoom(event.target.value)} autoComplete="off" /></label>
          <label className="collab-field--wide"><span>{c("token")}</span><input value={token} onChange={(event) => setToken(event.target.value)} autoComplete="off" /></label>
          {mode === "host" && <details className="collab-advanced">
            <summary>{c("roomDetails")}</summary>
            <div className="collab-advanced-fields">
              <label><span>{c("roomName")}</span><input value={roomName} onChange={(event) => setRoomName(event.target.value)} /></label>
              <label><span>{c("roomDescription")}</span><input value={description} onChange={(event) => setDescription(event.target.value)} /></label>
            </div>
          </details>}
          <details className={`collab-advanced collab-identity${identityReady ? "" : " collab-identity--required"}`} open={identityOpen} onToggle={(event) => setIdentityOpen(event.currentTarget.open)}>
            <summary><UserRound size={14} />{c("localIdentity")}{!savedIdentity && <span>{c("firstIdentity")}</span>}</summary>
            <div className="collab-advanced-fields">
              {!savedIdentity && <p className="collab-identity-guide">{c("firstIdentityGuide")}</p>}
              <label><span>{c("memberName")}</span><input required value={identity.memberName} onChange={(event) => setIdentity({ ...identity, memberName: event.target.value })} autoComplete="name" /></label>
              <label><span>{c("memberRole")}</span><input value={identity.memberRole || ""} onChange={(event) => setIdentity({ ...identity, memberRole: event.target.value })} /></label>
              <label><span>{c("agentName")}</span><input required value={identity.agentName} onChange={(event) => setIdentity({ ...identity, agentName: event.target.value })} /></label>
              <label><span>{c("agentRole")}</span><input value={identity.agentRole || ""} onChange={(event) => setIdentity({ ...identity, agentRole: event.target.value })} /></label>
            </div>
          </details>
          {!token.trim() && <div className="collab-warning"><AlertTriangle size={16} />{c("noTokenWarning")}</div>}
          {(connectionError || error) && <div className="collab-error" role="alert">{connectionError || error}</div>}
          <button className="collab-primary-button" type="submit" disabled={busy || !sessionID || !currentHost.trim() || Number(port) < (mode === "host" ? 0 : 1) || Number(port) > 65535 || !room.trim() || !identityReady}>{busy ? c("syncing") : mode === "host" ? c("create") : c("connect")}</button>
        </form>
      </main>
    </div>
  );
}
