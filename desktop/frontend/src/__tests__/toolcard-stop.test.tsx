import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { ToolCard } from "../components/ToolCard";
import { LocaleProvider } from "../lib/i18n";
import { initialState, reducer, historyMessagesToItems, type Item } from "../lib/useController";
import type { ToolStopResult } from "../lib/bridge";
import type { WireEvent } from "../lib/types";

const dom = new JSDOM("<!doctype html><html><body></body></html>");
Object.assign(globalThis, {window:dom.window, document:dom.window.document, IS_REACT_ACT_ENVIRONMENT:true});
Object.defineProperty(globalThis,"navigator",{value:dom.window.navigator,configurable:true});
let stop: (tab:string,id:string)=>Promise<ToolStopResult> = async (_,id)=>({stopId:id,status:"accepted"});
Object.assign(dom.window,{go:{main:{App:{StopToolTab:(tab:string,id:string)=>stop(tab,id)}}}});
type ToolItem = Extract<Item,{kind:"tool"}>;
const item = (overrides:Partial<ToolItem>={}):ToolItem => ({kind:"tool",id:"provider-id",callId:"provider-id",stopId:"invocation-1",name:"bash",args:"{}",readOnly:false,status:"running",...overrides});
const host=document.createElement("div"); document.body.appendChild(host);
const root=createRoot(host);
const render=async (value:ToolItem,tabId:string|undefined="tab-a")=>act(async()=>{root.render(<LocaleProvider><ToolCard item={value} tabId={tabId}/></LocaleProvider>)});
const button=()=>host.querySelector<HTMLButtonElement>(".tool__stop");
const click=async()=>act(async()=>{button()?.click();await Promise.resolve()});

await render(item());
assert.ok(button(),"live capability exposes stop");
const calls:string[][]=[];
stop=async(tab,id)=>{calls.push([tab,id]);return {stopId:id,status:"accepted"}};
await click(); await click();
assert.deepEqual(calls,[["tab-a","invocation-1"]],"binding routes exact tab + invocation and suppresses repeat clicks");
assert.equal(button()?.disabled,true);
for(const status of ["done","error","stopped"] as const){ await render(item({status}));assert.equal(button(),null); }
for(const overrides of [{stopId:undefined},{parentId:"parent"},{isShell:true}]){ await render(item(overrides));assert.equal(button(),null); }
await render(item(),"");assert.equal(button(),null,"missing tab never falls back to active tab");

await render(item());
stop=async()=>({status:"accepted",stopId:"wrong-invocation"});
await click();
assert.ok(host.querySelector('[role="alert"]'),"invalid binding response is visible");
assert.equal(button()?.disabled,false,"invalid response stays retryable");
stop=async(_,id)=>({status:"accepted",stopId:id});
await click();assert.equal(button()?.disabled,true);

await render(item({stopId:"invocation-2"}));
let settle!:(value:ToolStopResult)=>void;
stop=()=>new Promise(resolve=>{settle=resolve});
await click();
await render(item({stopId:"invocation-3"}));
await act(async()=>{settle({stopId:"invocation-2",status:"accepted"});await Promise.resolve()});
assert.equal(button()?.disabled,false,"late reply cannot disable replacement invocation");
stop=async(_,id)=>({status:"finished",stopId:id});
await click();assert.equal(button()?.disabled,false);
await act(async()=>{root.unmount()});

let state=reducer(initialState,{type:"user",text:"work",seq:0});
const event=(e:WireEvent)=>{state=reducer(state,{type:"event",e})};
const dispatch=()=>event({kind:"tool_dispatch",tool:{id:"reused",name:"wait",args:"{}",readOnly:true}});
dispatch();event({kind:"tool_progress",tool:{id:"reused",name:"wait",readOnly:true,stopId:"first"}});
let cards=state.items.filter((it):it is ToolItem=>it.kind==="tool");assert.equal(cards[0].stopId,"first");
event({kind:"tool_result",tool:{id:"reused",name:"wait",readOnly:true,stopped:true,err:"stopped"}});
dispatch();event({kind:"tool_progress",tool:{id:"reused",name:"wait",readOnly:true,stopId:"second"}});
cards=state.items.filter((it):it is ToolItem=>it.kind==="tool");
assert.equal(cards.length,2);assert.equal(cards[0].status,"stopped");assert.equal(cards[0].stopId,undefined);
assert.equal(cards[1].stopId,"second");assert.notEqual(cards[0].id,cards[1].id);
assert.equal(state.running,true,"single-tool result leaves turn running");
const restored=historyMessagesToItems([{role:"assistant",content:"",toolCalls:[{id:"reused",name:"wait",arguments:"{}",stopId:"live"}]}],"h").items.find((it):it is ToolItem=>it.kind==="tool");
assert.equal(restored?.status,"running");assert.equal(restored?.stopId,"live");
console.log("tool stop binding, retries, stale replies, reducer and hydration passed");

const stoppedHistory=historyMessagesToItems([
 {role:"assistant",content:"",toolCalls:[{id:"finished",name:"wait",arguments:"{}"}]},
 {role:"tool",content:"",toolCallId:"finished",toolResultArchived:true,toolResultStopped:true},
],"h").items.find((it):it is ToolItem=>it.kind==="tool");
assert.equal(stoppedHistory?.status,"stopped","archived stopped receipt must not display success");

const reusedHistory=historyMessagesToItems([
 {role:"assistant",content:"",toolCalls:[{id:"same",name:"wait",arguments:"{}"}]},
 {role:"tool",content:"stopped",toolCallId:"same",toolResultStopped:true},
 {role:"assistant",content:"",toolCalls:[{id:"same",name:"wait",arguments:"{}"}]},
 {role:"tool",content:"success",toolCallId:"same"},
],"h").items.filter((it):it is ToolItem=>it.kind==="tool");
assert.deepEqual(reusedHistory.map(it=>it.status),["stopped","done"],"reused provider IDs retain their own receipts");
