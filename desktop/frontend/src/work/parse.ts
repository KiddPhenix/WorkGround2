import { type ObjectKind, type ViewEventType, type WorkView, type WorkViewEvent } from './types';
import { WORK_VIEW_SCHEMA_VERSION_V2, type ApplyWorkPatchResult, type ArtifactSlot, type PatchIntentReceipt, type TaskV2View, type WorkDefinitionRevision, type WorkInput, type WorkPatchPreview, type WorkViewV2 } from './types_v2';

const viewTypes = new Set<ViewEventType>(['snapshot','delta','attention','removed']);
const objectKinds = new Set<ObjectKind>(['work','block','run','stage','task','attempt','cornerstone','conclusion','artifact','definition','artifact_slot','input','patch','node']);

export class ViewFutureSchemaError extends Error {
  constructor(readonly got:number, readonly current:number, readonly eventID:string, readonly subject='WorkViewEvent') { super(`${subject} schema version ${got} exceeds current max ${current} on event "${eventID}"; read-only access is required`); this.name='ViewFutureSchemaError'; }
}
export interface ViewParseResult { event:WorkViewEvent|null; raw:string; futureError:ViewFutureSchemaError|null; }
export type WorkSnapshotParseResult =
  | { kind:'supported'; view:WorkView|WorkViewV2; raw:string }
  | { kind:'unsupported'; schemaVersion:number; raw:string; futureError:ViewFutureSchemaError };

export function parseWorkViewEvent(raw:string):ViewParseResult {
  let value:unknown; try{value=JSON.parse(raw)}catch(e){throw new SyntaxError(`work: decode WorkViewEvent: ${String(e)}`)}
  if(!isRecord(value))throw new TypeError('work: WorkViewEvent must be an object');
  const sv=value.schemaVersion;
  if(!Number.isInteger(sv)||(sv as number)<1)throw new TypeError('work: WorkViewEvent schemaVersion must be a positive integer');
  const eid=typeof value.eventID==='string'?value.eventID:'';
  if((sv as number)>WORK_VIEW_SCHEMA_VERSION_V2){return{event:null,raw,futureError:new ViewFutureSchemaError(sv as number,WORK_VIEW_SCHEMA_VERSION_V2,eid)}}
  validateWorkViewEvent(value);
  return{event:value as unknown as WorkViewEvent,raw,futureError:null};
}
export function rejectWorkViewWrite(r:ViewParseResult):ViewFutureSchemaError|null{return r.futureError}
export function isViewType(r:ViewParseResult,t:ViewEventType):boolean{return r.event?.type===t}
export function deltaAppliesTo(r:ViewParseResult,rev:number):boolean{return r.event?.type==='delta'&&r.event.baseRevision===rev}

export function parseWorkViewSnapshot(raw:unknown):WorkSnapshotParseResult {
  const text=typeof raw==='string'?raw:JSON.stringify(raw);
  let value:unknown;
  try{value=typeof raw==='string'?JSON.parse(raw):raw}catch(e){throw new SyntaxError(`work: decode WorkView snapshot: ${String(e)}`)}
  if(!isRecord(value))throw new TypeError('work: WorkView snapshot must be an object');
  const schemaVersion=value.schemaVersion;
  if(!Number.isInteger(schemaVersion)||(schemaVersion as number)<1)throw new TypeError('work: WorkView snapshot schemaVersion must be a positive integer');
  if((schemaVersion as number)>WORK_VIEW_SCHEMA_VERSION_V2){
    return{
      kind:'unsupported',
      schemaVersion:schemaVersion as number,
      raw:text,
      futureError:new ViewFutureSchemaError(schemaVersion as number,WORK_VIEW_SCHEMA_VERSION_V2,'snapshot','WorkView snapshot'),
    };
  }
  if(schemaVersion===WORK_VIEW_SCHEMA_VERSION_V2){
    return{kind:'supported',view:parseWorkViewV2(value),raw:text};
  }
  safeInt(value.revision);
  validateWork(fields(value.work));
  return{kind:'supported',view:value as unknown as WorkView,raw:text};
}

