import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { MCPServersSettingsPage, PluginsSettingsPage } from "../components/CapabilitiesPanel";
import type { AppBindings } from "../lib/bridge";
import { LocaleProvider } from "../lib/i18n";
import { mcpServerLifecycleActions, mcpServerRetryableFromAvailableList } from "../lib/mcpServerLifecycle";
import type { Meta, PluginInstallOptions, PluginView, ServerView, TabMeta } from "../lib/types";

function ok(value: unknown, message: string) {
  if (!value) throw new Error(message);
}

function server(status: ServerView["status"]): ServerView {
  return {
    name: "codegraph",
    transport: "stdio",
    status,
    configured: true,
    autoStart: true,
    tier: "background",
    tools: 0,
    prompts: 0,
    resources: 0,
  };
}

const initializing = mcpServerLifecycleActions(server("initializing"));
ok(initializing.enabled, "initializing server should still be treated as enabled");
ok(!initializing.showRetryInRow, "initializing server should not expose retry until it fails");
ok(!initializing.canReconnect, "initializing server should not expose reconnect while already connecting");
ok(!initializing.canConnectNow, "initializing server should not use the deferred connect-now action");

const connected = mcpServerLifecycleActions(server("connected"));
ok(!connected.showRetryInRow, "connected server row should keep the toggle UI");
ok(connected.canReconnect, "connected server details should expose reconnect");

const manuallyConnected = mcpServerLifecycleActions({ ...server("connected"), autoStart: false, startIntent: "off", runtimeState: "ready" });
ok(manuallyConnected.enabled, "connected manual server should still render as enabled");
ok(!manuallyConnected.canConnectNow, "connected manual server should not expose connect-now");
ok(manuallyConnected.canReconnect, "connected manual server should expose reconnect");

const automaticIdle = mcpServerLifecycleActions({ ...server("deferred"), startIntent: "automatic" });
ok(!automaticIdle.canConnectNow, "automatic idle server should not look like a manual connector");
ok(!automaticIdle.canReconnect, "automatic idle server should wait for background connection or failure");

const failed = mcpServerLifecycleActions({ ...server("failed"), runtimeState: "issue" });
ok(failed.showRetryInRow, "failed server row should expose retry");

ok(mcpServerRetryableFromAvailableList(server("initializing")), "connecting server should be included in available-list retry all");
ok(mcpServerRetryableFromAvailableList({ ...server("deferred"), startIntent: "automatic" }), "automatic idle server should be included in available-list retry all");
ok(!mcpServerRetryableFromAvailableList(server("connected")), "connected server should be excluded from available-list retry all");
ok(!mcpServerRetryableFromAvailableList({ ...server("disabled"), startIntent: "off" }), "disabled server should be excluded from available-list retry all");
ok(!mcpServerRetryableFromAvailableList({ ...server("failed"), runtimeState: "issue" }), "failed server is handled by the failure banner retry all");

function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    await act(async () => {
      await flush();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

function installDom() {
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.HTMLButtonElement = dom.window.HTMLButtonElement;
  globalThis.HTMLInputElement = dom.window.HTMLInputElement;
  globalThis.Event = dom.window.Event;
  globalThis.KeyboardEvent = dom.window.KeyboardEvent;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.localStorage = dom.window.localStorage;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: () => ({
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }),
  });
  return dom;
}

function findButton(label: string, root: ParentNode = document): HTMLButtonElement | undefined {
  return Array.from(root.querySelectorAll("button")).find((button) => button.textContent?.trim() === label) as HTMLButtonElement | undefined;
}

function setInputValue(input: HTMLInputElement, value: string) {
  const win = input.ownerDocument.defaultView;
  const setter = Object.getOwnPropertyDescriptor((win?.HTMLInputElement ?? HTMLInputElement).prototype, "value")?.set;
  setter?.call(input, value);
  const eventCtor = win?.Event ?? Event;
  const inputEventCtor = win?.InputEvent ?? eventCtor;
  input.dispatchEvent(new inputEventCtor("input", { bubbles: true, data: value, inputType: "insertText" } as InputEventInit));
  input.dispatchEvent(new eventCtor("change", { bubbles: true }));
}

