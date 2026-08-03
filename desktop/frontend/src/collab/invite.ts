export interface CollaborationInviteValue {
  host: string;
  port: number;
  room: string;
  token?: string;
}

const inviteScheme = "workground2:";

export function buildCollaborationInvite(value: CollaborationInviteValue): string {
  const host = value.host.trim();
  const room = value.room.trim();
  if (!host || !room || !Number.isInteger(value.port) || value.port < 1 || value.port > 65535) {
    throw new Error("invalid collaboration invite");
  }
  const authorityHost = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  const token = value.token?.trim();
  return `${inviteScheme}//${authorityHost}:${value.port}/${encodeURIComponent(room)}${token ? `?token=${encodeURIComponent(token)}` : ""}`;
}

export function parseCollaborationInvite(value: string): CollaborationInviteValue {
  let url: URL;
  try {
    url = new URL(value.trim());
  } catch {
    throw new Error("invalid collaboration invite");
  }
  const room = decodeURIComponent(url.pathname.replace(/^\/+/, "")).trim();
  const port = Number(url.port);
  const host = url.hostname.replace(/^\[|\]$/g, "").trim();
  if (url.protocol !== inviteScheme || !host || !room || !Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error("invalid collaboration invite");
  }
  return { host, port, room, token: url.searchParams.get("token")?.trim() || undefined };
}
