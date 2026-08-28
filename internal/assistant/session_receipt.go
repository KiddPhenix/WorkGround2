package assistant

import (
	"encoding/json"
	"errors"
	"time"
)

// sessionCreateOperation is the receipt operation for a Session-creation
// idempotency binding (request ID -> Session ID).
const sessionCreateOperation = "session_create"

// ResolveSessionCreation returns the Session ID previously created for a request
// ID, or ok=false when no binding exists yet.
func (s *Store) ResolveSessionCreation(assistantID, requestID string) (string, bool, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return "", false, err
	}
	if err := validateRequestID(requestID); err != nil {
		return "", false, err
	}
	unlock, err := s.lockAssistant(assistantID)
	if err != nil {
		return "", false, err
	}
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return "", false, err
	}
	r, ok := agg.Requests[requestID]
	if !ok {
		return "", false, nil
	}
	if r.Operation != sessionCreateOperation {
		return "", false, &IdempotencyError{RequestID: requestID, Operation: sessionCreateOperation}
	}
	var sessionID string
	if err := json.Unmarshal(r.Result, &sessionID); err != nil {
		return "", false, err
	}
	return sessionID, true, nil
}

// RecordSessionCreation binds a request ID to a Session ID idempotently. A
// replay (lost response, crash, restart) returns the previously recorded
// Session ID instead of creating a second Session.
func (s *Store) RecordSessionCreation(assistantID, requestID, sessionID string) (string, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return "", err
	}
	if err := validateRequestID(requestID); err != nil {
		return "", err
	}
	if sessionID == "" {
		return "", errors.New("assistant: session id is required")
	}
	unlock, err := s.lockAssistant(assistantID)
	if err != nil {
		return "", err
	}
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return "", err
	}
	if r, ok := agg.Requests[requestID]; ok {
		if r.Operation != sessionCreateOperation {
			return "", &IdempotencyError{RequestID: requestID, Operation: sessionCreateOperation}
		}
		var existing string
		if err := json.Unmarshal(r.Result, &existing); err != nil {
			return "", err
		}
		return existing, nil
	}
	now := storeNow(time.Now())
	result, err := json.Marshal(sessionID)
	if err != nil {
		return "", err
	}
	agg.Requests[requestID] = requestReceipt{
		Operation: sessionCreateOperation, Fingerprint: sessionID,
		Result: result, CreatedAt: now,
	}
	touch(agg, now)
	if err := s.write(agg); err != nil {
		return "", err
	}
	return sessionID, nil
}
