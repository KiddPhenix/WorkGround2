import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  parseArtifactSlot, parseWorkDefinitionRevision, parseWorkInput,
  parseApplyWorkPatchResult, parsePatchIntentReceipt, parseWorkPatchPreview, parseWorkViewV2,
} from '../work/parse.js';
import type {
  ApplyWorkPatchResult, ArtifactSlot, PatchIntentReceipt, WorkDefinitionRevision, WorkInput, WorkPatchPreview,
  RetryWorkNodeRequest, InputSubmissionResult,
  BeginWorkPlanningResult, ApplyDefinitionResult, SubmitInputResult, PreviewWorkPatchResult,
  DefRevisionAppliedPayload, InputSubmittedPayload, InputRejectedPayload,
  InputCornerstoneChangedPayload, PatchAppliedPayload,
  TaskRuntimeCreatedPayload, TaskRuntimeUpdatedPayload, TaskStaleResultPayload, V2TaskRuntime,
} from '../work/types_v2.js';
import type { Work } from '../work/types.js';

const fixtureDir = join(dirname(fileURLToPath(import.meta.url)), '../work/__fixtures__');
function readFixture(name: string): string { return readFileSync(join(fixtureDir, name), 'utf-8'); }
function readFixtureJSON(name: string): unknown { return JSON.parse(readFixture(name)); }
function cloneToRecord(v: unknown): Record<string, unknown> { return JSON.parse(JSON.stringify(v)); }

