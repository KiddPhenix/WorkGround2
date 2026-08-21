import assert from "node:assert/strict";
import { applyRoomIcons, applyRoomPins, pinnedRoomRows, ROOM_PIN_LIMIT, roomPinsFull, type RoomRow } from "../components/widget/roomsManager";

function row(topicId: string, pinned = false): RoomRow {
  return { topicId, pinned, icon: "", label: topicId, scope: "global", workspaceRoot: "", sessionPath: `/rooms/${topicId}.jsonl` };
}

assert.equal(ROOM_PIN_LIMIT, 7, "Room desktop pin capacity is seven");
assert.deepEqual(pinnedRoomRows([row("a", true), row("b"), row("c", true)]).map((item) => item.topicId), ["a", "c"]);
assert.equal(roomPinsFull(Array.from({ length: ROOM_PIN_LIMIT - 1 }, (_, index) => row(String(index), true))), false);
assert.equal(roomPinsFull(Array.from({ length: ROOM_PIN_LIMIT }, (_, index) => row(String(index), true))), true);
assert.equal(pinnedRoomRows(Array.from({ length: ROOM_PIN_LIMIT + 2 }, (_, index) => row(String(index), true))).length, ROOM_PIN_LIMIT);

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
