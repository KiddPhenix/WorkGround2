import assert from "node:assert/strict";
import { applyRoomIcons, applyRoomPins, normalizeRoomIcons, normalizeRoomPins, pinnedRoomRows, ROOM_PIN_LIMIT, roomPinsFull, roomRows, type RoomRow } from "../components/widget/roomsManager";

function row(topicId: string, pinned = false): RoomRow {
  return { topicId, pinned, icon: "", label: topicId, scope: "global", workspaceRoot: "", sessionPath: `/rooms/${topicId}.jsonl` };
}

assert.equal(ROOM_PIN_LIMIT, 7, "Room desktop pin capacity is seven");
assert.deepEqual(pinnedRoomRows([row("a", true), row("b"), row("c", true)]).map((item) => item.topicId), ["a", "c"]);
assert.equal(roomPinsFull(Array.from({ length: ROOM_PIN_LIMIT - 1 }, (_, index) => row(String(index), true))), false);
assert.equal(roomPinsFull(Array.from({ length: ROOM_PIN_LIMIT }, (_, index) => row(String(index), true))), true);
assert.equal(pinnedRoomRows(Array.from({ length: ROOM_PIN_LIMIT + 2 }, (_, index) => row(String(index), true))).length, ROOM_PIN_LIMIT);

assert.deepEqual(normalizeRoomPins(null), [], "a nil Go slice serialized by Wails is an empty Room pin list");
assert.deepEqual(
  applyRoomPins([row("local-room")], null).map((item) => [item.topicId, item.pinned]),
  [["local-room", false]],
  "an empty pin store cannot abort projection or hide locally persisted Rooms",
);
assert.deepEqual(normalizeRoomPins({ TopicIDs: [" old-a ", "old-a", "old-b"] }), ["old-a", "old-b"], "old Wails state wrappers remain readable");
assert.deepEqual(normalizeRoomIcons(null), {}, "a nil Go icon map falls back to default glyphs");
assert.deepEqual(normalizeRoomIcons({ Icons: { room: "python" } }), { room: "python" }, "old icon state wrappers remain readable");
assert.throws(() => normalizeRoomPins({ topicIds: [1] }), /非字符串 ID/, "malformed pin payloads remain explicit");
assert.throws(() => normalizeRoomIcons({ room: 1 }), /图标设置格式无效/, "malformed icon payloads remain explicit");
assert.deepEqual(roomRows({ Nodes: [{ Kind: "global_topic", Label: "旧 Room", TopicID: "old-room", SessionKind: "collaboration", SessionPath: "/rooms/old.jsonl" }] }), [
  { topicId: "old-room", label: "旧 Room", pinned: false, icon: "", scope: "global", workspaceRoot: "", sessionPath: "/rooms/old.jsonl" },
], "old Wails ProjectNode field casing is normalized at the Room list boundary");

const projected = applyRoomPins(
  [row("tree-a", true), row("tree-b"), row("tree-c")],
  ["tree-c", "missing", "tree-b", "tree-c"],
);
assert.deepEqual(
  projected.map((item) => [item.topicId, item.pinned]),
  [["tree-c", true], ["tree-b", true], ["tree-a", false]],
  "desktop pins override sidebar pinned state, keep persisted pin order, dedupe stale IDs, then keep tree order",
);
assert.deepEqual(
  applyRoomIcons([row("known"), row("default")], { known: " Python ", default: "unknown", stale: "discussion" }).map((item) => [item.topicId, item.icon]),
  [["known", "python"], ["default", ""]],
  "Room icons normalize through the shared project catalog while stale preferences remain outside the tree projection",
);
assert.deepEqual(
  applyRoomPins([row("valid")], [...Array.from({ length: ROOM_PIN_LIMIT }, (_, index) => `stale-${index}`), "valid"]).map((item) => [item.topicId, item.pinned]),
  [["valid", true]],
  "stale ids do not consume the seven visible Room pin slots",
);

console.log("rooms manager tests passed");
