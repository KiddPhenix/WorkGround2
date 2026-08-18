import assert from "node:assert/strict";
import { quickStartAcceptCompletion, quickStartAtItems, quickStartCompletionKey, quickStartCompletionMove, quickStartPickMenu, quickStartSkillMatches, quickStartSkillQuery, quickStartSlashMatches, quickStartSlashQuery } from "../components/widget/quickStartCompletion";
import type { CommandInfo, DirEntry, SlashArgItem } from "../lib/types";

// --- slash trigger detection ---
assert.equal(quickStartSlashQuery("/"), "", "a bare slash starts an empty command query");
assert.equal(quickStartSlashQuery("/model"), "model", "whole-input slash token becomes the query");
assert.equal(quickStartSlashQuery("/model x"), null, "whitespace ends the command-word phase");
assert.equal(quickStartSlashQuery("tell me"), null, "plain text never triggers the slash menu");

const commands: CommandInfo[] = [
  { name: "model", description: "switch model", kind: "builtin" },
  { name: "init", description: "bootstrap", kind: "skill" },
  { name: "mcp", description: "manage MCP", kind: "builtin" },
];
assert.deepEqual(quickStartSlashMatches("m", commands).map((c) => c.name), ["model", "mcp"], "slash matches filter the shared command catalog by substring");
assert.deepEqual(quickStartSlashMatches(null, commands), [], "no slash query yields no matches");

// --- $ Skill trigger detection ---
assert.equal(quickStartSkillQuery("$"), "", "a bare dollar starts the Skill menu");
assert.equal(quickStartSkillQuery("$in"), "in", "whole-input dollar token becomes the Skill query");
assert.equal(quickStartSkillQuery("$init extra"), null, "whitespace ends the Skill-name phase");
assert.equal(quickStartSkillQuery("plain"), null, "plain text never triggers the Skill menu");
assert.deepEqual(quickStartSkillMatches("i", commands).map((c) => c.name), ["init"], "$ only offers matching Skill commands");
assert.deepEqual(quickStartSkillMatches("m", commands), [], "$ never exposes builtin slash commands");

// --- @ file reference items ---
const dirEntries: DirEntry[] = [
  { name: "src", isDir: true },
  { name: "README.md", isDir: false },
];
assert.deepEqual(quickStartAtItems(dirEntries), [
  { entry: dirEntries[0], label: "src", path: "src", isDir: true },
  { entry: dirEntries[1], label: "README.md", path: "README.md", isDir: false },
], "at items carry the label/path/isDir the menu renders");

// --- menu priority: slash > slasharg > skill > at; vocabulary is inline ---
const slashItem: CommandInfo = { name: "model", description: "", kind: "builtin" };
const argItem: SlashArgItem = { label: "list", insert: "list", hint: "", descend: false };

assert.equal(quickStartPickMenu({ slashMatches: [slashItem], slashArgs: { items: [argItem], from: 6 }, at: { token: { raw: "x", dir: "", frag: "x" }, items: [{ entry: dirEntries[1], label: "README.md", path: "README.md", isDir: false }] } })?.kind, "slash", "slash commands win over every other menu");
assert.equal(quickStartPickMenu({ slashMatches: [], slashArgs: { items: [argItem], from: 6 }, at: { token: { raw: "x", dir: "", frag: "x" }, items: [{ entry: dirEntries[1], label: "README.md", path: "README.md", isDir: false }] } })?.kind, "slasharg", "slash arguments come second");
assert.equal(quickStartPickMenu({ slashMatches: [], slashArgs: null, at: { token: { raw: "x", dir: "", frag: "x" }, items: [{ entry: dirEntries[1], label: "README.md", path: "README.md", isDir: false }] } })?.kind, "at", "@ file references come after slash menus");
assert.equal(quickStartPickMenu({ slashMatches: [], slashArgs: null, at: null }), null, "no menu candidates means no menu; vocabulary remains inline");
assert.equal(quickStartPickMenu({ slashMatches: [], slashArgs: { items: [], from: 6 }, at: null }), null, "an empty suggestion list never opens a menu");
const skillMenu = quickStartPickMenu({ slashMatches: [], slashArgs: null, skillMatches: [commands[1]], at: null });
assert.equal(skillMenu?.kind, "skill", "$ Skill candidates open their own menu kind");

// --- key handling: IME-safe, menu-aware, shared submit rule ---
const key = (overrides: Record<string, unknown> = {}) => ({ key: "", shiftKey: false, ctrlKey: false, metaKey: false, altKey: false, ...overrides });
const menu = quickStartPickMenu({ slashMatches: [slashItem], slashArgs: null, at: null });

