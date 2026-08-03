import { useState, type FormEvent } from "react";
import { AlertTriangle, Link2, RadioTower } from "lucide-react";
import { useT } from "../../lib/i18n";
import { collabCopy } from "../copy";
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

export function ConnectionPanel({ sessionID, status, error, initial, onHost, onJoin, onClose, onConnected }: ConnectionPanelProps) {
  const c = collabCopy(useT());
  const [mode, setMode] = useState<"host" | "join">("join");
  const readSaved = (key: string, fallback: string) => {
    try { return localStorage.getItem(`collab:${key}`) || fallback; } catch { return fallback; }
  };
  const [host, setHost] = useState(() => initial?.host || readSaved("host", "127.0.0.1"));
  const [port, setPort] = useState(() => String(initial?.port || readSaved("port", "39170")));
  const [room, setRoom] = useState(() => initial?.room || readSaved("room", ""));
  const [token, setToken] = useState("");
  const [memberID] = useState(() => {
    const saved = readSaved("memberID", "");
    const next = saved || (typeof crypto !== "undefined" && "randomUUID" in crypto ? `member-${crypto.randomUUID()}` : `member-${Date.now()}`);
    try { localStorage.setItem("collab:memberID", next); } catch { /* private mode */ }
    return next;
  });
  const [memberName, setMemberName] = useState(() => readSaved("memberName", c("defaultMember")));
  const [agentName, setAgentName] = useState(() => readSaved("agentName", c("defaultAgent")));
  const [roomName, setRoomName] = useState(() => initial?.title || "");
  const [description, setDescription] = useState(() => initial?.description || "");
  const busy = status === "connecting" || status === "syncing";

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const resolvedMemberName = memberName.trim() || c("defaultMember");
    const resolvedAgentName = agentName.trim() || c("defaultAgent");
    try {
      localStorage.setItem("collab:host", host.trim()); localStorage.setItem("collab:port", port); localStorage.setItem("collab:room", room.trim());
      localStorage.setItem("collab:memberName", resolvedMemberName); localStorage.setItem("collab:agentName", resolvedAgentName); localStorage.setItem("collab:memberID", memberID);
    } catch { /* private mode: the backend still returns a recoverable member identity */ }
    const shared = { port: Number(port), room: room.trim(), token: token.trim() || undefined, memberID, memberName: resolvedMemberName, agentName: resolvedAgentName, sessionID };
    try {
      if (mode === "host") await onHost({ ...shared, listenHost: host.trim(), roomName: roomName.trim() || undefined, description: description.trim() || undefined });
      else await onJoin({ ...shared, host: host.trim() });
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
          <label><span>{mode === "host" ? c("listenHost") : c("hostIP")}</span><input required value={host} onChange={(event) => setHost(event.target.value)} autoComplete="off" /></label>
          <label><span>{c("port")}</span><input required type="number" min="1" max="65535" value={port} onChange={(event) => setPort(event.target.value)} /></label>
          <label><span>{c("room")}</span><input required value={room} onChange={(event) => setRoom(event.target.value)} autoComplete="off" /></label>
          <label><span>{c("token")}</span><input value={token} onChange={(event) => setToken(event.target.value)} autoComplete="off" /></label>
          {mode === "host" && <details className="collab-advanced">
            <summary>{c("roomDetails")}</summary>
            <div className="collab-advanced-fields">
              <label><span>{c("roomName")}</span><input value={roomName} onChange={(event) => setRoomName(event.target.value)} /></label>
              <label><span>{c("roomDescription")}</span><input value={description} onChange={(event) => setDescription(event.target.value)} /></label>
            </div>
          </details>}
          <details className="collab-advanced">
            <summary>{c("localIdentity")}</summary>
            <div className="collab-advanced-fields">
              <label><span>{c("memberName")}</span><input value={memberName} onChange={(event) => setMemberName(event.target.value)} autoComplete="name" /></label>
              <label><span>{c("agentName")}</span><input value={agentName} onChange={(event) => setAgentName(event.target.value)} /></label>
            </div>
          </details>
          {!token.trim() && <div className="collab-warning"><AlertTriangle size={16} />{c("noTokenWarning")}</div>}
          {error && <div className="collab-error" role="alert">{error}</div>}
          <button className="collab-primary-button" type="submit" disabled={busy || !sessionID || !host.trim() || Number(port) < 1 || Number(port) > 65535 || !room.trim()}>{busy ? c("syncing") : mode === "host" ? c("create") : c("connect")}</button>
        </form>
      </main>
    </div>
  );
}
