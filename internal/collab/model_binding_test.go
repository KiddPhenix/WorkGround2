package collab

import (
	"reflect"
	"testing"
	"time"
)

// Wails cannot infer time.Time while generating TypeScript bindings. Keep
// every exported collaboration timestamp explicitly represented as a string.
func TestWailsTimeFieldsDeclareStringType(t *testing.T) {
	models := []any{
		Room{},
		Member{},
		ChatMessage{},
		Contribution{},
		AgentRequest{},
		AgentRun{},
		AgentResult{},
		FileOffer{},
		Reaction{},
		RoomEvent{},
		SweepInput{},
	}
	timeType := reflect.TypeOf(time.Time{})
	for _, model := range models {
		typ := reflect.TypeOf(model)
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			fieldType := field.Type
			if fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}
			if fieldType == timeType && field.Tag.Get("ts_type") != "string" {
				t.Errorf("%s.%s must declare ts_type:string for Wails bindings", typ.Name(), field.Name)
			}
		}
	}
}
