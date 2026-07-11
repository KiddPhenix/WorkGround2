import { create } from "zustand";
import { applySetState } from "./setState";
import type { Dispatch, SetStateAction } from "react";

export type AddOnDialogState = {
  pluginName: string | null;
  panelID: string | null;
  message: string;
  busy: boolean;
  setOpen: Dispatch<SetStateAction<{ pluginName: string; panelID: string; message: string } | null>>;
  setBusy: Dispatch<SetStateAction<boolean>>;
};

export const useAddOnDialogStore = create<AddOnDialogState>((set) => ({
  pluginName: null,
  panelID: null,
  message: "",
  busy: false,
  setOpen: (update) =>
    set((s) => {
      const val = applySetState(
        s.pluginName ? { pluginName: s.pluginName, panelID: s.panelID ?? "", message: s.message } : null,
        update,
      );
      return val
        ? { pluginName: val.pluginName, panelID: val.panelID, message: val.message }
        : { pluginName: null, panelID: null, message: "" };
    }),
  setBusy: (update) => set((s) => ({ busy: applySetState(s.busy, update) })),
}));