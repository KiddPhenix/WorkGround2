package main

import (
	"workground2/internal/control"
	"workground2/internal/event"
)

// controllerTaskMemory reads the optional briefing port without widening the
// desktop's main SessionAPI dependency or breaking lean test doubles.
func controllerTaskMemory(ctrl control.SessionAPI) (event.TaskMemory, bool) {
	if ctrl == nil {
		return event.TaskMemory{}, false
	}
	provider, ok := ctrl.(control.TaskMemoryStatus)
	if !ok {
		return event.TaskMemory{}, false
	}
	return provider.TaskMemory(), true
}
