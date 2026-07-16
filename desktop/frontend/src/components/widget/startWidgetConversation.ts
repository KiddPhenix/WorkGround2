import type { WidgetConversationInput, WidgetConversationResult } from "../../lib/bridge";

export const WIDGET_TRANSPORT_RETRIES = 5;
const RETRY_BASE_DELAY_MS = 200;
const RETRY_MAX_DELAY_MS = 5_000;

type StartConversation = (input: WidgetConversationInput) => Promise<WidgetConversationResult>;
type Wait = (delayMs: number) => Promise<void>;

const wait: Wait = (delayMs) => new Promise((resolve) => window.setTimeout(resolve, delayMs));

// Wails transport failures are retried here because they occur outside Go.
// Reusing the exact input preserves the backend requestId receipt guarantee.
export async function startWidgetConversationWithRetry(
  start: StartConversation,
  input: WidgetConversationInput,
  waitForRetry: Wait = wait,
): Promise<WidgetConversationResult> {
  for (let attempt = 0; ; attempt += 1) {
    try {
      return await start(input);
    } catch (cause) {
      if (attempt >= WIDGET_TRANSPORT_RETRIES) throw cause;
      const delay = Math.min(RETRY_BASE_DELAY_MS * (2 ** attempt), RETRY_MAX_DELAY_MS);
      await waitForRetry(delay);
    }
  }
}
