export interface WidgetModeBackend {
  EnterWidgetMode(): Promise<unknown>;
  ExitWidgetMode(tabID: string): Promise<void>;
}

export interface WidgetModeCoordinator {
  current(): boolean;
  sync(active: boolean): void;
  enter(): Promise<void>;
  exit(tabID?: string): Promise<void>;
  toggle(): Promise<void>;
}

export function createWidgetModeCoordinator(
  backend: WidgetModeBackend,
  publish: (active: boolean) => void,
  onMainWindowOpen: () => void = () => {},
): WidgetModeCoordinator {
  let active = false;
  let desired = false;
  let exitTabID = "";
  let pending: Promise<void> | null = null;

  const publishState = (next: boolean) => {
    const openedMainWindow = active && !next;
    active = next;
    if (openedMainWindow) onMainWindowOpen();
    publish(next);
  };

  const sync = (next: boolean) => {
    if (!pending) desired = next;
    publishState(next);
  };

  const drain = () => {
    if (pending) return pending;
    pending = (async () => {
      try {
        while (active !== desired) {
          const target = desired;
          if (target) await backend.EnterWidgetMode();
          else await backend.ExitWidgetMode(exitTabID);
          publishState(target);
        }
      } catch (cause) {
        desired = active;
        throw cause;
      }
    })().finally(() => {
      pending = null;
    });
    return pending;
  };

  const request = (target: boolean, tabID = "") => {
    desired = target;
    if (!target) exitTabID = tabID;
    return drain();
  };

  return {
    current: () => active,
    sync,
    enter: () => request(true),
    exit: (tabID = "") => request(false, tabID),
    toggle: () => request(!desired),
  };
}