console.log("capabilities panel MCP actions");

{
  const dom = installDom();
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  const meta: Meta = { label: "test", ready: true, eventChannel: "test-channel", cwd: "/tmp/WorkGround2-test", workspaceRoot: "/tmp/WorkGround2-test" };
  const tabs: TabMeta[] = [{
    id: "tab-1",
    scope: "project",
    workspaceRoot: "/tmp/WorkGround2-test",
    workspaceName: "WorkGround2-test",
    topicId: "topic-1",
    topicTitle: "Test",
    label: "Test",
    ready: true,
    running: false,
    mode: "normal",
    toolApprovalMode: "auto",
    active: true,
    cwd: "/tmp/WorkGround2-test",
  }];
  let trustCalls = 0;
  let bulkTrustCalls = 0;
  let untrustCalls = 0;
  let servers: ServerView[] = [{
    name: "github",
    transport: "stdio",
    status: "connected",
    configured: true,
    autoStart: true,
    tools: 2,
    prompts: 0,
    resources: 0,
    toolList: [
      { name: "issue_read", description: "Read issues.", readOnlyHint: true },
      { name: "issue_write", description: "Write issues." },
    ],
    trustedReadOnlyTools: [],
  }];
  window.go = {
    main: {
      App: {
        Meta: async () => meta,
        ListTabs: async () => tabs,
        MCPServers: async () => servers.map((s) => ({
          ...s,
          toolList: s.toolList?.map((tool) => ({ ...tool })),
          trustedReadOnlyTools: [...(s.trustedReadOnlyTools ?? [])],
        })),
        TrustMCPServerTool: async (name: string, toolName: string) => {
          trustCalls += 1;
          servers = servers.map((s) => s.name === name ? { ...s, trustedReadOnlyTools: [...(s.trustedReadOnlyTools ?? []), toolName] } : s);
        },
        TrustMCPServerTools: async (name: string, toolNames: string[]) => {
          bulkTrustCalls += 1;
          servers = servers.map((s) => {
            if (s.name !== name) return s;
            const trusted = Array.from(new Set([...(s.trustedReadOnlyTools ?? []), ...toolNames]));
            return { ...s, trustedReadOnlyTools: trusted };
          });
        },
        UntrustMCPServerTool: async (name: string, toolName: string) => {
          untrustCalls += 1;
          servers = servers.map((s) => s.name === name ? { ...s, trustedReadOnlyTools: (s.trustedReadOnlyTools ?? []).filter((tool) => tool !== toolName) } : s);
        },
      } as Partial<AppBindings> as AppBindings,
    },
  };

  await act(async () => {
    root.render(React.createElement(LocaleProvider, null, React.createElement(MCPServersSettingsPage)));
    await flush();
  });
  await waitFor("github server row", () => Boolean(document.querySelector(".cap-row__name")?.textContent?.includes("github")));

  const disclosure = document.querySelector<HTMLButtonElement>(".cap-disclosure");
  if (!disclosure) throw new Error("missing MCP disclosure button");
  await act(async () => {
    disclosure.click();
    await flush();
  });

  const trustReadOnly = findButton("Pre-trust read-only (1)");
  if (!trustReadOnly) throw new Error("missing bulk Pre-trust read-only button");
  await act(async () => {
    trustReadOnly.click();
    await flush();
  });
  await waitFor("bulk trusted tool", () => servers[0]?.trustedReadOnlyTools?.includes("issue_read") ?? false);

  const viewTools = findButton("View tools");
  if (!viewTools) throw new Error("missing View tools button");
  await act(async () => {
    viewTools.click();
    await flush();
  });

  await waitFor("trusted badge", () => Boolean(document.querySelector(".cap-tool-trust")?.textContent?.includes("Trusted")));
  const untrust = findButton("Untrust");
  if (!untrust) throw new Error("missing Untrust button");
  await act(async () => {
    untrust.click();
    await flush();
  });
  await waitFor("untrusted tool", () => !(servers[0]?.trustedReadOnlyTools?.includes("issue_read") ?? false));

  await waitFor("Pre-trust button", () => Boolean(findButton("Pre-trust")));
  const trust = findButton("Pre-trust");
  if (!trust) throw new Error("missing Pre-trust button");
  await act(async () => {
    trust.click();
    await flush();
  });

  await waitFor("trusted badge", () => Boolean(document.querySelector(".cap-tool-trust")?.textContent?.includes("Trusted")));
  ok(bulkTrustCalls === 1, "clicking Pre-trust read-only invokes the MCP bulk trust action once");
  ok(untrustCalls === 1, "clicking Untrust invokes the MCP untrust action once");
  ok(trustCalls === 1, "clicking Trust invokes the MCP trust action once");
  ok(servers[0]?.trustedReadOnlyTools?.includes("issue_read") ?? false, "trusted raw tool name is added to the server snapshot");

  await act(async () => {
    root.unmount();
  });
  dom.window.close();
}

