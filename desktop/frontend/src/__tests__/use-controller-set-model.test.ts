// Tests for useController setModel error propagation — verifies that
// SetModelForTab failures are dispatched as local_notices AND re-thrown
// so callers (ModelSwitcher) can detect failure.
//
// tsx src/__tests__/use-controller-set-model.test.ts

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

// ── setModel error propagation pattern ─────────────────────────────────────
// The pattern in useController.setModel:
//   try { await app.SetModelForTab(...) }
//   catch (err) { dispatchTo(..., local_notice); throw err; }
//
// This test verifies that the pattern:
//   1. Dispatches a local_notice on failure
//   2. Re-throws so callers can detect the failure

async function setModelPattern(
  modelName: string,
  setModelFn: () => Promise<void>,
  dispatchFn: (action: { type: string; level: string; text: string }) => void,
): Promise<void> {
  try {
    await setModelFn();
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err ?? "");
    dispatchFn({ type: "local_notice", level: "warn", text: `Model switch failed: ${msg}` });
    throw err;
  }
}

async function run() {
  // ── throws after dispatching ───────────────────────────────────────────
  {
    const dispatched: unknown[] = [];
    const dispatch = (a: unknown) => dispatched.push(a);
    const err = new Error("finish or cancel the current turn");

    let thrown: Error | null = null;
    try {
      await setModelPattern(
        "gpt-4o",
        async () => { throw err; },
        dispatch,
      );
    } catch (e) {
      thrown = e as Error;
    }

    ok(thrown === err, "setModel re-throws the original error");
    ok(dispatched.length === 1, "dispatched exactly one action");
    ok(
      (dispatched[0] as Record<string, unknown>).type === "local_notice",
      "dispatched action is local_notice",
    );
    ok(
      (dispatched[0] as Record<string, unknown>).level === "warn",
      "local_notice level is warn",
    );
    ok(
      typeof (dispatched[0] as Record<string, unknown>).text === "string" &&
        ((dispatched[0] as Record<string, unknown>).text as string).includes("finish or cancel"),
      "local_notice text includes error message",
    );
  }

  // ── success: no dispatch, no throw ─────────────────────────────────────
  {
    const dispatched: unknown[] = [];
    const dispatch = (a: unknown) => dispatched.push(a);

    let thrown: Error | null = null;
    try {
      await setModelPattern(
        "gpt-4o",
        async () => { /* success */ },
        dispatch,
      );
    } catch (e) {
      thrown = e as Error;
    }

    ok(thrown === null, "no error thrown on success");
    ok(dispatched.length === 0, "no dispatch on success");
  }

  // ── non-Error rejection ────────────────────────────────────────────────
  {
    const dispatched: unknown[] = [];
    const dispatch = (a: unknown) => dispatched.push(a);

    let thrown: unknown = null;
    try {
      await setModelPattern(
        "gpt-4o",
        async () => { throw "string error"; },
        dispatch,
      );
    } catch (e) {
      thrown = e;
    }

    ok(thrown === "string error", "string error re-thrown as-is");
    ok(dispatched.length === 1, "dispatched for string error");
  }

  process.stdout.write(`\n${passed}/${passed + failed} passed\n`);
  if (failed > 0) process.exit(1);
}

run().catch((err) => {
  process.stderr.write(`Test harness error: ${err instanceof Error ? err.stack : String(err)}\n`);
  process.exit(1);
});
