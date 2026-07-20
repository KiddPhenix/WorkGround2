package control

import "workground2/internal/work"

// WorkControl is the controller-side driving port shared by all frontends.
// The concrete Controller will forward these intents to internal/work.Service;
// this contract intentionally contains no Work business rules.
type WorkControl interface {
	work.WorkController
}

// WorkViewSink is the controller-side transport sink. Persisted WorkEvent
// values never pass through this boundary.
type WorkViewSink = work.ViewSink
