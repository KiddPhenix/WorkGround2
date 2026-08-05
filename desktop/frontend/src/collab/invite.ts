import type { CollaborationRouteInput } from "./types";

export interface CollaborationInviteV1 {
  version?: 1;
  host: string;
  port: number;
  room: string;
  token?: string;
}

export interface CollaborationInviteV2 {
  version: 2;
  room: string;
  hostKey: string;
  routes: CollaborationRouteInput[];
  roomToken?: string;
}

export type CollaborationInviteValue = CollaborationInviteV1 | CollaborationInviteV2;

const inviteScheme = "workground2:";

export function tryBuildCollaborationInvite(value: CollaborationInviteValue): string {
  try {
    return buildCollaborationInvite(value);
  } catch {
    return "";
  }
}

export function buildCollaborationInvite(value: CollaborationInviteValue): string {
  if (value.version === 2) {
    const room = value.room.trim();
    const hostKey = value.hostKey.trim();
    if (!room || !hostKey || value.routes.length === 0) throw new Error("invalid collaboration invite");
    const bytes = new TextEncoder().encode(JSON.stringify({
      version: 2,
      room,
      hostKey,
      routes: value.routes,
      roomToken: value.roomToken?.trim() || undefined,
    }));
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return `${inviteScheme}//join#v2.${btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")}`;
  }
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
  if (url.protocol === inviteScheme && url.hostname === "join" && url.hash.startsWith("#v2.")) {
    try {
      const encoded = url.hash.slice(4).replace(/-/g, "+").replace(/_/g, "/");
      const binary = atob(encoded.padEnd(Math.ceil(encoded.length / 4) * 4, "="));
      const payload = JSON.parse(new TextDecoder().decode(Uint8Array.from(binary, (char) => char.charCodeAt(0)))) as Record<string, unknown>;
      const room = typeof payload.room === "string" ? payload.room.trim() : "";
      const hostKey = typeof payload.hostKey === "string" ? payload.hostKey.trim() : "";
      const routes = Array.isArray(payload.routes) ? payload.routes.filter(validRoute) as CollaborationRouteInput[] : [];
      if (payload.version !== 2 || !room || !hostKey || routes.length === 0) throw new Error();
      return { version: 2, room, hostKey, routes, roomToken: typeof payload.roomToken === "string" ? payload.roomToken.trim() || undefined : undefined };
    } catch {
      throw new Error("invalid collaboration invite");
    }
  }
  const room = decodeURIComponent(url.pathname.replace(/^\/+/, "")).trim();
  const port = Number(url.port);
  const host = url.hostname.replace(/^\[|\]$/g, "").trim();
  if (url.protocol !== inviteScheme || !host || !room || !Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error("invalid collaboration invite");
  }
  return { host, port, room, token: url.searchParams.get("token")?.trim() || undefined };
}

function validRoute(value: unknown): boolean {
  if (!value || typeof value !== "object") return false;
  const route = value as Record<string, unknown>;
  if (route.kind === "lan") return typeof route.host === "string" && Number.isInteger(route.port) && Number(route.port) > 0 && Number(route.port) <= 65535;
  return route.kind === "relay"
    && typeof route.relayId === "string"
    && typeof route.url === "string"
    && typeof route.tunnelId === "string"
    && typeof route.guestCapability === "string";
}
