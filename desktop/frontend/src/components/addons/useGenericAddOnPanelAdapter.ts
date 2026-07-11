import { useCallback, useEffect, useRef, useState } from "react";
import { app } from "../../lib/bridge";
import type { AddOnPanelQueryResult } from "../../lib/types";
import type { AddOnPanelAdapter, AddOnPanelAdapterMap, AddOnPanelSchemaMap, AddOnRecord } from "./AddonPanelRenderer";
import { addonPanelKey } from "./AddonPanelRenderer";
import { asArray } from "../../lib/array";
import type { PluginView } from "../../lib/types";

export type PanelDataKey = string; // "pluginName:panelID:adapterKey"

function makePanelDataKey(pluginName: string, panelID: string, adapter: string): PanelDataKey {
  return `${pluginName}:${panelID}:${adapter}`;
}

/**
 * Returns all (panelID, adapterKey) pairs for a plugin's addon panels by
 * reading the panel schemas.
 */
function collectAdapterKeys(
  plugin: PluginView,
  schemas: AddOnPanelSchemaMap | null,
): Array<{ panelID: string; adapterKey: string }> {
  const pairs: Array<{ panelID: string; adapterKey: string }> = [];
  for (const panel of asArray(plugin.addon?.panels)) {
    const panelID = panel.id || panel.entry || "";
    if (!panelID) continue;
    const state = schemas?.[addonPanelKey(plugin.name, panelID)];
    const schema = state?.schema;
    const sections = asArray(schema?.sections);
    for (const section of sections) {
      const adapterKey = (section.adapter || section.dataSource || schema?.storage?.source || "") as string;
      if (adapterKey && !pairs.some((p) => p.adapterKey === adapterKey)) {
        pairs.push({ panelID, adapterKey });
      }
    }
  }
  return pairs;
}

export type PanelDataStore = Record<PanelDataKey, AddOnPanelQueryResult>;
export type PanelFormStore = Record<PanelDataKey, AddOnRecord>;

/**
 * Generic hook that loads AddOn panel data via AddOnPanelQuery for every
 * plugin/panel/adapter combination and exposes a ready-to-use AddOnPanelAdapterMap.
 *
 * Callers MUST wait for schemas to be loaded before passing them here.
 */
export function useGenericAddOnPanelAdapters(
  plugins: PluginView[] | null,
  schemas: AddOnPanelSchemaMap | null,
): { adapters: AddOnPanelAdapterMap; loading: boolean; reload: () => Promise<void> } {
  const [panelData, setPanelData] = useState<PanelDataStore>({});
  const [forms, setForms] = useState<PanelFormStore>({});
  const [loading, setLoading] = useState(false);
  const pendingRef = useRef(0);

  const loadAll = useCallback(async () => {
    if (!plugins || !schemas) return;
    const allQueries: Array<{ key: PanelDataKey; pluginName: string; panelID: string; adapterKey: string }> = [];
    for (const plugin of plugins) {
      if (!plugin.addon) continue;
      for (const { panelID, adapterKey } of collectAdapterKeys(plugin, schemas)) {
        allQueries.push({
          key: makePanelDataKey(plugin.name, panelID, adapterKey),
          pluginName: plugin.name,
          panelID,
          adapterKey,
        });
      }
    }
    if (allQueries.length === 0) return;

    const id = ++pendingRef.current;
    setLoading(true);
    const results = await Promise.allSettled(
      allQueries.map((q) => app.AddOnPanelQuery(q.pluginName, q.panelID, q.adapterKey)),
    );
    if (id !== pendingRef.current) return; // stale

    const data: PanelDataStore = {};
    const form: PanelFormStore = {};
    for (let i = 0; i < allQueries.length; i++) {
      const q = allQueries[i];
      const settled = results[i];
      if (settled.status === "fulfilled") {
        data[q.key] = settled.value;
        form[q.key] = settled.value.form || {};
      } else {
        data[q.key] = { records: [], form: {} };
        form[q.key] = {};
      }
    }
    setPanelData(data);
    setForms(form);
    setLoading(false);
  }, [plugins, schemas]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  // Build the adapter map
  const adapters: AddOnPanelAdapterMap = {};
  if (plugins && schemas) {
    for (const plugin of plugins) {
      if (!plugin.addon) continue;
      for (const { panelID, adapterKey } of collectAdapterKeys(plugin, schemas)) {
        const dataKey = makePanelDataKey(plugin.name, panelID, adapterKey);
        const data = panelData[dataKey];
        const form = forms[dataKey] || {};

        if (!adapters[adapterKey]) {
          // One adapter per adapter key (shared across plugins if they use the same key)
          adapters[adapterKey] = createAdapter(adapterKey, dataKey, plugin.name, panelID, data, form, setForms, loadAll);
        }
      }
    }
  }

  return { adapters, loading, reload: loadAll };
}

function createAdapter(
  adapterKey: string,
  dataKey: PanelDataKey,
  pluginName: string,
  panelID: string,
  data: AddOnPanelQueryResult | undefined,
  form: AddOnRecord,
  setForms: React.Dispatch<React.SetStateAction<PanelFormStore>>,
  reload: () => Promise<void>,
): AddOnPanelAdapter {
  return {
    records: data?.records ?? null,
    form,
    setFormValue: (key, value) => {
      setForms((prev) => ({
        ...prev,
        [dataKey]: { ...(prev[dataKey] || {}), [key]: value },
      }));
    },
    resetForm: () => {
      void reload();
    },
    runFormAction: async (actionId) => {
      await app.AddOnPanelAction(pluginName, panelID, adapterKey, { actionId, form });
      await reload();
    },
    canFormAction: () => true,
    runRecordAction: async (actionId, record) => {
      const recordId = String(record.id ?? record.displayName ?? "");
      await app.AddOnPanelAction(pluginName, panelID, adapterKey, {
        actionId,
        recordId,
        extra: actionId === "force" ? { force: true } : undefined,
      });
      await reload();
    },
    canRecordAction: () => true,
    recordKey: (record) => String(record.id || record.displayName || ""),
    recordStatus: (record) => {
      const state = (record.state as { status?: string; lastError?: string }) || {};
      const status = state.status || "unconfigured";
      return { dot: genericStatusDot(status), label: genericStatusLabel(status), sub: state.lastError || "" };
    },
    recordBadges: (record) => {
      const badges: string[] = [];
      if (record.id && record.id !== record.displayName) badges.push(String(record.id));
      if (record.version) badges.push(String(record.version));
      if (record.manifestKind) badges.push(String(record.manifestKind));
      return badges;
    },
  };
}

function genericStatusDot(status: string): string {
  switch (status) {
    case "ready": return "connected";
    case "syncing": case "running": return "pending";
    case "update_failed": case "failed": return "error";
    case "needs_auth": return "warning";
    case "unconfigured": case "disabled": default: return "disabled";
  }
}

function genericStatusLabel(status: string): string {
  switch (status) {
    case "ready": return "Ready";
    case "syncing": return "Syncing";
    case "running": return "Running";
    case "update_failed": return "Update failed";
    case "failed": return "Failed";
    case "needs_auth": return "Needs auth";
    case "unconfigured": return "Unconfigured";
    case "disabled": return "Disabled";
    default: return status;
  }
}
