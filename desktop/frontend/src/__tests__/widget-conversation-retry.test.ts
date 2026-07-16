import assert from "node:assert/strict";
import type { WidgetConversationInput, WidgetConversationResult } from "../lib/bridge";
import { startWidgetConversationWithRetry } from "../components/widget/startWidgetConversation";

const input: WidgetConversationInput = { prompt: "fix it", requestId: "req-1", workspace: "global" };
const accepted: WidgetConversationResult = { status: "accepted", snapshot: {} as WidgetConversationResult["snapshot"] };

{
  const calls: WidgetConversationInput[] = [];
  const delays: number[] = [];
  await assert.rejects(
    startWidgetConversationWithRetry(
      async (got) => {
        calls.push(got);
        throw new Error("transport timeout");
      },
      input,
      async (delay) => { delays.push(delay); },
    ),
    /transport timeout/,
  );
  assert.equal(calls.length, 6, "initial call plus five transport retries");
  assert.ok(calls.every((got) => got === input), "every retry reuses the exact input object");
  assert.deepEqual(delays, [200, 400, 800, 1600, 3200]);
}

{
  let calls = 0;
  const result = await startWidgetConversationWithRetry(async () => {
    calls += 1;
    if (calls < 3) throw new Error("temporary transport failure");
    return accepted;
  }, input, async () => {});
  assert.equal(result, accepted);
  assert.equal(calls, 3, "stop retrying immediately after success");
}

console.log("widget conversation transport retry tests passed");
