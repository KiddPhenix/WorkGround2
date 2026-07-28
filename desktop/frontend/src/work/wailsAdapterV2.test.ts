/**
 * wailsAdapter V2 integration tests — real adapter calls through mock Wails App.
 *
 * Covers all 12 V2 methods through createWailsWorkControllerPort:
 *   - lowerCamel success
 *   - PascalCase-only → contract_malformed
 *   - Every required scalar: missing → contract_malformed
 *   - Every required scalar: wrong-type (string/NaN instead of boolean/number) → contract_malformed
 *   - committed=true with missing payload → contract_malformed
 *   - SelectWorkInputFile conditional: canceled=false artifactRef validation
 *   - binding Promise reject → transport_error (recoverable)
 *   - idempotency: network-fail → retry same requestID → duplicate+committed=true replay
 *   - committed-recovery (transportError.code='committed_recovery')
 *
 * Run: npx tsx src/work/wailsAdapterV2.test.ts
 */

// ── test harness ───────────────────────────────────────────────────────────

let passed = 0;
let failed = 0;
const failures: string[] = [];

function pass(label: string): void {
  process.stdout.write(`  PASS  ${label}\n`);
  passed++;
}

function fail(label: string, detail: string): void {
  process.stdout.write(`  FAIL  ${label}: ${detail}\n`);
  failed++;
  failures.push(`${label}: ${detail}`);
}

