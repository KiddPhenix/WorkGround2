// Package collab implements the authoritative host side of WorkGround2 rooms.
//
// A room stores only shared collaboration facts. Personal agent sessions,
// workspaces, tool approvals, and execution remain owned by each member's
// client. All room mutations pass through Service and are persisted before
// they are exposed to subscribers.
package collab