console.log("capabilities panel plugin actions");

{
  const dom = installDom();
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  const meta: Meta = { label: "test", ready: true, eventChannel: "plugin-channel", cwd: "/tmp/WorkGround2-test", workspaceRoot: "/tmp/WorkGround2-test" };
  const tabs: TabMeta[] = [{
    id: "tab-plugin",
    scope: "project",
    workspaceRoot: "/tmp/WorkGround2-test",
    workspaceName: "WorkGround2-test",
    topicId: "topic-plugin",
    topicTitle: "Plugins",
    label: "Plugins",
    ready: true,
    running: false,
    mode: "normal",
    toolApprovalMode: "auto",
    active: true,
    cwd: "/tmp/WorkGround2-test",
  }];
  let planCalls = 0;
  let installCalls = 0;
  let pickArchiveCalls = 0;
  let pickFolderCalls = 0;
  const plannedSources: string[] = [];
  const installedSources: string[] = [];
  const removedPlugins: string[] = [];
  let plugins: PluginView[] = [{
    name: "superpowers",
    version: "0.1.0",
    description: "Shared agent skills and hooks.",
    source: "git:github.com/obra/superpowers",
    root: "~/.WorkGround2/plugins/superpowers",
    manifestKind: "WorkGround2",
    enabled: true,
    skills: 2,
    hooks: 1,
    mcpServers: 0,
    updateAvailable: true,
    remoteVersion: "0.1.2",
  }, {
    name: "skill-share",
    version: "0.1.0",
    description: "Skill Share metadata package.",
    source: "/tmp/skill-share",
    root: "~/.WorkGround2/plugins/skill-share",
    manifestKind: "WorkGround2",
    enabled: true,
    skills: 0,
    hooks: 0,
    mcpServers: 0,
    addon: {
      kind: "skill-share",
      displayName: "Skill Share Package",
      capabilities: ["settings", "skills"],
      runtime: { type: "builtin" },
      panels: [{ id: "sources", title: "Sources", entry: "panels/sources.schema.json" }],
      secrets: [{ id: "git-credential", label: "Git credential", required: false }],
      configSchema: "panels/sources.schema.json",
      storageNamespace: "skill-share",
    },
  }, {
    name: "draw-tool",
    version: "0.1.0",
    description: "External draw runtime package.",
    source: "/tmp/draw-tool",
    root: "~/.WorkGround2/plugins/draw-tool",
    manifestKind: "WorkGround2",
    enabled: true,
    skills: 0,
    hooks: 0,
    mcpServers: 1,
    addon: {
      kind: "draw-tool",
      displayName: "Draw Tool Package",
      capabilities: ["tools", "image-generation"],
      runtime: { type: "mcp", mcpServer: "draw-addon" },
      panels: [{ id: "providers", title: "Providers", entry: "panels/providers.schema.json" }],
      secrets: [{ id: "provider-api-key", label: "Provider API key", required: false }],
      configSchema: "panels/providers.schema.json",
      storageNamespace: "draw-tool",
    },
  }, {
    name: "jira-connector",
    version: "0.2.0",
    description: "External Jira connector package.",
    source: "/tmp/jira-connector",
    root: "~/.WorkGround2/plugins/jira-connector",
    manifestKind: "WorkGround2",
    enabled: true,
    skills: 0,
    hooks: 0,
    mcpServers: 1,
    addon: {
      kind: "jira-connector",
      displayName: "Jira Connector",
      capabilities: ["settings", "tools"],
      runtime: { type: "mcp", mcpServer: "jira-connector" },
      panels: [{ id: "issues", title: "Issues", entry: "panels/issues.schema.json" }],
      secrets: [{ id: "jira-token", label: "Jira token", required: true }],
      configSchema: "panels/issues.schema.json",
      storageNamespace: "jira-connector",
    },
  }];
  window.go = {
    main: {
      App: {
        Meta: async () => meta,
        ListTabs: async () => tabs,
        Plugins: async () => plugins.map((plugin) => ({ ...plugin, warnings: [...(plugin.warnings ?? [])] })),
        AddOnPanelSchema: async (name: string, panelID: string) => {
          const plugin = plugins.find((item) => item.name === name);
          if (!plugin?.addon) throw new Error(`missing addon ${name}`);
          const namespace = plugin.addon.storageNamespace || name;
          if (panelID === "sources") {
            return JSON.stringify({
              version: 1,
              title: "Sources",
              storage: { namespace, source: `${namespace}/profiles.json` },
              sections: [{
                id: "sources",
                adapter: `${namespace}/profiles.json`,
                form: {
                  actions: [{ id: "save", labelKey: "caps.skillShareSave", variant: "primary" }],
                  fields: [
                    { key: "id", labelKey: "caps.skillShareId" },
                    { key: "gitUrl", labelKey: "caps.skillShareGitUrl", span: 2 },
                    { key: "enabled", labelKey: "caps.skillShareEnabled", type: "checkbox" },
                  ],
                },
                list: {
                  titleKey: "caps.skillShareProfiles",
                  emptyKey: "caps.skillShareNoProfiles",
                  summaryKey: "caps.skillShareProfileSummary",
                  titleField: "displayName",
                  detailFields: [{ path: "gitUrl", labelKey: "caps.skillShareGitUrl", span: 2 }],
                  actions: [{ id: "edit", labelKey: "caps.skillShareEdit" }],
                },
              }],
            });
          }
          if (panelID === "providers") {
            return JSON.stringify({
              version: 1,
              title: "Providers",
              storage: { namespace, source: `${namespace}/config.json` },
              sections: [{
                id: "providers",
                adapter: `${namespace}/config.json`,
                form: {
                  actions: [
                    { id: "generate", labelKey: "caps.drawAddonGenerate" },
                    { id: "save", labelKey: "caps.drawAddonSave", variant: "primary" },
                  ],
                  fields: [
                    { key: "id", labelKey: "caps.drawAddonId" },
                    { key: "mode", labelKey: "caps.drawAddonMode", type: "select", options: [{ value: "api", labelKey: "caps.drawAddonModeApi" }, { value: "cli", labelKey: "caps.drawAddonModeCli" }] },
                    { key: "baseUrl", labelKey: "caps.drawAddonBaseUrl", span: 2, visibleWhen: { key: "mode", equals: "api" } },
                    { key: "prompt", labelKey: "caps.drawAddonPrompt", type: "textarea", span: 2 },
                  ],
                },
                list: {
                  titleKey: "caps.drawAddonProviders",
                  emptyKey: "caps.drawAddonNoProviders",
                  summaryKey: "caps.drawAddonProviderSummary",
                  titleField: "displayName",
                  detailFields: [{ path: "baseUrl", labelKey: "caps.drawAddonBaseUrl", span: 2 }],
                },
              }],
            });
          }
          return JSON.stringify({ version: 1, title: panelID, sections: [] });
        },
        SkillShareProfiles: async () => [{
          id: "team",
          displayName: "Team skills",
          enabled: true,
          gitUrl: "https://example.test/team-skills.git",
          branch: "main",
          path: ".",
          authStatus: "anonymous",
          update: { auto: true, checkOnLogin: true, intervalSeconds: 3600 },
          state: { status: "ready" },
        }],
        SaveSkillShareProfile: async () => ({ id: "team", enabled: true, gitUrl: "", authStatus: "anonymous", state: { status: "ready" } }),
        SyncSkillShareProfile: async () => ({ taskId: "task", profileId: "team", phase: "ready", status: "succeeded", startedAt: "", finishedAt: "" }),
        DeleteSkillShareProfile: async () => ({ id: "team", enabled: false, gitUrl: "", authStatus: "anonymous", state: { status: "disabled" } }),
        RecoverSkillShareProfiles: async () => [],
        FlowSkillShareProfiles: async () => [],
        SaveFlowSkillShareProfile: async () => ({ id: "team", enabled: true, gitUrl: "", authStatus: "anonymous", state: { status: "ready" } }),
        SyncFlowSkillShareProfile: async () => ({ taskId: "task", profileId: "team", phase: "ready", status: "succeeded", startedAt: "", finishedAt: "" }),
        DeleteFlowSkillShareProfile: async () => ({ id: "team", enabled: false, gitUrl: "", authStatus: "anonymous", state: { status: "disabled" } }),
        RecoverFlowSkillShareProfiles: async () => [],
        DrawAddonProviders: async () => [{
          id: "openai-images",
          displayName: "OpenAI Images",
          enabled: true,
          mode: "api",
          baseUrl: "https://api.example.test",
          model: "image-model",
          authStatus: "set",
          state: { status: "ready" },
        }],
        PlanPluginInstall: async (source: string, options: PluginInstallOptions) => {
          planCalls += 1;
          plannedSources.push(source);
          ok(options.dryRun === true, "plugin preview asks for dry-run planning");
          return JSON.stringify({
            ok: true,
            status: "planned",
            name: "superpowers",
            actions: [{ kind: "plugin", action: "install_plugin_package", name: "superpowers", source, status: "planned" }],
          });
        },
        InstallPlugin: async (source: string, _options: PluginInstallOptions) => {
          installCalls += 1;
          installedSources.push(source);
          const next: PluginView = {
            name: "superpowers",
            version: "0.1.1",
            description: "Shared agent skills and hooks.",
            source,
            root: "~/.WorkGround2/plugins/superpowers",
            manifestKind: "WorkGround2",
            enabled: true,
            skills: 3,
            hooks: 1,
            mcpServers: 1,
            updateAvailable: true,
            remoteVersion: "0.1.2",
          };
          plugins = plugins.filter((plugin) => plugin.name !== next.name).concat(next);
          return JSON.stringify({ ok: true, status: "done", actions: [{ action: "install_plugin_package", name: next.name, status: "done" }] });
        },
        PickPluginFolder: async () => {
          pickFolderCalls += 1;
          return "/tmp/superpowers-plugin";
        },
        PickPluginArchive: async () => {
          pickArchiveCalls += 1;
          return "/tmp/superpowers-plugin.zip";
        },
        RemovePlugin: async (name: string) => {
          removedPlugins.push(name);
          plugins = plugins.filter((plugin) => plugin.name !== name);
        },
        AddOnPanelQuery: async () => ({ records: [], form: {} }),
        AddOnPanelAction: async () => ({}),
      } as Partial<AppBindings> as AppBindings,
    },
  };

  await act(async () => {
    root.render(React.createElement(LocaleProvider, null, React.createElement(PluginsSettingsPage)));
    await flush();
  });
  await waitFor("skill share AddOn package block", () => Boolean(document.querySelector('[data-plugin-settings-block="addon:skill-share"]')));
  ok(!Boolean(document.querySelector('[data-plugin-settings-block="skill-share"]')), "Skill Share is not rendered as a default built-in settings block");
  ok(!Boolean(document.querySelector('[data-plugin-settings-block="draw-addon"]')), "Draw AddOn is not rendered as a default built-in settings block");
  const skillBlock = document.querySelector<HTMLElement>('[data-plugin-settings-block="addon:skill-share"]');
  if (!skillBlock) throw new Error("missing Skill Share AddOn package block");
  const skillBlockToggle = skillBlock.querySelector<HTMLButtonElement>(".cap-plugin-settings-block__head");
  if (!skillBlockToggle) throw new Error("missing Skill Share block toggle");
  ok(skillBlock.textContent?.includes("Skill Share Package"), "Skill Share AddOn block shows manifest display name");
  ok(skillBlock.textContent?.includes("0.1.0"), "Skill Share AddOn block has package version status");
  ok(skillBlock.textContent?.includes("OK"), "Skill Share AddOn block has error status");
  ok(skillBlock.textContent?.includes("No update"), "Skill Share AddOn block has update status");
  ok(!Boolean(findButton("Uninstall AddOn", skillBlock)), "Skill Share AddOn details start collapsed");
  await act(async () => {
    skillBlockToggle.click();
    await flush();
  });
  ok(!skillBlock.textContent?.includes("Runtime"), "Skill Share AddOn expanded details hide default runtime metadata");
  ok(!skillBlock.textContent?.includes("Install path"), "Skill Share AddOn expanded details hide default install path metadata");
  ok(Boolean(skillBlock.querySelector('input[aria-label="source id"]')), "Skill Share AddOn renders schema-driven source id input");
  ok(Boolean(skillBlock.querySelector('input[aria-label="Git URL"]')), "Skill Share AddOn renders schema-driven Git URL input");
  ok(skillBlock.textContent?.includes("Team skills"), "Skill Share AddOn renders records through the declared adapter");
  const skillUninstall = findButton("Uninstall AddOn", skillBlock);
  if (!skillUninstall) throw new Error("missing Skill Share AddOn uninstall button");
  await act(async () => {
    skillUninstall.click();
    await flush();
  });
  const skillConfirm = findButton("Confirm uninstall AddOn", skillBlock);
  if (!skillConfirm) throw new Error("missing Skill Share AddOn uninstall confirmation");
  await act(async () => {
    skillConfirm.click();
    await flush();
  });
  await waitFor("Skill Share package removed", () => removedPlugins.includes("skill-share"));
  await waitFor("Skill Share AddOn block removed", () => !document.querySelector('[data-plugin-settings-block="addon:skill-share"]'));
  const drawBlock = document.querySelector<HTMLElement>('[data-plugin-settings-block="addon:draw-tool"]');
  if (!drawBlock) throw new Error("missing Draw AddOn package block");
  const drawBlockToggle = drawBlock.querySelector<HTMLButtonElement>(".cap-plugin-settings-block__head");
  if (!drawBlockToggle) throw new Error("missing Draw AddOn block toggle");
  ok(drawBlock.textContent?.includes("Draw Tool Package"), "Draw AddOn package block shows manifest display name");
  ok(!Boolean(findButton("Uninstall AddOn", drawBlock)), "Draw AddOn details start collapsed");
  await act(async () => {
    drawBlockToggle.click();
    await flush();
  });
  ok(!drawBlock.textContent?.includes("Runtime"), "Draw AddOn expanded details hide default runtime metadata");
  ok(!drawBlock.textContent?.includes("Install path"), "Draw AddOn expanded details hide default install path metadata");
  ok(Boolean(drawBlock.querySelector('input[aria-label="provider id"]')), "Draw AddOn renders schema-driven provider id input");
  ok(Boolean(drawBlock.querySelector('select[aria-label="mode"]')), "Draw AddOn renders schema-driven mode selector");
  ok(drawBlock.textContent?.includes("OpenAI Images"), "Draw AddOn renders provider records through the declared adapter");
  const drawUninstall = findButton("Uninstall AddOn", drawBlock);
  if (!drawUninstall) throw new Error("missing Draw AddOn uninstall button");
  await act(async () => {
    drawUninstall.click();
    await flush();
  });
  const drawConfirm = findButton("Confirm uninstall AddOn", drawBlock);
  if (!drawConfirm) throw new Error("missing Draw AddOn uninstall confirmation");
  await act(async () => {
    drawConfirm.click();
    await flush();
  });
  await waitFor("Draw AddOn package removed", () => removedPlugins.includes("draw-tool"));
  await waitFor("Draw AddOn block removed", () => !document.querySelector('[data-plugin-settings-block="addon:draw-tool"]'));
  const addonBlock = document.querySelector<HTMLElement>('[data-plugin-settings-block="addon:jira-connector"]');
  if (!addonBlock) throw new Error("missing external AddOn package block");
  ok(addonBlock.textContent?.includes("Jira Connector"), "external AddOn package block shows manifest display name");
  ok(addonBlock.textContent?.includes("0.2.0"), "external AddOn package block shows package version");
  const addonBlockToggle = addonBlock.querySelector<HTMLButtonElement>(".cap-plugin-settings-block__head");
  if (!addonBlockToggle) throw new Error("missing external AddOn block toggle");
  await act(async () => {
    addonBlockToggle.click();
    await flush();
  });
  ok(!addonBlock.textContent?.includes("Runtime"), "external AddOn expanded details hide default runtime metadata");
  ok(!addonBlock.textContent?.includes("Install path"), "external AddOn expanded details hide default install path metadata");
  ok(Boolean(findButton("Uninstall AddOn", addonBlock)), "external AddOn package block exposes uninstall action");
  ok(Boolean(document.querySelector(".cap-plugin-form-grid .cap-plugin-fields--local")), "local plugin install mode uses the shared form grid");
  const localOptionTexts = Array.from(document.querySelectorAll(".cap-plugin-installer__options > .cap-plugin-option-block"))
    .map((option) => option.textContent ?? "");
  ok(localOptionTexts[0]?.includes("Overwrite same-name plugin"), "local install mode shows overwrite before link mode");
  ok(localOptionTexts[1]?.includes("Developer mode: link selected folder"), "local install mode shows folder-only link mode after overwrite");

  const chooseArchive = findButton("Choose plugin zip");
  if (!chooseArchive) throw new Error("missing plugin zip picker button");
  await act(async () => {
    chooseArchive.click();
    await flush();
  });
  await waitFor("picked plugin zip source", () => document.body.textContent?.includes("/tmp/superpowers-plugin.zip") ?? false);
  ok(pickArchiveCalls === 1, "clicking Choose plugin zip invokes the plugin archive picker once");

  const chooseFolder = findButton("Choose plugin folder");
  if (!chooseFolder) throw new Error("missing plugin folder picker button");
  await act(async () => {
    chooseFolder.click();
    await flush();
  });
  await waitFor("picked plugin folder source", () => document.body.textContent?.includes("/tmp/superpowers-plugin") ?? false);
  ok(pickFolderCalls === 1, "clicking Choose folder invokes the plugin folder picker once");

  const gitMode = findButton("Git repository");
  if (!gitMode) throw new Error("missing Git repository install mode");
  await act(async () => {
    gitMode.click();
    await flush();
  });
  ok(Boolean(document.querySelector(".cap-plugin-form-grid .cap-plugin-fields--git")), "Git plugin install mode uses the shared form grid");
  const sourceInput = document.querySelector<HTMLInputElement>('input[aria-label="Git repository URL"]');
  if (!sourceInput) throw new Error("missing plugin git source input");
  await act(async () => {
    setInputValue(sourceInput, "git:github.com/obra/superpowers");
    await flush();
  });
  await waitFor("plugin preview enabled", () => findButton("Preview")?.disabled === false);

  const preview = findButton("Preview");
  if (!preview) throw new Error("missing plugin preview button");
  await act(async () => {
    preview.click();
    await flush();
  });
  await waitFor("plugin install plan", () => document.body.textContent?.includes("install_plugin_package") ?? false);
  ok(planCalls === 1, "clicking Preview invokes plugin install planning once");
  ok(plannedSources[0] === "git:github.com/obra/superpowers", "plugin preview receives the entered Git source");

  const install = findButton("Install plugin");
  if (!install) throw new Error("missing plugin install button");
  await act(async () => {
    install.click();
    await flush();
  });
  await waitFor("plugin install result", () => installCalls === 1 && plugins.find((plugin) => plugin.name === "superpowers")?.version === "0.1.1");
  ok(installedSources[0] === "git:github.com/obra/superpowers", "plugin install receives the entered Git source");

  await act(async () => {
    root.unmount();
  });
  dom.window.close();
}
