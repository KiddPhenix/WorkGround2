package assistant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResearchKind is the source kind of a research record. It maps to the managed
// Research Session template the supervisor uses (web / GitHub / docs / trusted
// community), so the source URL or repository is always captured with the
// finding.
type ResearchKind string

const (
	ResearchWeb       ResearchKind = "web"
	ResearchGitHub    ResearchKind = "github"
	ResearchDocs      ResearchKind = "docs"
	ResearchCommunity ResearchKind = "community"
)

// ResearchVerification is the durable verification status of a research record.
// External findings start as unverified candidate knowledge (never treated as
// evidence); local validation or multi-source cross-checking promotes them.
type ResearchVerification string

const (
	ResearchUnverified ResearchVerification = "unverified"
	ResearchVerified   ResearchVerification = "verified"
	ResearchRefuted    ResearchVerification = "refuted"
)

// Research is one durable, source-attributable research finding. It records
// WHERE the finding came from (URL/repo), WHEN it was observed, WHAT evidence
// was actually seen, and whether it has been verified. Model guesses are not
// evidence: a record with empty Evidence and no source is rejected.
type Research struct {
	ID           string               `json:"id"`
	AssistantID  string               `json:"assistant_id"`
	SessionID    string               `json:"session_id,omitempty"`
	RespID       string               `json:"resp_id,omitempty"`
	Kind         ResearchKind         `json:"kind"`
	SourceURL    string               `json:"source_url,omitempty"`
	SourceRepo   string               `json:"source_repo,omitempty"`
	Question     string               `json:"question,omitempty"`
	Evidence     string               `json:"evidence,omitempty"`
	Verification ResearchVerification `json:"verification"`
	Revision     int64                `json:"revision"`
	CreatedAt    time.Time            `json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time            `json:"updated_at" ts_type:"string"`
}

func validResearchKind(k ResearchKind) bool {
	switch k {
	case ResearchWeb, ResearchGitHub, ResearchDocs, ResearchCommunity:
		return true
	}
	return false
}

func validResearchVerification(v ResearchVerification) bool {
	switch v {
	case ResearchUnverified, ResearchVerified, ResearchRefuted:
		return true
	}
	return false
}

// RecordResearchInput records (or updates) one research finding idempotently by
// request ID. ExpectedRevision 0 allows an upsert; a non-zero value requires
// the record to exist at that revision (update path).
type RecordResearchInput struct {
	RequestID   string
	AssistantID string
	Research    Research
	// ExpectedRevision guards updates; 0 on create (a fresh record always gets
	// revision 1).
	ExpectedRevision int64
	Now              time.Time
}

// RecordResearch persists one research finding. Duplicate records for the same
// source are detected and reported as already_applied without creating a second
// entry (opportunity dedup by source+question). It is idempotent under
// RequestID and refuses records without a source or concrete evidence, so a
// model guess can never be stored as research.
func (s *Store) RecordResearch(in RecordResearchInput) (Research, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return Research{}, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Research{}, err
	}
	r := in.Research
	r.AssistantID = in.AssistantID
	r.SourceURL = strings.TrimSpace(r.SourceURL)
	r.SourceRepo = strings.TrimSpace(r.SourceRepo)
	r.Evidence = strings.TrimSpace(r.Evidence)
	r.Question = strings.TrimSpace(r.Question)
	if !validResearchKind(r.Kind) {
		return Research{}, fmt.Errorf("assistant: invalid research kind %q", r.Kind)
	}
	if r.SourceURL == "" && r.SourceRepo == "" {
		return Research{}, errors.New("assistant: research requires a source URL or repository")
	}
	if r.Evidence == "" {
		return Research{}, errors.New("assistant: research requires concrete evidence (model guesses are not evidence)")
	}
	if !validResearchVerification(r.Verification) {
		return Research{}, fmt.Errorf("assistant: invalid research verification %q", r.Verification)
	}
	if r.SessionID != "" {
		if err := validateID("session", r.SessionID); err != nil {
			return Research{}, err
		}
	}
	if r.RespID != "" {
		if err := validateID("responsibility", r.RespID); err != nil {
			return Research{}, err
		}
	}
	fp, err := inputFingerprint(struct {
		AssistantID, SessionID, RespID string
		Kind, SourceURL, SourceRepo    string
		Question, Evidence             string
		Verification                   ResearchVerification
	}{r.AssistantID, r.SessionID, r.RespID, string(r.Kind), r.SourceURL, r.SourceRepo, r.Question, r.Evidence, r.Verification})
	if err != nil {
		return Research{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Research{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Research{}, err
	}
	if result, ok, receiptErr := receiptResult[Research](agg, in.RequestID, "record_research", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	now := storeNow(in.Now)

	// Update path: the record already exists and must match the expected
	// revision.
	idx := -1
	for i := range agg.Research {
		if agg.Research[i].ID == r.ID {
			idx = i
			break
		}
	}
	if idx >= 0 {
		if in.ExpectedRevision == 0 {
			// Same source+question already recorded: idempotent no-op rather
			// than a silent second entry.
			return clone(agg.Research[idx]), nil
		}
		if agg.Research[idx].Revision != in.ExpectedRevision {
			return Research{}, conflict("research", r.ID, in.ExpectedRevision, agg.Research[idx].Revision)
		}
		rec := &agg.Research[idx]
		rec.Kind = r.Kind
		rec.SourceURL = r.SourceURL
		rec.SourceRepo = r.SourceRepo
		rec.Question = r.Question
		rec.Evidence = r.Evidence
		rec.Verification = r.Verification
		rec.SessionID = r.SessionID
		rec.RespID = r.RespID
		rec.Revision++
		rec.UpdatedAt = now
		result := clone(*rec)
		touch(agg, now)
		if err := putReceipt(agg, in.RequestID, "record_research", fp, result, now); err != nil {
			return Research{}, err
		}
		if err := validateAggregate(agg); err != nil {
			return Research{}, fmt.Errorf("%w: record research update: %v", ErrCorrupt, err)
		}
		if err := s.write(agg); err != nil {
			return Research{}, err
		}
		return result, nil
	}

	// Create path: dedup by (source URL or repo, question) so the same finding
	// is never stored twice.
	if r.ID == "" {
		r.ID = StableID("research", fmt.Sprintf("%s/%s/%s/%d", in.AssistantID, sourceKey(r), r.Question, now.UnixNano()))
	}
	if err := validateID("research", r.ID); err != nil {
		return Research{}, err
	}
	for i := range agg.Research {
		existing := &agg.Research[i]
		if existing.SourceURL == r.SourceURL && existing.SourceRepo == r.SourceRepo && existing.Question == r.Question && existing.Question != "" {
			return Research{}, fmt.Errorf("assistant: research already recorded for this source: %w", ErrConflict)
		}
	}
	r.Revision = 1
	r.CreatedAt, r.UpdatedAt = now, now
	agg.Research = append(agg.Research, r)
	result := clone(r)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "record_research", fp, result, now); err != nil {
		return Research{}, err
	}
	if err := validateAggregate(agg); err != nil {
		return Research{}, fmt.Errorf("%w: record research: %v", ErrCorrupt, err)
	}
	if err := s.write(agg); err != nil {
		return Research{}, err
	}
	return result, nil
}

func sourceKey(r Research) string {
	if r.SourceRepo != "" {
		return r.SourceRepo
	}
	return r.SourceURL
}

// validateResearch checks one research record against the rest of the aggregate.
func validateResearch(agg *aggregate, r Research) error {
	if err := validateID("research", r.ID); err != nil {
		return err
	}
	if r.AssistantID != agg.Assistant.ID {
		return fmt.Errorf("research %s belongs to %s", r.ID, r.AssistantID)
	}
	if !validResearchKind(r.Kind) {
		return fmt.Errorf("research %s has invalid kind %q", r.ID, r.Kind)
	}
	if r.SourceURL == "" && r.SourceRepo == "" {
		return fmt.Errorf("research %s has no source URL or repository", r.ID)
	}
	if r.Evidence == "" {
		return fmt.Errorf("research %s has no evidence", r.ID)
	}
	if !validResearchVerification(r.Verification) {
		return fmt.Errorf("research %s has invalid verification %q", r.ID, r.Verification)
	}
	if r.RespID != "" && !hasResponsibility(agg, r.RespID) {
		return fmt.Errorf("research %s references missing responsibility %s", r.ID, r.RespID)
	}
	if r.Revision < 1 || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return fmt.Errorf("research %s has invalid revision or timestamps", r.ID)
	}
	return nil
}

func hasResponsibility(agg *aggregate, id string) bool {
	for _, r := range agg.Plan.Responsibilities {
		if r.ID == id {
			return true
		}
	}
	return false
}