function validateWorkViewEvent(value:Record<string,unknown>):void{
  if(!viewTypes.has(value.type as ViewEventType))throw new TypeError(`work: invalid WorkViewEvent type ${String(value.type)}`);
  for(const f of['workID','eventID','requestID']as const){if(typeof value[f]!=='string'||value[f].length===0)throw new TypeError(`work: WorkViewEvent requires ${f}`)}
  if(!isRevision(value.revision)||!isRevision(value.baseRevision))throw new TypeError('work: revisions must be safe non-negative');
  if(value.type==='delta'&&value.baseRevision>=value.revision)throw new TypeError('work: delta baseRevision must be lower than revision');
  if(!isRecord(value.object)||!objectKinds.has(value.object.kind as ObjectKind))throw new TypeError('work: WorkViewEvent requires valid object kind');
  if(typeof value.object.id!=='string'||value.object.id.length===0)throw new TypeError('work: WorkViewEvent requires object id');
  const obj=value.object;
  if(obj.expectedRevision!=null){if(typeof obj.expectedRevision!=='number'||!Number.isSafeInteger(obj.expectedRevision)||obj.expectedRevision<0)throw new TypeError('work: ObjectContext.expectedRevision must be safe non-negative')}
  if(obj.definitionRevision!=null){if(typeof obj.definitionRevision!=='number'||!Number.isSafeInteger(obj.definitionRevision)||obj.definitionRevision<0)throw new TypeError('work: ObjectContext.definitionRevision must be safe non-negative')}
  for(const f of['workID','runID','taskID','nodeID','blockID','inputID','specID','definitionID','artifactSlotID','patchID']){const v=obj[f];if(v!=null&&(typeof v!=='string'||v.length===0))throw new TypeError(`work: ObjectContext.${f} must be a non-empty string`)}
  checkDate(value.createdAt);
  if(!('payload'in value))throw new TypeError('work: WorkViewEvent requires payload');
}

// ── Narrowing helpers ──────────────────────────────────────────────────────
function isRecord(v:unknown):v is Record<string,unknown>{return typeof v==='object'&&v!==null&&!Array.isArray(v)}
function isRevision(v:unknown):v is number{return Number.isSafeInteger(v)&&(v as number)>=0}
export{isRevision as _isRevision};
export function validateRevision(label:string,v:unknown):asserts v is number{if(!Number.isSafeInteger(v)||(v as number)<0)throw new TypeError(`work: ${label} safe non-negative`)}
export function validateTaskID(id:unknown):asserts id is string{if(typeof id!=='string'||id.length===0)throw new TypeError('work: task id non-empty')}
function fields(v:unknown):Record<string,unknown>{if(!isRecord(v))throw new TypeError('work: expected object');return v}
function arr(v:unknown):unknown[]{if(!Array.isArray(v))throw new TypeError('work: expected array');return v}
function arrOrEmpty(v:unknown):unknown[]{return v==null?[]:arr(v)}
function str(v:unknown):string{if(typeof v!=='string')throw new TypeError('work: expected string');return v}
function nonEmpty(v:unknown):string{const s=str(v);if(s.length===0)throw new TypeError('work: expected non-empty string');return s}
function safeInt(v:unknown):number{if(typeof v!=='number'||!Number.isFinite(v)||!Number.isSafeInteger(v)||v<0)throw new TypeError(`work: expected safe non-negative int`);return v}
function posInt(v:unknown):number{if(typeof v!=='number'||!Number.isFinite(v)||!Number.isSafeInteger(v)||v<1)throw new TypeError(`work: expected positive safe int`);return v}
function checkBool(v:unknown):boolean{if(typeof v!=='boolean')throw new TypeError('work: expected boolean');return v}
function hasOwn(r:Record<string,unknown>,k:string):boolean{return Object.prototype.hasOwnProperty.call(r,k)}
function ownFields(r:Record<string,unknown>,k:string):Record<string,unknown>{if(!hasOwn(r,k))throw new TypeError(`work: missing required field "${k}"`);return fields(r[k])}
function ownArr(r:Record<string,unknown>,k:string):unknown[]{if(!hasOwn(r,k))throw new TypeError(`work: missing required field "${k}"`);return arr(r[k])}
function daysInMonth(y:number,m:number):number{
  if(m===2)return isLeapYear(y)?29:28;
  return[31,28,31,30,31,30,31,31,30,31,30,31][m-1];
}
function isLeapYear(y:number):boolean{return(y%4===0&&y%100!==0)||y%400===0;}
function checkDate(v:unknown):string{
  const s=str(v);
  const m=/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.exec(s);
  if(!m)throw new TypeError(`work: strict RFC3339 required, got ${s}`);
  const Y=+m[1],M=+m[2],D=+m[3],h=+m[4],mi=+m[5],se=+m[6];
  if(Y===0)throw new TypeError(`work: year 0000 is not accepted: ${s}`);
  if(M<1||M>12||D<1||D>daysInMonth(Y,M))throw new TypeError(`work: invalid calendar date: ${s}`);
  if(h>23||mi>59||se>59)throw new TypeError(`work: invalid time components: ${s}`);
  // Calendar roundtrip via Date.UTC is reliable for years >= 100; for years
  // 1-99 JS maps to 1900-1999, so we rely on the manual daysInMonth check above.
  if(Y>=100){
    const d=new Date(Date.UTC(Y,M-1,D,h,mi,se));
    if(Number.isNaN(d.getTime()))throw new TypeError(`work: invalid date: ${s}`);
    if(d.getUTCFullYear()!==Y||d.getUTCMonth()+1!==M||d.getUTCDate()!==D)throw new TypeError(`work: invalid calendar date: ${s}`);
  }
  const off=m[8];
  if(off!=='Z'&&off){
    const om=/^[+-](\d{2}):(\d{2})$/.exec(off);if(!om)throw new TypeError(`work: invalid offset: ${s}`);
    const oh=+om[1],os=+om[2];if(oh>23||os>59)throw new TypeError(`work: invalid offset components: ${s}`);
  }
  return s;
}
function checkDateOpt(r:Record<string,unknown>,k:string):void{if(hasOwn(r,k))checkDate(r[k])}
function strArr(a:unknown[]):void{for(let i=0;i<a.length;i++){const v=a[i];if(typeof v!=='string'||v.length===0)throw new TypeError(`work: [${i}] non-empty string`)}}