// ── Compile-time typed fixture (Work, not `as Work`) ───────────────────────
const fullWorkData: Work = {
  schemaVersion:1,id:'w1',name:'Test',state:'draft',archiveState:'active',
  blueprintRef:{id:'bp',schemaVersion:1,version:1},
  definitionSnapshot:{schemaVersion:1,revision:1,blueprintRef:{id:'bp',schemaVersion:1,version:1},promptTemplate:'',workflow:{stages:[{id:'s1',title:'S1',tasks:[{id:'t1',title:'T1'}]}]},blockSpecs:[{id:'bs1',kind:'k',schemaVersion:1,label:'L',editable:false,placement:{blockId:'b1',slot:'primary',order:0}}],digest:'sha256:abc'},
  blocks:[{id:'b1',kind:'k',schemaVersion:1,revision:1,status:'ready',data:{},source:{provider:'ai',mode:'snapshot',verified:false},fallback:{summary:''},createdAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T00:00:00Z'}],
  placements:[{blockId:'b1',slot:'primary',order:0}],cornerstones:[],runs:[],prompt:'',
  createdWith:{workSchemaVersion:1,eventSchemaVersion:1,rendererSetVersion:1},
  createdAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T00:00:00Z'
};

function workViewV2(w?: Work): Record<string, unknown> {
  return {schemaVersion:2,revision:1,work:w??fullWorkData,artifactSlots:[],tasks:[],inputs:[]};
}

// ── Compile-time typed DTO fixtures ────────────────────────────────────────
{ const raw: unknown = readFixtureJSON('work-v2-artifact-slot.json'); const s: ArtifactSlot = parseArtifactSlot(raw); void s; }
{ const raw: unknown = readFixtureJSON('work-v2-definition-revision.json'); const r: WorkDefinitionRevision = parseWorkDefinitionRevision(raw); void r; }
{ const raw: unknown = readFixtureJSON('work-v2-work-input.json'); const i: WorkInput = parseWorkInput(raw); void i; }
{ const raw: unknown = readFixtureJSON('work-v2-patch-preview.json'); const p: WorkPatchPreview = parseWorkPatchPreview(raw); void p; }
{ const raw=cloneToRecord(readFixtureJSON('work-v2-patch-preview.json'));
  for(const field of['operations','affectedNodeIds','affectedBlockIds','affectedArtifactSlotIds','staleArtifactSlotIds','invalidatedTaskIds'])raw[field]=null;
  const p=parseWorkPatchPreview(raw);
  assert.deepEqual(p.operations,[]);assert.deepEqual(p.affectedNodeIds,[]);assert.deepEqual(p.affectedBlockIds,[]);
  assert.deepEqual(p.affectedArtifactSlotIds,[]);assert.deepEqual(p.staleArtifactSlotIds,[]);assert.deepEqual(p.invalidatedTaskIds,[]); }
{ const raw: unknown = readFixtureJSON('work-v2-patch-receipt.json'); const p: PatchIntentReceipt = parsePatchIntentReceipt(raw); assert.equal(p.requiresRerun,true); }
{ const raw: unknown = readFixtureJSON('work-v2-patch-apply-result.json'); const p: ApplyWorkPatchResult = parseApplyWorkPatchResult(raw); assert.equal(p.requiresRerun,true); }
{ const r: RetryWorkNodeRequest = { workId:'w1',runId:'r1',taskId:'t1',expectedRevision:1,requestId:'r1' }; void r; }
{ // @ts-expect-error
  const bad: RetryWorkNodeRequest = { workId:'w1',runId:'r1',taskId:'t1',requestId:'r1' }; void bad; }
{ const o: InputSubmissionResult = { input:{id:'i1',workId:'w1',runId:'r1',taskId:'t1',blockId:'b1',specId:'s1',value:{},state:'submitted',revision:1,updatedAt:'2026-01-01T00:00:00Z'},revision:1,duplicate:false }; void o; }
{ // @ts-expect-error
  const bad: InputSubmissionResult = { revision:1,duplicate:false }; void bad; }
{ const o: BeginWorkPlanningResult = {revision:1,duplicate:false,committed:true,recoverable:false}; void o;
  // @ts-expect-error
  const bad: BeginWorkPlanningResult = {revision:1,committed:true,recoverable:false}; void bad; }
{ const o: ApplyDefinitionResult = {revision:2,duplicate:true,committed:true,recoverable:false}; void o;
  // @ts-expect-error
  const bad: ApplyDefinitionResult = {revision:2,duplicate:true,committed:true}; void bad; }
{ const input: WorkInput = {id:'i1',workId:'w1',runId:'r1',taskId:'t1',blockId:'b1',specId:'s1',value:{},state:'submitted',revision:1,updatedAt:'2026-01-01T00:00:00Z'};
  const o: SubmitInputResult = {input,revision:2,duplicate:false,committed:true,recoverable:false}; void o;
  // @ts-expect-error
  const bad: SubmitInputResult = {input,revision:2,duplicate:false,committed:true}; void bad; }
{ const o: PreviewWorkPatchResult = {revision:3,duplicate:true,committed:true,recoverable:false}; void o;
  // @ts-expect-error
  const bad: PreviewWorkPatchResult = {revision:3,duplicate:true,committed:true}; void bad; }
{ const a: DefRevisionAppliedPayload = { workId:'w',revision:1,previousRevision:0,expectedRevision:1 }; void a;
  // @ts-expect-error
  const e: DefRevisionAppliedPayload = { workId:'w',revision:1,previousRevision:0 }; void e; }
{ const a: InputSubmittedPayload = { inputId:'i',workId:'w',runId:'r',taskId:'t',blockId:'b',specId:'s',value:'x',revision:1,expectedRevision:1 }; void a;
  // @ts-expect-error
  const e: InputSubmittedPayload = { inputId:'i',workId:'w',revision:1 }; void e; }
{ const a: InputRejectedPayload = { inputId:'i',workId:'w',runId:'r',taskId:'t',blockId:'b',specId:'s',value:'x',revision:1,expectedRevision:1 }; void a;
  // @ts-expect-error
  const e: InputRejectedPayload = { inputId:'i',workId:'w',value:'x',revision:1,expectedRevision:1 }; void e; }
{ const a: InputCornerstoneChangedPayload = { inputId:'i',workId:'w',runId:'r',taskId:'t',blockId:'b',specId:'s',cornerstoneId:'c',pinned:true,revision:1,expectedRevision:1 }; void a;
  // @ts-expect-error
  const e: InputCornerstoneChangedPayload = { inputId:'i',workId:'w',cornerstoneId:'c',pinned:true }; void e; }
{ const a: PatchAppliedPayload = { patchId:'p',workId:'w',runId:'r',taskId:'t',blockId:'b',scope:'block',newRevision:1,expectedRevision:1,invalidatedTaskIds:[] }; void a;
  // @ts-expect-error
  const e: PatchAppliedPayload = { patchId:'p',workId:'w',runId:'r',taskId:'t',blockId:'b',scope:'block',newRevision:1,invalidatedTaskIds:[] }; void e; }
{ const runtime: V2TaskRuntime = { taskId:'t',workId:'w',runId:'r',nodeId:'n',definitionRev:1,state:'pending',revision:1,updatedAt:'2026-01-01T00:00:00Z' };
  const a: TaskRuntimeCreatedPayload = { taskId:'t',workId:'w',runId:'r',nodeId:'n',expectedRevision:0,definitionRev:1,runtime }; void a;
  // @ts-expect-error
  const e: TaskRuntimeCreatedPayload = { taskId:'t',workId:'w',runId:'r',nodeId:'n',definitionRev:1,runtime }; void e; }
{ const runtime: V2TaskRuntime = { taskId:'t',workId:'w',runId:'r',nodeId:'n',definitionRev:1,state:'ready',revision:2,updatedAt:'2026-01-01T00:00:00Z' };
  const a: TaskRuntimeUpdatedPayload = { taskId:'t',workId:'w',runId:'r',expectedRevision:1,state:'ready',runtime }; void a;
  // @ts-expect-error
  const e: TaskRuntimeUpdatedPayload = { taskId:'t',workId:'w',runId:'r',state:'ready',runtime }; void e; }
{ const a: TaskStaleResultPayload = { taskId:'t',workId:'w',runId:'r',expectedRevision:2,attemptId:'a',staleToken:'old',currentToken:'new' }; void a;
  // @ts-expect-error
  const e: TaskStaleResultPayload = { taskId:'t',workId:'w',runId:'r',attemptId:'a',staleToken:'old',currentToken:'new' }; void e; }

// ── Positive: real SourceRef, CornerstoneRef, ActionReceipt, Run ────────────
{ const w: Work = structuredClone(fullWorkData);
  w.cornerstones = [{id:'c1',workId:'w1',type:'file_ref',title:'Ref Corner',mode:'snapshot',digest:'sha:x',required:false,status:'active',
    ref:{kind:'workspace_file',path:'/x'},
    provenance:{kind:'file',path:'/y'}, pinnedAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T00:00:00Z'}];
  assert.doesNotThrow(() => parseWorkViewV2(workViewV2(w))); }
{ const w: Work = structuredClone(fullWorkData);
  w.actionReceipts = [{workId:'w1',blockId:'b1',actionId:'a1',status:'succeeded',requestId:'r1',retryable:false,outcomeKnown:true}];
  assert.doesNotThrow(() => parseWorkViewV2(workViewV2(w))); }
{ const w: Work = structuredClone(fullWorkData);
  w.runs = [{id:'r1',workId:'w1',state:'running',definitionDigest:'sha:x',
    stages:[{name:'s1',state:'running',startedAt:'2026-01-01T00:00:00Z',tasks:[]}],
    startedAt:'2026-01-01T00:00:00Z',
    cancel:{requestId:'cr1',status:'delivered',attempts:1,updatedAt:'2026-01-01T00:00:00Z'},
    pause:{requestId:'pr1',pausedAt:'2026-01-01T00:00:00Z',notice:'paused'}}];
  assert.doesNotThrow(() => parseWorkViewV2(workViewV2(w))); }
{ const w: Work = structuredClone(fullWorkData);
  w.runs = [{id:'r1',workId:'w1',state:'running',definitionDigest:'sha:x',
    stages:[{name:'s1',state:'running',startedAt:'2026-01-01T00:00:00Z',tasks:[{name:'t1',state:'running',attempts:[{index:0,state:'running',
      sessionRef:{sessionPath:'p',branchId:'b',modelRef:'m',turnCount:1,preview:'Preview',startedAt:'2026-01-01T00:00:00Z'},
      startedAt:'2026-01-01T00:00:00Z',
      receipt:{requestId:'ar1',outcome:'ok',confirmedAt:'2026-01-01T00:00:00Z'}}]}]}],
    startedAt:'2026-01-01T00:00:00Z'}];
  assert.doesNotThrow(() => parseWorkViewV2(workViewV2(w))); }
// BlockFreshness positive
{ const w: Work = structuredClone(fullWorkData);
  w.blocks = [{id:'b1',kind:'k',schemaVersion:1,revision:1,status:'ready',data:{},source:{provider:'ai',mode:'snapshot',verified:false},fallback:{summary:''},createdAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T00:00:00Z',freshness:{checkedAt:'2026-01-01T00:00:00Z',staleReason:'stale'}}];
  assert.doesNotThrow(() => parseWorkViewV2(workViewV2(w))); }
// BlockActionSpec positive
{ const w: Work = structuredClone(fullWorkData);
  w.blocks = [{id:'b1',kind:'k',schemaVersion:1,revision:1,status:'ready',data:{},source:{provider:'ai',mode:'snapshot',verified:false},fallback:{summary:''},createdAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T00:00:00Z',actions:[{id:'a1',label:'L',intent:'I',risk:'read',confirmRequired:false}]}];
  assert.doesNotThrow(() => parseWorkViewV2(workViewV2(w))); }
// Task startedAt/finishedAt positive
{ const w: Work = structuredClone(fullWorkData);
  w.runs = [{id:'r1',workId:'w1',state:'running',definitionDigest:'sha:x',
    stages:[{name:'s1',state:'running',startedAt:'2026-01-01T00:00:00Z',tasks:[{name:'t1',state:'running',attempts:[],startedAt:'2026-01-01T00:00:00Z',finishedAt:'2026-01-01T01:00:00Z'}]}],
    startedAt:'2026-01-01T00:00:00Z'}];
  assert.doesNotThrow(() => parseWorkViewV2(workViewV2(w))); }

// ── Negative tests ─────────────────────────────────────────────────────────
{ const bad = cloneToRecord(readFixtureJSON('work-v2-artifact-slot.json')); bad.state = 'BOGUS'; assert.throws(() => parseArtifactSlot(bad)); }
{ const bad = cloneToRecord(readFixtureJSON('work-v2-work-input.json')); bad.state = 'BOGUS'; assert.throws(() => parseWorkInput(bad)); }
{ const bad = cloneToRecord(readFixtureJSON('work-v2-definition-revision.json')); bad.nodes = [42]; assert.throws(() => parseWorkDefinitionRevision(bad)); }
{ const bad = cloneToRecord(readFixtureJSON('work-v2-patch-receipt.json')); delete bad.requiresRerun; assert.throws(() => parsePatchIntentReceipt(bad)); }
{ const bad = cloneToRecord(readFixtureJSON('work-v2-patch-apply-result.json')); bad.requiresRerun = 'yes'; assert.throws(() => parseApplyWorkPatchResult(bad)); }
{ const bad = cloneToRecord(readFixtureJSON('work-v2-artifact-slot.json')); bad.expectedCount = 0; assert.throws(() => parseArtifactSlot(bad)); }
{ const bad = cloneToRecord(readFixtureJSON('work-v2-definition-revision.json')); bad.createdAt = '2026-02-30T00:00:00Z'; assert.throws(() => parseWorkDefinitionRevision(bad)); }
{ const bad = cloneToRecord(readFixtureJSON('work-v2-artifact-slot.json')); bad.progress = Infinity; assert.throws(() => parseArtifactSlot(bad)); }
{ const bad = cloneToRecord(readFixtureJSON('work-v2-artifact-slot.json')); bad.summary = 42; assert.throws(() => parseArtifactSlot(bad)); }
// 10+ Work recursive negative tests
{ const w = workViewV2(); const wk = cloneToRecord(w.work); const ds = cloneToRecord(wk.definitionSnapshot); ds.blueprintRef = {}; wk.definitionSnapshot = ds; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); const ds = cloneToRecord(wk.definitionSnapshot); const bss = JSON.parse(JSON.stringify(ds.blockSpecs)); delete bss[0].editable; ds.blockSpecs = bss; wk.definitionSnapshot = ds; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); const blk = JSON.parse(JSON.stringify(wk.blocks)); blk[0].createdAt = '2026-01-01T00:00:00'; wk.blocks = blk; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); wk.cornerstones = [{id:'c1',workId:'w1',type:'decision',title:'T',mode:'snapshot',digest:'sha256:x',required:false,status:'active',ref:{},provenance:{kind:'file',path:'x'},pinnedAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T00:00:00Z'}]; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); wk.cornerstones = [{id:'c1',workId:'w1',type:'decision',title:'T',mode:'snapshot',digest:'sha256:x',required:false,status:'active',ref:{kind:'workspace_file',path:'x'},provenance:{},pinnedAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T00:00:00Z'}]; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); wk.runs = [{id:'r1',workId:'w1',state:'running',stages:[],startedAt:'2026-01-01T00:00:00Z'}]; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); const cw = cloneToRecord(wk.createdWith); cw.toolContracts = [{}]; wk.createdWith = cw; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); wk.actionReceipts = [{}]; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); wk.conclusions = [{id:'c1',kind:'fact',status:'proposed',title:'T',generatedAt:'2026-01-01T00:00:00Z'}]; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(); const wk = cloneToRecord(w.work); wk.blocks = [{}]; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ assert.throws(() => parseWorkViewV2({schemaVersion:2,revision:1,work:{id:'w1',name:'T'},tasks:[],inputs:[]})); }
{ const w = workViewV2(structuredClone(fullWorkData)); const wk = cloneToRecord(w.work); wk.state = 'BOGUS'; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
{ const w = workViewV2(structuredClone(fullWorkData)); const wk = cloneToRecord(w.work); wk.createdAt = '2026-01-01T00:00:00'; w.work = wk; assert.throws(() => parseWorkViewV2(w)); }
// Task startedAt missing timezone
{ const w: Work = structuredClone(fullWorkData);
  w.runs = [{id:'r1',workId:'w1',state:'running',definitionDigest:'sha:x',
    stages:[{name:'s1',state:'running',startedAt:'2026-01-01T00:00:00Z',tasks:[{name:'t1',state:'running',attempts:[],startedAt:'2026-01-01T00:00:00'}]}],
    startedAt:'2026-01-01T00:00:00Z'}];
  assert.throws(() => parseWorkViewV2(workViewV2(w))); }

// ── V2 definition revision draft/active validation ─────────────────────────
// Blank draft (goal="", nodes=[]) — accepted.
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'draft',goal:'',nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  const def=parseWorkDefinitionRevision(raw);assert.equal(def.status,'draft');assert.equal(def.goal,'');assert.equal(def.nodes.length,0); }

// Goal-only draft — accepted.
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'draft',goal:'build a report',nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  const def=parseWorkDefinitionRevision(raw);assert.equal(def.goal,'build a report');assert.equal(def.nodes.length,0); }

