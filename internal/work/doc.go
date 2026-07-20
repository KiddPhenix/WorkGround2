// Package work defines the Work system's core domain types, value objects,
// state machines, and the DefinitionSnapshot canonicalisation contract for V1.
//
// It is a pure domain package — no storage, event log, Service, Controller,
// frontend wiring, or later-phase flows. It does not import internal/control.
package work