// ── Enums ──────────────────────────────────────────────────────────────────
const slotStates=new Set(['reserved','generating','ready','partial','failed','stale']);
const inputStates=new Set(['requested','draft','submitted','rejected','accepted']);
const patchScopes=new Set(['block','workflow']);
const patchActionKinds=new Set(['reuse','reformat','rerun','ask_user']);
const defStatuses=new Set(['draft','active','superseded']);
const inputKinds=new Set(['text','number','date','choice','multi_choice','file','roster','form','approval']);
const artRefStatuses=new Set(['available','stale','missing','failed']);
const taskV2States=new Set(['pending','ready','running','waiting_input','waiting_approval','completed','failed_retryable','failed_terminal','canceled','invalidated']);
const workStates=new Set(['draft','ready','running','waiting_user','paused','completed','failed','cancelled']);
const archStates=new Set(['active','archived','deleted']);
const runStates=new Set(['pending','running','waiting','completed','failed','cancelled','needs_confirmation']);
const sideEffects=new Set(['read','workspace_write','external_write','destructive']);
const conclKinds=new Set(['fact','finding','decision','outcome','lesson']);
const conclStatuses=new Set(['proposed','confirmed','superseded']);
const cornTypes=new Set(['instruction','file_ref','file_snapshot','decision','conclusion','source','policy','parameter']);
const cornModes=new Set(['live_ref','snapshot']);
const cornStatuses=new Set(['active','stale','missing','denied','invalid']);
const blockStatuses=new Set(['loading','ready','empty','stale','blocked','failed']);
const placeSlots=new Set(['primary','secondary','attention','result']);
const actionRisks=new Set(['read','write','destructive','external']);
const srcRefKinds=new Set(['work','session_turn','block','artifact','file','url']);
const crRefKinds=new Set(['inline','session_turn','workspace_file','artifact','url']);
const gateOutcomes=new Set(['approved','input_provided']);
const cancelDeliveries=new Set(['pending','delivered','failed']);
const resolveErrorKinds=new Set(['missing','denied','invalid','network']);
const receiptStatuses=new Set(['pending','running','succeeded','failed','rejected','unknown']);

function checkEnum(v:unknown,set:Set<string>,label:string):string{const s=str(v);if(!set.has(s))throw new TypeError(`work: ${label} invalid: ${s}`);return s}

// ── Sub-validators ─────────────────────────────────────────────────────────
function validateBlueprintRef(r:Record<string,unknown>):void{nonEmpty(r.id);safeInt(r.schemaVersion);posInt(r.version)}

function validateToolContract(r:Record<string,unknown>):void{nonEmpty(r.name);safeInt(r.contractVersion);checkEnum(r.sideEffectClass,sideEffects,'sideEffectClass');checkBool(r.required);if(r.provider!==undefined)str(r.provider)}

function validateSourceRef(r:Record<string,unknown>):void{checkEnum(r.kind,srcRefKinds,'SourceRef.kind');if(r.workId!=null)str(r.workId);if(r.objectId!=null)str(r.objectId);if(r.path!=null)str(r.path);if(r.url!=null)str(r.url);if(r.digest!=null)str(r.digest)}

function validateCornerstoneRef(r:Record<string,unknown>):void{checkEnum(r.kind,crRefKinds,'ref.kind');if(r.sessionId!=null)str(r.sessionId);if(r.turn!=null)safeInt(r.turn);if(r.path!=null)str(r.path);if(r.artifactId!=null)str(r.artifactId);if(r.url!=null)str(r.url);if(r.blobDigest!=null)str(r.blobDigest)}

