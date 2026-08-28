// Package assistanttool provides the natural-language control tools an
// Assistant supervisor uses to manage its own Routines, Memory and Policy
// through the authoritative assistant.Store. Every write tool carries a
// request_id for idempotent replay and, where a stale view could overwrite a
// concurrent edit, an expected_revision for optimistic concurrency control.
//
// These tools are read/write adapters over the Store; they do not hold any
// execution state themselves. Session control tools live in the sessiontool
// package because they target the shared Session subsystem rather than the
// Assistant Store.
package assistanttool