function checkEq<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) {
    fail(label, `got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  } else pass(label);
}

function checkOk(condition: boolean, label: string): void {
  if (!condition) fail(label, 'assertion failed');
  else pass(label);
}

// ── mock Wails App ─────────────────────────────────────────────────────────

type MockBinding = (...args: unknown[]) => Promise<unknown>;

class MockWailsApp {
  private bindings = new Map<string, MockBinding>();
  set(method: string, fn: MockBinding): void { this.bindings.set(method, fn); }
  getApp(): Record<string, MockBinding> {
    const self = this;
    return new Proxy({} as Record<string, MockBinding>, {
      get(_target, prop: string) {
        const fn = self.bindings.get(prop);
        if (!fn) throw new Error(`MockWailsApp: no binding for ${prop}`);
        return fn;
      },
    });
  }
}

function setupWailsGlobal(mockApp: MockWailsApp): void {
  (globalThis as Record<string, unknown>).window = {
    go: { main: { App: mockApp.getApp() } },
    runtime: { EventsOn: () => {}, EventsOff: () => {} },
  };
}

// ── fixtures ───────────────────────────────────────────────────────────────

const viewWithWork = (workId: string) => ({
  work: { id: workId, schemaVersion: 2, title: 'Test Work' },
  revision: 5,
  assessment: { state: 'ready', blocking: false, degraded: false },
  artifacts: [], cornerstones: [], tasks: [],
});

const OK: Record<string, Record<string, unknown>> = {
  beginWorkPlanning:       { result: viewWithWork('w-bp'), revision: 5, duplicate: false, committed: true, recoverable: false },
  applyDefinition:         { view: viewWithWork('w-ad'), intent: { workId: 'w-ad', runId: 'r-1', definitionRev: 1, reason: 'applied' }, impact: { keptNodeIds: [], invalidatedNodeIds: [], newNodeIds: ['n1'], removedNodeIds: [], requiresRerun: false }, revision: 6, duplicate: false, committed: true, recoverable: false },
  createCandidateRevision: { candidate: { workId: 'w-cc', revision: 2, parentRevision: 1, status: 'draft', goal: 'test', nodes: [], artifactSlots: [], inputSpecs: [], createdBy: 'ai', createdAt: new Date().toISOString(), digest: 'abc' }, impact: { keptNodeIds: [], invalidatedNodeIds: [], newNodeIds: [], removedNodeIds: [], requiresRerun: false }, revision: 7, duplicate: false, committed: true, recoverable: false },
  retryWorkNode:           { result: { id: 't1', title: 'Task 1', state: 'running', revision: 3 }, revision: 8, duplicate: false, committed: true, recoverable: false },
  retryArtifactSlot:       { slot: { id: 's1', workId: 'w-rs', definitionRev: 1, title: 'Slot 1', kind: 'file', expectedCount: 1, required: true, state: 'generating', revision: 4 }, revision: 9, duplicate: false, committed: true, recoverable: false },
  previewArtifact:         { preview: { artifactId: 'a1', workId: 'w-pa', grade: 'inline' as const, mimeType: 'text/plain', canOpen: true, canConvert: false }, committed: true, recoverable: false },
  requestArtifactConversion: { preview: { artifactId: 'a2', workId: 'w-rc', grade: 'filecard' as const, mimeType: 'application/pdf', canOpen: true, canConvert: false }, committed: true, recoverable: false, duplicate: false },
  selectWorkInputFile:     { artifactRef: { id: 'a3', name: 'report.pdf', type: 'application/pdf', status: 'available', path: '/tmp/f.txt', blobDigest: 'd1' }, canceled: false },
  submitWorkInput:         { input: { id: 'i1', workId: 'w-si', state: 'submitted', revision: 5 }, receipt: { operation: 'SubmitWorkInput', requestId: 'req-1', revision: 10 }, revision: 10, duplicate: false, committed: true, recoverable: false },
  setInputCornerstone:     { cornerstoneId: 'cs1', pinned: true, receipt: { operation: 'SetInputCornerstone', requestId: 'req-2', revision: 11 }, revision: 11, duplicate: false, committed: true, recoverable: false },
  previewWorkPatch:        { preview: { id: 'pp1', workId: 'w-pp', baseDefinitionRev: 1, baseBlockRev: 1, scope: 'block', operations: [], affectedNodeIds: [], invalidatedTaskIds: [], requiresRerun: false, digest: 'd2', expiresAt: new Date().toISOString() }, receipt: { operation: 'PreviewWorkPatch', requestId: 'req-3', revision: 12 }, revision: 12, duplicate: false, committed: true, recoverable: false },
  applyWorkPatch:          { workRevision: 13, newRevision: 14, invalidatedTaskIds: ['t1'], affectedBlockIds: ['b1'], affectedArtifactSlotIds: [], staleArtifactSlotIds: [], requiresRerun: true, duplicate: false, committed: true, recoverable: false, receipt: { operation: 'ApplyWorkPatch', requestId: 'req-4', revision: 14 } },
};

// ── Scalar validation table ────────────────────────────────────────────────
// For each method, a list of [field, wrongTypeValue] pairs that must be validated.
// Each pair generates: (a) field missing → contract_malformed, (b) field wrong-type → contract_malformed.

interface ScalarEntry { field: string; bad: unknown; }
const SCALARS: Record<string, ScalarEntry[]> = {
  beginWorkPlanning:       [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'revision',bad:'five'}],
  applyDefinition:         [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'revision',bad:'five'}],
  createCandidateRevision: [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'revision',bad:'five'}],
  retryWorkNode:           [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'revision',bad:'five'}],
  retryArtifactSlot:       [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'revision',bad:'five'}],
  previewArtifact:         [{field:'committed',bad:'true'},{field:'recoverable',bad:1}],
  requestArtifactConversion: [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'}],
  selectWorkInputFile:     [{field:'canceled',bad:'no'}],
  submitWorkInput:         [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'revision',bad:'five'}],
  setInputCornerstone:     [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'pinned',bad:1},{field:'revision',bad:'five'}],
  previewWorkPatch:        [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'revision',bad:'five'}],
  applyWorkPatch:          [{field:'committed',bad:'true'},{field:'recoverable',bad:1},{field:'duplicate',bad:'yes'},{field:'workRevision',bad:'x'},{field:'newRevision',bad:'x'},{field:'requiresRerun',bad:1}],
};

// ── PascalCase variants ────────────────────────────────────────────────────

const PASCAL: Record<string, Record<string, unknown>> = {
  beginWorkPlanning:       { Result: OK.beginWorkPlanning.result, Revision: 5, Duplicate: false, Committed: true, Recoverable: false },
  selectWorkInputFile:     { ArtifactRef: { id: 'a3', name: 'report.pdf', type: 'application/pdf', status: 'available' }, Canceled: false },
  createCandidateRevision: { Candidate: OK.createCandidateRevision.candidate, Revision: 7, Duplicate: false, Committed: true, Recoverable: false },
  submitWorkInput:         { Result: OK.submitWorkInput.input, Revision: 10, Duplicate: false, Committed: true, Recoverable: false, Receipt: OK.submitWorkInput.receipt },
  setInputCornerstone:     { CornerstoneID: 'cs-bad', Pinned: true, Revision: 11, Duplicate: false, Committed: true, Recoverable: false, Receipt: OK.setInputCornerstone.receipt },
  applyWorkPatch:          { WorkRevision: 13, NewRevision: 14, InvalidatedTaskIDs: ['t1'], AffectedBlockIDs: ['b1'], AffectedArtifactSlotIDs: [], StaleArtifactSlotIDs: [], RequiresRerun: true, Duplicate: false, Committed: true, Recoverable: false, Receipt: OK.applyWorkPatch.receipt },
};

// ── Map camelCase test keys to PascalCase Wails binding methods ────────────

function wailsMethod(key: string): string {
  const m: Record<string, string> = {
    beginWorkPlanning: 'BeginWorkPlanning', applyDefinition: 'ApplyDefinition',
    createCandidateRevision: 'CreateCandidateRevision', retryWorkNode: 'RetryWorkNode',
    retryArtifactSlot: 'RetryArtifactSlot', previewArtifact: 'PreviewArtifact',
    requestArtifactConversion: 'RequestArtifactConversion', selectWorkInputFile: 'SelectWorkInputFile',
    submitWorkInput: 'SubmitWorkInput', setInputCornerstone: 'SetInputCornerstone',
    previewWorkPatch: 'PreviewWorkPatch', applyWorkPatch: 'ApplyWorkPatch',
  };
  return m[key] ?? key;
}

type PortMethod = (input: any) => Promise<Record<string, unknown>>;

// ── Input factories ────────────────────────────────────────────────────────

function inputFor(method: string): Record<string, unknown> {
  switch (method) {
    case 'beginWorkPlanning':       return { sessionId: 's1', requestId: `req-${method}` };
    case 'applyDefinition':         return { workId: 'w-ad', revision: 1, expectedRevision: 1, requestId: `req-${method}` };
    case 'createCandidateRevision': return { workId: 'w-cc', intent: 'test', baseDefinitionRevision: 1, expectedRevision: 1, requestId: `req-${method}` };
    case 'retryWorkNode':           return { workId: 'w-rn', runId: 'r1', taskId: 't1', expectedRevision: 1, requestId: `req-${method}` };
    case 'retryArtifactSlot':       return { workId: 'w-rs', slotId: 's1', definitionRevision: 1, expectedRevision: 1, requestId: `req-${method}` };
    case 'previewArtifact':         return { workId: 'w-pa', definitionRevision: 1, slotId: 's1', slotRevision: 1, artifactId: 'a1', requestId: `req-${method}` };
    case 'requestArtifactConversion': return { workId: 'w-rc', definitionRevision: 1, slotId: 's1', slotRevision: 1, artifactId: 'a2', requestId: `req-${method}`, allowExternal: false, approvalToken: '' };
    case 'selectWorkInputFile':     return { workId: 'w-sf', runId: 'r1', taskId: 't1', blockId: 'b1', inputId: 'i1', specId: 's1' };
    case 'submitWorkInput':         return { workId: 'w-si', runId: 'r1', taskId: 't1', blockId: 'b1', inputId: 'i1', value: 'hello', definitionRevision: 1, inputRevision: 1, expectedRevision: 1, requestId: `req-${method}` };
    case 'setInputCornerstone':     return { workId: 'w-cp', inputId: 'i1', pin: true, definitionRevision: 1, inputRevision: 1, expectedRevision: 1, requestId: `req-${method}` };
    case 'previewWorkPatch':        return { workId: 'w-pp', runId: 'r1', taskId: 't1', blockId: 'b1', sessionId: 's1', instruction: 'fix', definitionRevision: 1, blockRevision: 1, scope: 'block', requestId: `req-${method}` };
    case 'applyWorkPatch':          return { workId: 'w-ap', patchId: 'pp1', previewDigest: 'd2', scope: 'block', expectedRevision: 1, requestId: `req-${method}` };
    default: throw new Error(`unknown: ${method}`);
  }
}

function makeMissing(ok: Record<string, unknown>, field: string): Record<string, unknown> {
  const v = { ...ok }; delete (v as any)[field]; return v;
}
function makeWrongType(ok: Record<string, unknown>, field: string, bad: unknown): Record<string, unknown> {
  return { ...ok, [field]: bad };
}
function getError(r: Record<string, unknown>): { code?: unknown; recoverable?: unknown } | undefined {
  return (r as any).transportError || (r as any).error;
}

// ── run ────────────────────────────────────────────────────────────────────

async function run(): Promise<void> {
  const mock = new MockWailsApp();
  const setAllOk = () => { for (const [m, v] of Object.entries(OK)) mock.set(wailsMethod(m), async () => v); };
  setAllOk();
  for (const m of ['WorkEnabled','WorkCapable','WorkCollaborationV2Enabled','ReadUIPreference']) mock.set(m, async () => true);
  for (const m of ['GetWork','WatchWork','UnwatchWork','WriteUIPreference']) mock.set(m, async () => m === 'GetWork' ? viewWithWork('w-1') : undefined);

  setupWailsGlobal(mock);
  const { createWailsWorkControllerPort } = (await import('./wailsAdapter'));
  const port = createWailsWorkControllerPort('test-tab');
  if (!port) { fail('factory', 'port undefined'); return; }
  const p = port as unknown as Record<string, PortMethod | undefined>;

  // ── 1. lowerCamel success (12 methods) ───────────────────────────────────
  for (const method of Object.keys(OK)) {
    const fn = p[method]; if (!fn) { fail(`${method} success`, 'undefined'); continue; }
    try {
      const r = await fn(inputFor(method));
      if (method === 'selectWorkInputFile') checkEq((r as any).canceled, false, `${method}: success`);
      else checkEq(r.committed, true, `${method}: success`);
    } catch (e) { fail(`${method}: success`, `threw`); }
  }

  // ── 2. Scalar missing + wrong-type (per field, per method) ───────────────
  for (const [method, entries] of Object.entries(SCALARS)) {
    const fn = p[method]; if (!fn) continue;
    const wm = wailsMethod(method);
    for (const {field, bad} of entries) {
      // missing
      const missingVal = makeMissing(OK[method], field);
      mock.set(wm, async () => missingVal);
      const rm = await fn(inputFor(method));
      const em = getError(rm);
      checkEq(em?.code, 'contract_malformed', `${method}: ${field} missing → contract_malformed`);

      // wrong-type
      const badVal = makeWrongType(OK[method], field, bad);
      mock.set(wm, async () => badVal);
      const rw = await fn(inputFor(method));
      const ew = getError(rw);
      checkEq(ew?.code, 'contract_malformed', `${method}: ${field} wrong-type → contract_malformed`);

      mock.set(wm, async () => OK[method]); // restore
    }
  }

  // ── 2b. NaN/Infinity for number fields (passes typeof but fails Number.isFinite) ─
  const NAN_METHODS = ['beginWorkPlanning','applyDefinition','createCandidateRevision','retryWorkNode','retryArtifactSlot','submitWorkInput','setInputCornerstone','previewWorkPatch'];
  for (const method of NAN_METHODS) {
    const fn = p[method]; if (!fn) continue;
    const wm = wailsMethod(method);
    const badVal = { ...OK[method], revision: NaN };
    mock.set(wm, async () => badVal);
    const r = await fn(inputFor(method));
    checkEq(getError(r)?.code, 'contract_malformed', `${method}: revision=NaN → contract_malformed`);
    mock.set(wm, async () => OK[method]);
  }
  // applyWorkPatch NaN on workRevision/newRevision
  { const m='applyWorkPatch'; const fn=p[m]; if(fn){ const wm=wailsMethod(m);
    mock.set(wm, async () => ({...OK[m], workRevision: NaN}));
    checkEq(getError(await fn(inputFor(m)))?.code, 'contract_malformed', `${m}: workRevision=NaN → contract_malformed`);
    mock.set(wm, async () => ({...OK[m], newRevision: NaN}));
    checkEq(getError(await fn(inputFor(m)))?.code, 'contract_malformed', `${m}: newRevision=NaN → contract_malformed`);
    mock.set(wm, async () => OK[m]);
  }}

  // ── 3. Payload missing (committed=true, no CR, but payload absent) ───────
  const PAYLOAD_METHODS = ['beginWorkPlanning','applyDefinition','createCandidateRevision','retryWorkNode','retryArtifactSlot','previewArtifact','requestArtifactConversion','submitWorkInput','previewWorkPatch'];
  for (const method of PAYLOAD_METHODS) {
    const fn = p[method]; if (!fn) continue;
    const wm = wailsMethod(method);
    // committed/recoverable/duplicate/revision all valid, but no payload
    const base = { committed: true, recoverable: false };
    if (OK[method].hasOwnProperty('revision')) (base as any).revision = 5;
    if (OK[method].hasOwnProperty('duplicate')) (base as any).duplicate = false;
    if (method === 'requestArtifactConversion') (base as any).duplicate = false;
    mock.set(wm, async () => base);
    const r = await fn(inputFor(method));
    const err = getError(r);
    checkEq(err?.code, 'contract_malformed', `${method}: payload missing → contract_malformed`);
    mock.set(wm, async () => OK[method]);
  }

  // ── 4. SelectWorkInputFile conditional artifactRef ───────────────────────
  {
    const method = 'selectWorkInputFile'; const fn = p[method]; if (!fn) { fail('sf cond', 'undefined'); return; }
    const wm = wailsMethod(method); const inp = inputFor(method);
    const okRef = { id:'a3', name:'report.pdf', type:'application/pdf', status:'available' };

    // field-by-field: missing → contract_malformed
    for (const field of ['id','name','type','status'] as const) {
      const bad: Record<string, unknown> = { ...okRef }; delete bad[field];
      mock.set(wm, async () => ({ canceled: false, artifactRef: bad }));
      const r = await fn(inp);
      checkEq((r as any).error?.code, 'contract_malformed', `sf no-ref-${field} → malformed`);
    }

    // field-by-field: empty string → contract_malformed
    for (const field of ['id','name','type','status'] as const) {
      mock.set(wm, async () => ({ canceled: false, artifactRef: { ...okRef, [field]: '' } }));
      const r = await fn(inp);
      checkEq((r as any).error?.code, 'contract_malformed', `sf empty-${field} → malformed`);
    }

    // field-by-field: wrong-type (number) → contract_malformed
    for (const field of ['id','name','type','status'] as const) {
      mock.set(wm, async () => ({ canceled: false, artifactRef: { ...okRef, [field]: 123 } }));
      const r = await fn(inp);
      checkEq((r as any).error?.code, 'contract_malformed', `sf num-${field} → malformed`);
    }

    // invalid status → contract_malformed
    mock.set(wm, async () => ({ canceled: false, artifactRef: { ...okRef, status: 'broken' } }));
    const rs = await fn(inp);
    checkEq((rs as any).error?.code, 'contract_malformed', 'sf invalid-status → malformed');

    // canceled=true, no artifactRef → success (canceled)
    mock.set(wm, async () => ({ canceled: true }));
    const r5 = await fn(inp);
    checkEq((r5 as any).canceled, true, 'sf canceled=true → success');
    checkOk(!(r5 as any).error, 'sf canceled=true → no error');

    // explicit error, no artifactRef → success (error present)
    mock.set(wm, async () => ({ canceled: false, error: { code: 'user_canceled', message: 'cancelled', committed: false, recoverable: false } }));
    const r6 = await fn(inp);
    checkEq((r6 as any).canceled, false, 'sf error no-ref → canceled=false');
    checkOk((r6 as any).error !== undefined, 'sf error no-ref → error present');
    checkOk((r6 as any).artifactRef === undefined || (r6 as any).artifactRef === null, 'sf error no-ref → no artifactRef');

    mock.set(wm, async () => OK[method]);
  }

  // ── 5. PascalCase-only (7 methods) ───────────────────────────────────────
  for (const [method, pascalVal] of Object.entries(PASCAL)) {
    const fn = p[method]; if (!fn) continue;
    const wm = wailsMethod(method);
    mock.set(wm, async () => pascalVal);
    const r = await fn(inputFor(method));
    const raw: any = r;
    if (method === 'selectWorkInputFile') checkEq(raw.error?.code, 'contract_malformed', `${method}: pascal`);
    else checkEq(getError(r)?.code, 'contract_malformed', `${method}: pascal`);
    mock.set(wm, async () => OK[method]);
  }

  // ── 6. binding throw → transport_error (12 methods) ──────────────────────
  for (const method of Object.keys(OK)) {
    const fn = p[method]; if (!fn) continue;
    const wm = wailsMethod(method);
    mock.set(wm, async () => { throw new Error('network down'); });
    const r = await fn(inputFor(method));
    const err = getError(r);
    checkEq(err?.code, 'transport_error', `${method}: throw code`);
    checkEq(err?.recoverable ?? (r as any).recoverable, true, `${method}: throw recoverable`);
    mock.set(wm, async () => OK[method]);
  }

  // ── 7. committed-recovery ────────────────────────────────────────────────
  { const m = 'beginWorkPlanning'; const fn = p[m]; if (!fn) { fail('cr','undefined'); return; }
    mock.set(wailsMethod(m), async () => ({ revision:5, duplicate:false, committed:true, recoverable:true,
      transportError:{code:'committed_recovery',message:'lost',operation:'BeginWorkPlanning',workId:'w-cr',requestId:'cr-req',committed:true,recoverable:true} }));
    const r = await fn({ sessionId:'s-cr', requestId:'cr-req' });
    checkEq(r.committed, true, 'cr committed');
    checkEq(r.recoverable, true, 'cr recoverable');
    checkEq((r as any).transportError?.code, 'committed_recovery', 'cr code');
    mock.set(wailsMethod(m), async () => OK.beginWorkPlanning);
  }

  // ── 8. replay idempotency ────────────────────────────────────────────────
  { let call = 0; let rid = '';
    mock.set('BeginWorkPlanning', async (...a: unknown[]) => { call++;
      const inp = (a[1] as Record<string,string>);
      if (call === 1) { rid = inp.requestId; throw new Error('offline'); }
      if (inp.requestId !== rid) return { revision:0, duplicate:false, committed:false, recoverable:false, transportError:{code:'mismatch',message:'',committed:false,recoverable:false} };
      return { result: viewWithWork('w-replay'), revision:5, duplicate:true, committed:true, recoverable:false };
    });
    const fn = p['beginWorkPlanning']; if (!fn) { fail('replay','undefined'); return; }
    const r1 = await fn({ sessionId:'s-rp', requestId:'rp-1' });
    checkEq(getError(r1)?.code, 'transport_error', 'replay first');
    const r2 = await fn({ sessionId:'s-rp', requestId:'rp-1' });
    checkEq(r2.duplicate, true, 'replay dup');
    checkEq(r2.committed, true, 'replay committed');
    checkOk((r2 as any).result?.work?.id === 'w-replay', 'replay id');
    mock.set('BeginWorkPlanning', async () => OK.beginWorkPlanning);
  }

  // ── 9. Go nil slices normalize to frontend arrays ───────────────────────
  {
    const method = 'previewWorkPatch'; const fn = p[method]; if (!fn) { fail('preview null arrays','undefined'); return; }
    const preview = {
      ...(OK.previewWorkPatch.preview as Record<string, unknown>),
      operations: null,
      affectedNodeIds: null,
      affectedBlockIds: null,
      affectedArtifactSlotIds: null,
      staleArtifactSlotIds: null,
      invalidatedTaskIds: null,
    };
    mock.set(wailsMethod(method), async () => ({ ...OK.previewWorkPatch, preview }));
    const result = await fn(inputFor(method));
    const normalized = (result as any).preview;
    for (const field of ['operations','affectedNodeIds','affectedBlockIds','affectedArtifactSlotIds','staleArtifactSlotIds','invalidatedTaskIds']) {
      checkOk(Array.isArray(normalized?.[field]), `preview null arrays: ${field}`);
      checkEq(normalized?.[field]?.length, 0, `preview null arrays: ${field} empty`);
    }
    mock.set(wailsMethod(method), async () => OK.previewWorkPatch);
  }

  // ── Summary ──────────────────────────────────────────────────────────────
  process.stdout.write(`\n${passed} passed, ${failed} failed, ${passed + failed} total\n`);
  if (failed > 0) { process.stdout.write('\nFailures:\n'); for (const f of failures) process.stdout.write(`  ${f}\n`); }
}

run().then(() => { if (failed > 0) process.exit(1); }).catch((err) => {
  process.stdout.write(`\nFATAL: ${err instanceof Error ? err.message : String(err)}\n${err instanceof Error ? err.stack : ''}\n`);
  process.exit(1);
});
