import { Bot } from "lucide-react";

export function CollaborationAvatar({ name, src, agent = false, className = "" }: { name: string; src?: string; agent?: boolean; className?: string }) {
  return <span className={`collab-avatar${agent ? " collab-avatar--agent" : ""}${src ? " collab-avatar--image" : ""}${className ? ` ${className}` : ""}`} aria-hidden="true">
    {src ? <img src={src} alt="" /> : agent ? <Bot size={17} /> : name.trim().slice(0, 1).toUpperCase()}
  </span>;
}