assert.deepEqual(quickStartCompletionKey(menu, key({ key: "Enter" }), true, "enter"), { type: "none" }, "IME composition swallows all keys including Enter");
assert.deepEqual(quickStartCompletionKey(menu, key({ key: "ArrowDown" }), false, "enter"), { type: "move", delta: 1 }, "menu ArrowDown moves down");
assert.deepEqual(quickStartCompletionKey(menu, key({ key: "ArrowUp" }), false, "enter"), { type: "move", delta: -1 }, "menu ArrowUp moves up");
assert.deepEqual(quickStartCompletionKey(menu, key({ key: "Enter" }), false, "enter"), { type: "accept" }, "menu Enter accepts");
assert.deepEqual(quickStartCompletionKey(menu, key({ key: "Tab" }), false, "enter"), { type: "accept" }, "menu Tab accepts");
assert.deepEqual(quickStartCompletionKey(menu, key({ key: "Tab", shiftKey: true }), false, "enter"), { type: "none" }, "Shift+Tab is never a completion accept");
assert.deepEqual(quickStartCompletionKey(menu, key({ key: "Escape" }), false, "enter"), { type: "close" }, "menu Escape closes");
assert.deepEqual(quickStartCompletionKey(null, key({ key: "Enter" }), false, "enter"), { type: "submit" }, "plain Enter submits with enter-mode");
assert.deepEqual(quickStartCompletionKey(null, key({ key: "Enter" }), false, "ctrl_enter"), { type: "none" }, "plain Enter does not submit in Ctrl+Enter mode");
assert.deepEqual(quickStartCompletionKey(null, key({ key: "Enter", ctrlKey: true }), false, "ctrl_enter"), { type: "submit" }, "Ctrl+Enter submits in Ctrl+Enter mode");
assert.deepEqual(quickStartCompletionKey(null, key({ key: "Escape" }), false, "enter"), { type: "none" }, "Escape without a menu is left to the popup");

// --- active index navigation cycles and clamps ---
const three = quickStartPickMenu({ slashMatches: [slashItem, { name: "mcp", description: "", kind: "builtin" }, { name: "init", description: "", kind: "skill" }], slashArgs: null, at: null })!;
assert.equal(quickStartCompletionMove(three, 1)?.active, 1, "ArrowDown advances the highlight");
assert.equal(quickStartCompletionMove(quickStartCompletionMove(quickStartCompletionMove(three, 1), 1), 1)?.active, 0, "the highlight cycles back to the first item");
assert.equal(quickStartCompletionMove(quickStartCompletionMove(three, -1), -1)?.active, 1, "ArrowUp wraps to the last item");

// --- acceptance keeps the existing input format ---
const slashAccept = quickStartAcceptCompletion(menu!, "");
assert.deepEqual({ text: slashAccept.text, cursor: slashAccept.cursor }, { text: "/model ", cursor: 7 }, "accepting a slash command inserts \"/name \"");
assert.equal(slashAccept.menu, null, "slash acceptance closes the menu");

const args = quickStartPickMenu({ slashMatches: [], slashArgs: { items: [argItem], from: 7 }, at: null })!;
const argAccept = quickStartAcceptCompletion(args, "/skill xyz");
assert.deepEqual({ text: argAccept.text, cursor: argAccept.cursor }, { text: "/skill list", cursor: 11 }, "slash-arg acceptance replaces only the current token");

const skillAccept = quickStartAcceptCompletion(skillMenu!, "$in");
assert.deepEqual({ text: skillAccept.text, cursor: skillAccept.cursor }, { text: "$init ", cursor: 6 }, "accepting a $ Skill keeps the explicit invocation form");

const at = quickStartPickMenu({ slashMatches: [], slashArgs: null, at: { token: { raw: "REA", dir: "", frag: "rea" }, items: [{ entry: dirEntries[1], label: "README.md", path: "README.md", isDir: false }] } })!;
const atAccept = quickStartAcceptCompletion(at, "read @REA");
assert.deepEqual({ text: atAccept.text, cursor: atAccept.cursor }, { text: "read @README.md ", cursor: 16 }, "file reference acceptance inserts \"@path \" verbatim");
const dirAt = quickStartPickMenu({ slashMatches: [], slashArgs: null, at: { token: { raw: "sr", dir: "", frag: "sr" }, items: [{ entry: dirEntries[0], label: "src", path: "src", isDir: true }] } })!;
const dirAccept = quickStartAcceptCompletion(dirAt, "@sr");
assert.deepEqual({ text: dirAccept.text, cursor: dirAccept.cursor }, { text: "@src/", cursor: 5 }, "directory acceptance keeps the trailing slash for the next level");

console.log("quickstart completion tests passed");
