export interface CollaborationIdentity {
  memberID: string;
  memberName: string;
  memberRole?: string;
  agentID: string;
  agentName: string;
  agentRole?: string;
}

const identityKey = "collab:identity:v1";
const placeholderMembers = new Set(["Member", "成员", "成員"]);
const placeholderAgents = new Set(["Personal Agent"]);

function localID(prefix: string): string {
  const suffix = typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `${prefix}-${suffix}`;
}

export function newCollaborationIdentity(): CollaborationIdentity {
  return { memberID: localID("member"), memberName: "", agentID: localID("agent"), agentName: "" };
}

export function loadCollaborationIdentity(): CollaborationIdentity | undefined {
  try {
    const raw = localStorage.getItem(identityKey);
    if (raw) {
      const value = JSON.parse(raw) as Partial<CollaborationIdentity>;
      if (value.memberID && value.memberName?.trim() && value.agentID && value.agentName?.trim()) {
        return {
          memberID: value.memberID,
          memberName: value.memberName.trim(),
          memberRole: value.memberRole?.trim() || undefined,
          agentID: value.agentID,
          agentName: value.agentName.trim(),
          agentRole: value.agentRole?.trim() || undefined,
        };
      }
    }
    const memberName = localStorage.getItem("collab:memberName")?.trim();
    const agentName = localStorage.getItem("collab:agentName")?.trim();
    if (memberName && agentName && !placeholderMembers.has(memberName) && !placeholderAgents.has(agentName)) {
      return {
        memberID: localStorage.getItem("collab:memberID") || localID("member"),
        memberName,
        agentID: localStorage.getItem("collab:agentID") || localID("agent"),
        agentName,
      };
    }
  } catch { /* local storage may be unavailable */ }
  return undefined;
}

export function saveCollaborationIdentity(value: CollaborationIdentity): CollaborationIdentity {
  const identity = {
    memberID: value.memberID || localID("member"),
    memberName: value.memberName.trim(),
    memberRole: value.memberRole?.trim() || undefined,
    agentID: value.agentID || localID("agent"),
    agentName: value.agentName.trim(),
    agentRole: value.agentRole?.trim() || undefined,
  };
  if (!identity.memberName || !identity.agentName) throw new Error("collaboration identity is incomplete");
  try { localStorage.setItem(identityKey, JSON.stringify(identity)); } catch { /* the in-memory identity remains usable */ }
  return identity;
}
