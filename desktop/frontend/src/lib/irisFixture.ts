// IrisFixture — dev-only fixture data for the ?uiFixture=iris query mode.
// Produces the exact chapter 16 state: sessions, memory line, assistant messages,
// completed/active runs, artifacts, config, and AddOn instances.
//
// This module is tree-shakeable: imported only when the fixture is active.
// No component imports — pure data factories only.

import type { MemoryLine } from "../store/memory";
import type { RunRecord, RunEvent } from "../store/run";
import type { ArtifactRecord } from "../store/artifacts";
import type { QueueItem } from "../store/composerQueue";
import type { AddOnInstance } from "../store/addonSurface";

// ── Session state ─────────────────────────────────────────────────────────────

export interface IrisFixtureSession {
  currentWorkspaceName: string;
  secondaryWorkspaceName: string;
  currentSessionTitle: string;
  currentSessionLine1: string;
  currentSessionLine2: string;
  currentSessionLine3: string;
}

export function irisFixtureSession(): IrisFixtureSession {
  return {
    currentWorkspaceName: "WorkGround2",
    secondaryWorkspaceName: "joyquant-db",
    currentSessionTitle: "桌面信息架构重构",
    currentSessionLine1: "桌面信息架构重构", // current session
    currentSessionLine2: "任务执行进度组件",
    currentSessionLine3: "会话缓存策略",
  };
}

// ── Memory line ───────────────────────────────────────────────────────────────

export function irisFixtureMemory(): MemoryLine {
  return {
    goal: "重构桌面信息架构",
    current: "Artifact 模型已验证",
    nextStep: "实现持久化",
  };
}

// ── Transcript items ──────────────────────────────────────────────────────────

export interface IrisFixtureTranscriptItem {
  id: string;
  role: "user" | "assistant";
  text: string;
}

export function irisFixtureTranscript(): IrisFixtureTranscriptItem[] {
  return [
    {
      id: "assistant-1",
      role: "assistant" as const,
      text: "已调整为两级导航结构，核心路径保持不变。",
    },
    {
      id: "assistant-2",
      role: "assistant" as const,
      text: "好的，已制定持久化方案并完成 PoC 验证。\n\n将采用本地存储并预留云端同步接口，确保数据一致性与恢复能力。\n\n后续会输出设计文档与实现计划。",
    },
  ];
}

// ── Run data ──────────────────────────────────────────────────────────────────

export function irisFixtureCompletedRun(): RunRecord {
  const events: RunEvent[] = [
    { eventId: "cr-e1", content: "已读 1 个文件", stepLabel: "已读 1 个文件" },
    { eventId: "cr-e2", content: "思考 8 秒", stepLabel: "思考 8 秒" },
    { eventId: "cr-e3", content: "delete_range task_test.go", stepLabel: "delete_range task_test.go" },
    { eventId: "cr-e4", content: "运行完成", stepLabel: "+4" },
  ];
  return {
    runId: "cr-1",
    sessionId: "fixture-session",
    turnId: "fixture-turn-1",
    status: "completed",
    events,
    expanded: false,
    startedAt: Date.now() - 24000,
    completedAt: Date.now(),
  };
}

export function irisFixtureActiveRun(): RunRecord {
  const now = Date.now();
  const events: RunEvent[] = [
    { eventId: "ar-e1", content: "14:32:11  读取 internal/agent/task_test.go", stepLabel: "步骤 1" },
    { eventId: "ar-e2", content: "14:32:13  分析 delete_range 调用", stepLabel: "步骤 2" },
    { eventId: "ar-e3", content: "14:32:17  正在运行 go test ./internal/agent/...", stepLabel: "步骤 3" },
    { eventId: "ar-e4", content: "14:32:22  等待测试结果...", stepLabel: "步骤 4" },
  ];
  return {
    runId: "ar-1",
    sessionId: "fixture-session",
    turnId: "fixture-turn-2",
    status: "running",
    events,
    expanded: true,
    startedAt: now - 18000,
  };
}

// ── Artifacts ─────────────────────────────────────────────────────────────────

export function irisFixtureArtifacts(): ArtifactRecord[] {
  return [
    {
      artifactId: "art-1",
      sessionId: "fixture-session",
      name: "WorkGround2.exe",
      type: "binary",
      status: "available",
      path: "D:\\Work\\WorkGround2\\desktop\\build\\bin\\WorkGround2.exe",
    },
    {
      artifactId: "art-2",
      sessionId: "fixture-session",
      name: "调试入口",
      type: "debug",
      status: "available",
      path: "D:\\Work\\WorkGround2\\desktop",
    },
    {
      artifactId: "art-3",
      sessionId: "fixture-session",
      name: "debug.bat",
      type: "script",
      status: "available",
      path: "D:\\Work\\WorkGround2\\scripts\\debug.bat",
    },
    {
      artifactId: "art-4",
      sessionId: "fixture-session",
      name: "测试包.zip",
      type: "archive",
      status: "available",
      path: "D:\\Work\\WorkGround2\\dist\\测试包.zip",
    },
  ];
}

// ── Runtime config values ────────────────────────────────────────────────────

export interface IrisFixtureConfig {
  modelId: string;
  contextPercent: number;
  runtimeStatus: string;
  collaborationMode: string;
  approvalMode: string;
}

export function irisFixtureConfig(): IrisFixtureConfig {
  return {
    modelId: "DeepSeek-R1",
    contextPercent: 33,
    runtimeStatus: "运行中",
    collaborationMode: "normal",
    approvalMode: "ask",
  };
}

// ── AddOn instances ──────────────────────────────────────────────────────────

export function irisFixtureAddOns(): AddOnInstance[] {
  return [
    {
      instanceId: "addon-login",
      pluginId: "team",
      panelId: "login",
      scope: "window",
      status: "needs_input",
      density: "focus",
      pinned: false,
      activationOrder: 1,
      title: "团队登录",
    },
    {
      instanceId: "addon-build",
      pluginId: "build",
      panelId: "monitor",
      scope: "session",
      status: "active",
      density: "peek",
      pinned: false,
      activationOrder: 2,
      title: "构建监控",
      message: "构建进度 47%",
    },
    {
      instanceId: "addon-media",
      pluginId: "media",
      panelId: "preview",
      scope: "session",
      status: "playing",
      density: "focus",
      pinned: false,
      activationOrder: 3,
      title: "媒体预览",
      message: "demo.mp4",
    },
  ];
}

// ── Queue items ──────────────────────────────────────────────────────────────

export function irisFixtureQueueItems(): QueueItem[] {
  return [
    {
      queueItemId: "q-1",
      requestId: "req-1",
      content: "查看测试结果并总结",
      createdAt: Date.now() - 5000,
    },
    {
      queueItemId: "q-2",
      requestId: "req-2",
      content: "提交代码审查",
      createdAt: Date.now() - 3000,
    },
  ];
}

// ── Active session ID ────────────────────────────────────────────────────────

export const FIXTURE_SESSION_ID = "fixture-session";
