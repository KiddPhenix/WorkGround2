export interface WidgetModeState { active: boolean; revision: number; }

export interface WidgetModeBackend {
  EnterWidgetMode(): Promise<unknown>;
  ExitWidgetMode(tabID: string): Promise<void>;
  GetWidgetModeState(): Promise<WidgetModeState>;
}

export interface WidgetModeCoordinator {
  current(): boolean;
  sync(state: WidgetModeState): void;
  refresh(): Promise<void>;
  enter(): Promise<void>;
  exit(tabID?: string): Promise<void>;
  toggle(): Promise<void>;
}

export function createWidgetModeCoordinator(
  backend: WidgetModeBackend,
  publish: (state: WidgetModeState) => void,
  onMainWindowOpen: () => void = () => {},
): WidgetModeCoordinator {
  let state: WidgetModeState = { active: false, revision: -1 };
  let desired = false;
  let exitTabID = "";
  let pending: Promise<void> | null = null;
  let acknowledge: (() => void) | null = null;
  let requests = 0;
  let runningRequest = 0;

  const sync = (next: WidgetModeState) => {
    if (next.revision <= state.revision) return;
    const openedMainWindow = state.active && !next.active;
    state = next;
    // Native actions supersede completed intents, but preserve a newer click
    // queued while the current operation was running.
    if (!pending || requests === runningRequest) desired = next.active;
    if (openedMainWindow) onMainWindowOpen();
    publish(next);
    acknowledge?.();
  };
  const refresh = async () => { sync(await backend.GetWidgetModeState()); };

  const drain = (): Promise<void> => {
    if (pending) return pending;
    // Assign pending before work starts, including already-satisfied intents.
    pending = Promise.resolve().then(async () => {
      try {
        while (state.active !== desired) {
          const target = desired;
          const revision = state.revision;
          runningRequest = requests;
          const native = new Promise<void>((resolve) => {
            acknowledge = () => { if (state.revision > revision) resolve(); };
          });
          try {
            const binding = target ? backend.EnterWidgetMode() : backend.ExitWidgetMode(exitTabID);
            // Boolean events and void/late binding replies are invalidations,
            // never instructions to display an assumed mode.
            await Promise.race([binding.then(refresh), native]);
            if (state.revision <= revision && state.active !== target) {
              throw new Error("窗口切换未获确认，请重试");
            }
          } finally {
            acknowledge = null;
          }
        }
      } catch (cause) {
        desired = state.active;
        throw cause;
      }
    }).finally(() => {
      pending = null;
      // A click may land between the loop's last check and this microtask.
      if (state.active !== desired) return drain();
    });
    return pending;
  };

  const request = (target: boolean, tabID = "") => {
    requests++;
    desired = target;
    if (!target) exitTabID = tabID;
    return drain();
  };

  return {
    current: () => state.active,
    sync,
    refresh,
    enter: () => request(true),
    exit: (tabID = "") => request(false, tabID),
    toggle: () => request(!desired),
  };
}