// ── Recursive Work validator ──────────────────────────────────────────────
function validateWork(raw:unknown):void{
  const w=fields(raw);
  safeInt(w.schemaVersion);nonEmpty(w.id);nonEmpty(w.name);
  checkEnum(w.state,workStates,'Work.state');checkEnum(w.archiveState,archStates,'Work.archiveState');

  // blueprintRef
  validateBlueprintRef(ownFields(w,'blueprintRef'));

  // definitionSnapshot
  const ds=ownFields(w,'definitionSnapshot');
  safeInt(ds.schemaVersion);safeInt(ds.revision);
  const dsBlueprint=ownFields(ds,'blueprintRef');
  const emptyV2Definition=w.schemaVersion===WORK_VIEW_SCHEMA_VERSION_V2&&dsBlueprint.id===''&&ds.digest==='';
  if(emptyV2Definition){str(dsBlueprint.id);safeInt(dsBlueprint.schemaVersion);safeInt(dsBlueprint.version)}
  else validateBlueprintRef(dsBlueprint);
  str(ds.promptTemplate);
  // workflow
  const wf=fields(ds.workflow);
  for(const st of arr(wf.stages)){
    const s=fields(st);nonEmpty(s.id);nonEmpty(s.title);
    if(s.gate!==undefined)str(s.gate);
    for(const t of arr(s.tasks)){const tk=fields(t);nonEmpty(tk.id);nonEmpty(tk.title)}
  }
  // blockSpecs
  for(const b of ownArr(ds,'blockSpecs')as unknown[]){
    const bk=fields(b);nonEmpty(bk.id);nonEmpty(bk.kind);safeInt(bk.schemaVersion);nonEmpty(bk.label);
    if(!hasOwn(bk,'editable'))throw new TypeError('work: blockSpec.editable required');checkBool(bk.editable);
    if(bk.description!==undefined)str(bk.description);
    const pl=fields(bk.placement);nonEmpty(pl.blockId);checkEnum(pl.slot,placeSlots,'placement.slot');safeInt(pl.order);
    if(pl.span!==undefined)safeInt(pl.span);if(pl.collapsed!==undefined)checkBool(pl.collapsed);
  }
  // cornerstoneRequirements
  if(ds.cornerstoneRequirements!==undefined){for(const cr of arr(ds.cornerstoneRequirements)){const c=fields(cr);checkEnum(c.type,cornTypes,'cr.type');checkBool(c.required);if(c.label!==undefined)str(c.label)}}
  // conclusionKinds (enum)
  if(ds.conclusionKinds!==undefined){for(const k of arr(ds.conclusionKinds))checkEnum(k,conclKinds,'conclusionKind')}
  // artifactKinds
  if(ds.artifactKinds!==undefined)strArr(arr(ds.artifactKinds));
  // toolContracts
  if(ds.toolContracts!==undefined){for(const tc of arr(ds.toolContracts))validateToolContract(fields(tc))}
  if(emptyV2Definition)str(ds.digest);else nonEmpty(ds.digest);

  // blocks
  for(const b of arr(w.blocks)){
    const bk=fields(b);nonEmpty(bk.id);nonEmpty(bk.kind);safeInt(bk.schemaVersion);safeInt(bk.revision);
    checkEnum(bk.status,blockStatuses,'block.status');
    // data required
    if(!hasOwn(bk,'data'))throw new TypeError('work: block.data required');
    if(bk.title!==undefined)str(bk.title);
    const src=fields(bk.source);nonEmpty(src.provider);checkEnum(src.mode,new Set(['snapshot','query','stream']),'source.mode');checkBool(src.verified);
    if(src.ref!==undefined)str(src.ref);
    if(bk.freshness!==undefined){const fr=fields(bk.freshness);checkDateOpt(fr,'checkedAt');checkDateOpt(fr,'expiresAt');checkDateOpt(fr,'retryAt');if(fr.staleReason!==undefined)str(fr.staleReason)}
    const fb=fields(bk.fallback);str(fb.summary);
    if(fb.data!==undefined){} // data is optional raw
    if(bk.actions!==undefined){for(const a of arr(bk.actions)){const ac=fields(a);nonEmpty(ac.id);nonEmpty(ac.label);nonEmpty(ac.intent);checkEnum(ac.risk,actionRisks,'action.risk');checkBool(ac.confirmRequired)}}
    if(bk.tombstone!==undefined)checkBool(bk.tombstone);
    checkDate(bk.createdAt);checkDate(bk.updatedAt);
  }

  // placements
  for(const p of arr(w.placements)){const pl=fields(p);nonEmpty(pl.blockId);checkEnum(pl.slot,placeSlots,'placement.slot');safeInt(pl.order);if(pl.span!==undefined)safeInt(pl.span);if(pl.collapsed!==undefined)checkBool(pl.collapsed)}

  // prompt
  str(w.prompt);

  // cornerstones
  for(const c of arr(w.cornerstones)){
    const co=fields(c);nonEmpty(co.id);nonEmpty(co.workId);checkEnum(co.type,cornTypes,'cornerstone.type');nonEmpty(co.title);
    checkEnum(co.mode,cornModes,'cornerstone.mode');nonEmpty(co.digest);checkBool(co.required);
    checkEnum(co.status,cornStatuses,'cornerstone.status');
    // ref
    validateCornerstoneRef(ownFields(co,'ref'));
    // provenance
    validateSourceRef(ownFields(co,'provenance'));
    // optional
    if(co.content!==undefined)str(co.content);if(co.tags!==undefined)strArr(arr(co.tags));
    checkDate(co.pinnedAt);checkDate(co.updatedAt);
    checkDateOpt(co,'lastVerifiedAt');if(co.error!==undefined)str(co.error);if(co.resolveErrorKind!==undefined)checkEnum(co.resolveErrorKind,resolveErrorKinds,'resolveErrorKind');
    if(co.candidateDigest!==undefined)str(co.candidateDigest);if(co.tombstone!==undefined)checkBool(co.tombstone);
  }

  // runs
  for(const r of arr(w.runs)){
    const run=fields(r);nonEmpty(run.id);nonEmpty(run.workId);checkEnum(run.state,runStates,'run.state');
    nonEmpty(run.definitionDigest);
    if(run.requestId!==undefined)str(run.requestId);
    for(const st of arr(run.stages)){
      const stage=fields(st);nonEmpty(stage.name);checkEnum(stage.state,runStates,'stage.state');
      checkDate(stage.startedAt);checkDateOpt(stage,'finishedAt');
      if(stage.id!==undefined)str(stage.id);if(stage.gate!==undefined)str(stage.gate);if(stage.resolution!==undefined){const gr=fields(stage.resolution);nonEmpty(gr.stageId);checkEnum(gr.outcome,gateOutcomes,'GateResolution.outcome');if(gr.input!=null){if(!isRecord(gr.input))throw new TypeError('work: GateResolution.input must be an object')}if(gr.note!==undefined)str(gr.note)}
      for(const t of arr(stage.tasks)){
        const task=fields(t);nonEmpty(task.name);checkEnum(task.state,runStates,'task.state');
        if(task.id!==undefined)str(task.id);checkDateOpt(task,'startedAt');checkDateOpt(task,'finishedAt');
        for(const a of arr(task.attempts)){
          const att=fields(a);safeInt(att.index);checkEnum(att.state,runStates,'attempt.state');
          const sr=fields(att.sessionRef);nonEmpty(sr.sessionPath);nonEmpty(sr.branchId);nonEmpty(sr.modelRef);safeInt(sr.turnCount);
          nonEmpty(sr.preview);checkDate(sr.startedAt);
          checkDate(att.startedAt);
          if(att.id!==undefined)str(att.id);if(att.requestId!==undefined)str(att.requestId);if(att.error!==undefined)str(att.error);
          if(att.sideEffectClass!==undefined)str(att.sideEffectClass);
          checkDateOpt(att,'finishedAt');
          if(att.receipt!==undefined){const rc=fields(att.receipt);nonEmpty(rc.requestId);str(rc.outcome);checkDate(rc.confirmedAt);if(rc.evidence!==undefined)str(rc.evidence);if(rc.sideEffectClass!==undefined)str(rc.sideEffectClass)}
        }
      }
    }
    checkDate(run.startedAt);checkDateOpt(run,'finishedAt');
    // conclusion?: Conclusion
    if(run.conclusion!==undefined){const cc=fields(run.conclusion);nonEmpty(cc.id);checkEnum(cc.kind,conclKinds,'conclusion.kind');checkEnum(cc.status,conclStatuses,'conclusion.status');nonEmpty(cc.title);nonEmpty(cc.summary);checkDate(cc.generatedAt);
      if(cc.evidence!==undefined){for(const e of arr(cc.evidence))validateSourceRef(fields(e))}if(cc.artifacts!==undefined){for(const a of arr(cc.artifacts))parseArtifactRefV2(fields(a),'conclusion.artifact')}if(cc.nextSteps!==undefined)strArr(arr(cc.nextSteps));if(cc.supersedes!==undefined)str(cc.supersedes)}
    // cancel?: RunCancelReceipt
    if(run.cancel!==undefined){const cr=fields(run.cancel);nonEmpty(cr.requestId);checkEnum(cr.status,cancelDeliveries,'cancel.status');if(cr.error!==undefined)str(cr.error);safeInt(cr.attempts);checkDate(cr.updatedAt)}
    // pause?: RunPauseReceipt
    if(run.pause!==undefined){const pr=fields(run.pause);nonEmpty(pr.requestId);checkDate(pr.pausedAt);nonEmpty(pr.notice)}
  }

  // createdWith
  const cw=ownFields(w,'createdWith');safeInt(cw.workSchemaVersion);safeInt(cw.eventSchemaVersion);safeInt(cw.rendererSetVersion);
  if(cw.toolContracts!==undefined){for(const tc of arr(cw.toolContracts))validateToolContract(fields(tc))}
  if(cw.provider!==undefined)str(cw.provider);if(cw.model!==undefined)str(cw.model);
  checkDate(w.createdAt);checkDate(w.updatedAt);

  // optional: inputs
  if(hasOwn(w,'inputs')){const inp=w.inputs;if(typeof inp!=='object'||inp===null||Array.isArray(inp))throw new TypeError('work: inputs must be an object')}
  // actionReceipts recursive
  if(w.actionReceipts!==undefined){for(const a of arr(w.actionReceipts)){const ac=fields(a);nonEmpty(ac.workId);nonEmpty(ac.blockId);nonEmpty(ac.actionId);checkEnum(ac.status,receiptStatuses,'receipt.status');nonEmpty(ac.requestId);checkBool(ac.retryable);checkBool(ac.outcomeKnown);
    if(ac.blockKind!==undefined)str(ac.blockKind);if(ac.handlerIdentityVersion!==undefined)safeInt(ac.handlerIdentityVersion);if(ac.handlerId!==undefined)str(ac.handlerId);if(ac.handlerVersion!==undefined)str(ac.handlerVersion);
    if(ac.message!==undefined)str(ac.message);if(ac.inputDigest!==undefined)str(ac.inputDigest);if(ac.fingerprint!==undefined)str(ac.fingerprint);if(ac.intent!==undefined)str(ac.intent);if(ac.summary!==undefined)str(ac.summary);
    if(ac.risk!==undefined)checkEnum(ac.risk,actionRisks,'receipt.risk');if(ac.confirmRequired!==undefined)checkBool(ac.confirmRequired);
    if(ac.revision!==undefined)safeInt(ac.revision);checkDateOpt(ac,'createdAt');checkDateOpt(ac,'updatedAt')}}
  // conclusions
  if(w.conclusions!==undefined){
    for(const c of arr(w.conclusions)){const cc=fields(c);nonEmpty(cc.id);checkEnum(cc.kind,conclKinds,'conclusion.kind');checkEnum(cc.status,conclStatuses,'conclusion.status');nonEmpty(cc.title);nonEmpty(cc.summary);checkDate(cc.generatedAt);
      if(cc.evidence!==undefined){for(const e of arr(cc.evidence))validateSourceRef(fields(e))}
      if(cc.artifacts!==undefined){for(const a of arr(cc.artifacts))parseArtifactRefV2(fields(a),'conclusion.artifact')}
      if(cc.nextSteps!==undefined)strArr(arr(cc.nextSteps));if(cc.supersedes!==undefined)str(cc.supersedes);
    }
  }
  if(w.rerunOf!==undefined)nonEmpty(w.rerunOf);if(w.copiedFrom!==undefined)nonEmpty(w.copiedFrom);
  if(w.referencedWorks!==undefined)strArr(arr(w.referencedWorks));
  if(w.rerunUpgraded!==undefined)checkBool(w.rerunUpgraded);
  if(w.migrationPath!==undefined){for(const v of arr(w.migrationPath))safeInt(v)}
  checkDateOpt(w,'archivedAt');
}

