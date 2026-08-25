// Heartbeat panel bridge — typed wrappers around app heartbeat bindings.
// Custom components should import from here instead of calling app.* directly
// so that heartbeat-specific calls are scoped to this feature.

import { app } from "../../../lib/bridge";
import type { HeartbeatConversionResult, HeartbeatConversionStatus, HeartbeatTask } from "./heartbeat.types";

export function heartbeatListTasks(): Promise<HeartbeatTask[]> {
  return app.HeartbeatReloadTasks().then((v) => (v ?? []) as HeartbeatTask[]);
}

export function heartbeatSaveTasks(tasks: HeartbeatTask[]): Promise<void> {
  return app.HeartbeatSaveTasks(tasks as unknown);
}

export function heartbeatTriggerNow(id: string): Promise<void> {
  return app.HeartbeatTriggerNow(id);
}

export function heartbeatGenerateID(): Promise<string> {
  return app.HeartbeatGenerateID();
}

export function heartbeatListConversions(): Promise<HeartbeatConversionStatus[]> {
  return app.HeartbeatListConversions().then((v) => (v ?? []) as HeartbeatConversionStatus[]);
}

export function heartbeatConvertToAssistant(id: string): Promise<HeartbeatConversionResult> {
  return app.HeartbeatConvertToAssistant(id) as Promise<HeartbeatConversionResult>;
}