// Nodes-only draft — accepted.
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'draft',goal:'',nodes:[{id:'n1',title:'T1'}],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  const def=parseWorkDefinitionRevision(raw);assert.equal(def.goal,'');assert.equal(def.nodes.length,1);assert.equal(def.nodes[0].id,'n1'); }

// Draft goal is required and must remain a string even when blank.
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'draft',nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  assert.throws(()=>parseWorkDefinitionRevision(raw)); }
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'draft',goal:null,nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  assert.throws(()=>parseWorkDefinitionRevision(raw)); }
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'draft',goal:42,nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  assert.throws(()=>parseWorkDefinitionRevision(raw)); }

// Active with empty goal — rejected.
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'active',goal:'',nodes:[{id:'n1',title:'T1'}],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  assert.throws(()=>parseWorkDefinitionRevision(raw)); }

// Active with no nodes — rejected.
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'active',goal:'do stuff',nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  assert.throws(()=>parseWorkDefinitionRevision(raw)); }

// Active with both goal and nodes — accepted.
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'active',goal:'do stuff',nodes:[{id:'n1',title:'T1'}],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  const def=parseWorkDefinitionRevision(raw);assert.equal(def.status,'active');assert.equal(def.goal,'do stuff');assert.equal(def.nodes.length,1); }

// Superseded with empty goal — rejected (was an active definition).
{ const raw={workId:'w1',revision:1,parentRevision:0,status:'superseded',goal:'',nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  assert.throws(()=>parseWorkDefinitionRevision(raw)); }

// Superseded with goal and nodes — accepted (valid historical definition).
{ const raw={workId:'w1',revision:2,parentRevision:1,status:'superseded',goal:'was active',nodes:[{id:'n1',title:'T1'}],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  const def=parseWorkDefinitionRevision(raw);assert.equal(def.status,'superseded');assert.equal(def.goal,'was active');assert.equal(def.nodes.length,1); }

// Superseded with no nodes — rejected.
{ const raw={workId:'w1',revision:2,parentRevision:1,status:'superseded',goal:'was active',nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  assert.throws(()=>parseWorkDefinitionRevision(raw)); }

// Missing required field (empty workId) on draft — still rejected.
{ const raw={workId:'',revision:1,parentRevision:0,status:'draft',goal:'',nodes:[],artifactSlots:[],inputSpecs:[],createdBy:'test',createdAt:'2026-01-01T00:00:00Z',digest:'sha256:abc'};
  assert.throws(()=>parseWorkDefinitionRevision(raw)); }