// ── V2 DTO parsers ─────────────────────────────────────────────────────────
function parseArtifactRefV2(r:Record<string,unknown>,label:string):void{
  nonEmpty(r.id);nonEmpty(r.name);nonEmpty(r.type);checkEnum(r.status,artRefStatuses,`${label}.status`);
  if(r.path!=null)str(r.path);if(r.relativePath!=null)str(r.relativePath);if(r.blobDigest!=null)str(r.blobDigest);
  if(r.sourceRunId!=null)str(r.sourceRunId);if(r.error!=null)str(r.error);if(r.lastVerifiedAt!=null)checkDate(r.lastVerifiedAt);
  if(r.url!=null)str(r.url);
}
export function parseArtifactSlot(raw:unknown):ArtifactSlot{
  const r=fields(raw);nonEmpty(r.id);nonEmpty(r.workId);safeInt(r.definitionRev);safeInt(r.revision);
  if(r.upstreamDigest!=null)str(r.upstreamDigest);
  nonEmpty(r.title);nonEmpty(r.kind);posInt(r.expectedCount);checkBool(r.required);
  checkEnum(r.state,slotStates,'ArtifactSlot.state');
  if(!hasOwn(r,'artifactRefs'))throw new TypeError('work: missing required field "artifactRefs"');
  const artifactRefs=r.artifactRefs===null?[]:arr(r.artifactRefs);
  for(let i=0;i<artifactRefs.length;i++)parseArtifactRefV2(fields(artifactRefs[i]),`artifactRefs[${i}]`);
  if(r.progress!=null){if(typeof r.progress!=='number'||!Number.isFinite(r.progress)||r.progress<0||r.progress>1)throw new TypeError('work: progress 0..1')}
  if(r.error!=null){const e=fields(r.error);nonEmpty(e.code);nonEmpty(e.message);checkBool(e.retryable)}
  if(r.state==='failed'&&r.error==null)throw new TypeError('work: ArtifactSlot state=failed requires error');
  if(r.summary!=null)str(r.summary);
  return{...r,artifactRefs}as unknown as ArtifactSlot;
}
function parseNodeDef(n:unknown):void{const r=fields(n);nonEmpty(r.id);nonEmpty(r.title);if(r.description!=null)str(r.description);if(r.dependsOn!=null)strArr(arr(r.dependsOn));if(r.inputSpecIds!=null)strArr(arr(r.inputSpecIds));if(r.toolHints!=null)strArr(arr(r.toolHints));if(r.blockIds!=null)strArr(arr(r.blockIds));if(r.producesSlotIds!=null)strArr(arr(r.producesSlotIds));if(r.consumesSlotIds!=null)strArr(arr(r.consumesSlotIds))}
function parseASD(s:unknown):void{const r=fields(s);nonEmpty(r.id);nonEmpty(r.title);nonEmpty(r.kind);posInt(r.expectedCount);checkBool(r.required)}
function parseISD(s:unknown):void{const r=fields(s);nonEmpty(r.id);nonEmpty(r.label);if(r.description!=null)str(r.description);checkEnum(r.kind,inputKinds,'InputSpec.kind');checkBool(r.required);checkBool(r.pinEligible)}
function parsePO(o:unknown):void{const r=fields(o);nonEmpty(r.op);nonEmpty(r.path)}
function parsePA(a:unknown):void{
  const r=fields(a);checkEnum(r.action,patchActionKinds,'PatchAction.action');
  if(r.nodeId!=null)nonEmpty(r.nodeId);if(r.artifactSlotId!=null)nonEmpty(r.artifactSlotId);if(r.question!=null)str(r.question);if(r.reason!=null)str(r.reason);
  if((r.action==='reuse'||r.action==='rerun')&&(r.nodeId==null||r.artifactSlotId!=null))throw new TypeError(`work: PatchAction ${r.action} requires only nodeId`);
  if(r.action==='reformat'&&(r.artifactSlotId==null||r.nodeId!=null))throw new TypeError('work: PatchAction reformat requires only artifactSlotId');
  if(r.action==='ask_user'&&(typeof r.question!=='string'||r.question.trim().length===0))throw new TypeError('work: PatchAction ask_user requires question');
}
export function parseWorkDefinitionRevision(raw:unknown):WorkDefinitionRevision{
  const r=fields(raw);
  nonEmpty(r.workId);safeInt(r.revision);safeInt(r.parentRevision);
  checkEnum(r.status,defStatuses,'DefinitionStatus');
  const status=r.status as string;
  // Only draft may be blank, goal-only, or nodes-only.
  // active and superseded both represent an applied historical definition and must have goal + ≥1 node.
  if(status==='draft'){
    str(r.goal);
    for(const n of arr(r.nodes))parseNodeDef(n);
  }else{
    // active / superseded
    nonEmpty(r.goal);
    const nodes=arr(r.nodes);
    if(nodes.length===0)throw new TypeError('work: active/superseded definition must have at least one node');
    for(const n of nodes)parseNodeDef(n);
  }
  for(const s of arr(r.artifactSlots))parseASD(s);
  for(const s of arr(r.inputSpecs))parseISD(s);
  nonEmpty(r.createdBy);checkDate(r.createdAt);nonEmpty(r.digest);
  return r as unknown as WorkDefinitionRevision;
}
export function parseWorkInput(raw:unknown):WorkInput{const r=fields(raw);nonEmpty(r.id);nonEmpty(r.workId);nonEmpty(r.runId);nonEmpty(r.taskId);nonEmpty(r.blockId);nonEmpty(r.specId);checkEnum(r.state,inputStates,'InputState');safeInt(r.revision);checkDate(r.updatedAt);if(r.cornerstoneId!=null)str(r.cornerstoneId);return r as unknown as WorkInput}
export function parseWorkPatchPreview(raw:unknown):WorkPatchPreview{const r=fields(raw);nonEmpty(r.id);nonEmpty(r.workId);nonEmpty(r.runId);nonEmpty(r.taskId);nonEmpty(r.blockId);nonEmpty(r.sessionId);safeInt(r.baseDefinitionRev);safeInt(r.baseBlockRev);checkEnum(r.scope,patchScopes,'PatchScope');const operations=arrOrEmpty(r.operations);for(const o of operations)parsePO(o);const actions=arrOrEmpty(r.actions);for(const a of actions)parsePA(a);const affectedNodeIds=arrOrEmpty(r.affectedNodeIds);const affectedBlockIds=arrOrEmpty(r.affectedBlockIds);const affectedArtifactSlotIds=arrOrEmpty(r.affectedArtifactSlotIds);const staleArtifactSlotIds=arrOrEmpty(r.staleArtifactSlotIds);const invalidatedTaskIds=arrOrEmpty(r.invalidatedTaskIds);strArr(affectedNodeIds);strArr(affectedBlockIds);strArr(affectedArtifactSlotIds);strArr(staleArtifactSlotIds);strArr(invalidatedTaskIds);checkBool(r.requiresRerun);nonEmpty(r.digest);checkDate(r.expiresAt);return{...r,operations,actions,affectedNodeIds,affectedBlockIds,affectedArtifactSlotIds,staleArtifactSlotIds,invalidatedTaskIds}as unknown as WorkPatchPreview}
export function parsePatchIntentReceipt(raw:unknown):PatchIntentReceipt{const r=fields(raw);nonEmpty(r.requestId);nonEmpty(r.operation);nonEmpty(r.intentDigest);nonEmpty(r.patchId);safeInt(r.resultRevision);str(r.resultDigest);if(r.resultPatch!=null)parseWorkPatchPreview(r.resultPatch);if(r.scope!=null)checkEnum(r.scope,patchScopes,'PatchScope');if(r.newRevision!=null)safeInt(r.newRevision);if(r.invalidatedTaskIds!=null)strArr(arr(r.invalidatedTaskIds));if(r.affectedBlockIds!=null)strArr(arr(r.affectedBlockIds));if(r.affectedArtifactSlotIds!=null)strArr(arr(r.affectedArtifactSlotIds));if(r.staleArtifactSlotIds!=null)strArr(arr(r.staleArtifactSlotIds));checkBool(r.requiresRerun);if(r.error!=null)str(r.error);checkDate(r.createdAt);return r as unknown as PatchIntentReceipt}
export function parseApplyWorkPatchResult(raw:unknown):ApplyWorkPatchResult{const r=fields(raw);safeInt(r.workRevision);safeInt(r.newRevision);if(r.invalidatedTaskIds!=null)strArr(arr(r.invalidatedTaskIds));if(r.affectedBlockIds!=null)strArr(arr(r.affectedBlockIds));if(r.affectedArtifactSlotIds!=null)strArr(arr(r.affectedArtifactSlotIds));if(r.staleArtifactSlotIds!=null)strArr(arr(r.staleArtifactSlotIds));checkBool(r.requiresRerun);checkBool(r.duplicate);if(r.error!=null)str(r.error);return r as unknown as ApplyWorkPatchResult}
export function parseTaskV2View(raw:unknown):TaskV2View{const tk=fields(raw);nonEmpty(tk.id);nonEmpty(tk.runId);nonEmpty(tk.nodeId);nonEmpty(tk.title);checkEnum(tk.state,taskV2States,'TaskV2View.state');checkBool(tk.retryable);checkDate(tk.updatedAt);if(tk.progress!=null)str(tk.progress);if(tk.sessionRef!=null){const sr=fields(tk.sessionRef);nonEmpty(sr.sessionPath);nonEmpty(sr.branchId);nonEmpty(sr.modelRef);safeInt(sr.turnCount);str(sr.preview);checkDate(sr.startedAt)}if(tk.error!=null)str(tk.error);if(tk.waitingInputIds!=null)strArr(arr(tk.waitingInputIds));return tk as unknown as TaskV2View}
export function parseWorkViewV2(raw:unknown):WorkViewV2{
  const r=fields(raw);
  if(r.schemaVersion!==WORK_VIEW_SCHEMA_VERSION_V2)throw new TypeError(`work: WorkViewV2 schemaVersion must be ${WORK_VIEW_SCHEMA_VERSION_V2}`);
  safeInt(r.revision);
  validateWork(fields(r.work));
  const parsed:Record<string,unknown>={...r};
  if(r.definition!=null)parsed.definition=parseWorkDefinitionRevision(r.definition);
  if(r.artifactSlots!=null)parsed.artifactSlots=arr(r.artifactSlots).map(parseArtifactSlot);
  if(r.tasks!=null)parsed.tasks=arr(r.tasks).map(parseTaskV2View);
  if(r.inputs!=null)parsed.inputs=arr(r.inputs).map(parseWorkInput);
  if(r.patchPreviews!=null)parsed.patchPreviews=arr(r.patchPreviews).map(parseWorkPatchPreview);
  return parsed as unknown as WorkViewV2;
}
