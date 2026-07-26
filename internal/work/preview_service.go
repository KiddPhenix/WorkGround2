package work

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ── ApprovalVerifier ──────────────────────────────────────────────────────

// ApprovalVerifier checks whether an external conversion is authorised.
// The default implementation rejects everything.
type ApprovalVerifier interface {
	VerifyExternalApproval(ExternalApprovalIntent, string) error
}

type ExternalApprovalIntent struct {
	WorkID             string            `json:"workId"`
	DefinitionRevision int64             `json:"definitionRevision"`
	SlotID             string            `json:"slotId"`
	SlotRevision       int64             `json:"slotRevision"`
	ArtifactRefID      string            `json:"artifactId"`
	ContentDigest      string            `json:"contentDigest"`
	Converter          ConverterIdentity `json:"converter"`
	AllowExternal      bool              `json:"allowExternal"`
	RequestID          string            `json:"requestId"`
}

type defaultApprovalVerifier struct{}

func (d defaultApprovalVerifier) VerifyExternalApproval(ExternalApprovalIntent, string) error {
	return errors.New("external conversion not approved: no verifier configured")
}

// ── PreviewArtifact Request / Result ──────────────────────────────────────

type PreviewArtifactRequest struct {
	WorkID             string `json:"workId"`
	DefinitionRevision int64  `json:"definitionRevision"`
	SlotID             string `json:"slotId"`
	SlotRevision       int64  `json:"slotRevision"`
	ArtifactRefID      string `json:"artifactId"`
	RequestID          string `json:"requestId"`
}

type PreviewArtifactResult struct {
	Preview        *ArtifactPreview    `json:"preview,omitempty"`
	Committed      bool                `json:"committed"`
	Recoverable    bool                `json:"recoverable"`
	TransportError *WorkTransportError `json:"transportError,omitempty"`
}

// ── RequestArtifactConversion ─────────────────────────────────────────────

type RequestArtifactConversionInput struct {
	WorkID             string `json:"workId"`
	DefinitionRevision int64  `json:"definitionRevision"`
	SlotID             string `json:"slotId"`
	SlotRevision       int64  `json:"slotRevision"`
	ArtifactRefID      string `json:"artifactId"`
	RequestID          string `json:"requestId"`
	AllowExternal      bool   `json:"allowExternal"`
	ApprovalToken      string `json:"approvalToken"`
}

type RequestArtifactConversionResult struct {
	Preview        *ArtifactPreview    `json:"preview,omitempty"`
	Committed      bool                `json:"committed"`
	Recoverable    bool                `json:"recoverable"`
	Duplicate      bool                `json:"duplicate"`
	TransportError *WorkTransportError `json:"transportError,omitempty"`
}

// ── Conversion receipt (persisted intent) ─────────────────────────────────

type conversionReceipt struct {
	RequestID          string    `json:"requestId"`
	WorkID             string    `json:"workId"`
	ArtifactRefID      string    `json:"artifactId"`
	SlotID             string    `json:"slotId"`
	SlotRevision       int64     `json:"slotRevision"`
	DefinitionRevision int64     `json:"definitionRevision"`
	ContentDigest      string    `json:"contentDigest"`
	MimeType           string    `json:"mimeType,omitempty"`
	ConverterName      string    `json:"converterName,omitempty"`
	ConverterVersion   string    `json:"converterVersion,omitempty"`
	ConverterTarget    string    `json:"converterTarget,omitempty"`
	AllowExternal      bool      `json:"allowExternal"`
	ExternalApproved   bool      `json:"externalApproved,omitempty"`
	IntentDigest       string    `json:"intentDigest"`
	State              string    `json:"state"`
	ResultDigest       string    `json:"resultDigest,omitempty"`
	Error              string    `json:"error,omitempty"`
	RetryCount         int       `json:"retryCount"`
	LeaseOwner         string    `json:"leaseOwner,omitempty"`
	LeaseUntil         time.Time `json:"leaseUntil,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

const maxConversionRetries = 3
const conversionLease = 30 * time.Second
const maxConversionPump = 64

// conversionIntentDigest computes a stable digest of the conversion intent.
func conversionIntentDigest(input RequestArtifactConversionInput, contentDigest string, converter ConverterIdentity) string {
	raw := fmt.Sprintf("conv:v3:%s:%s:%s:%d:%d:%s:%s:%s:%s:%t:%s",
		input.WorkID, input.ArtifactRefID, input.SlotID,
		input.DefinitionRevision, input.SlotRevision,
		contentDigest, converter.Name, converter.Version, converter.Target,
		input.AllowExternal,
		input.RequestID,
	)
	sum := sha256.Sum256([]byte(raw))
	return digestPrefix + fmt.Sprintf("%x", sum[:])
}

// ── PreviewService ─────────────────────────────────────────────────────────

type PreviewService struct {
	state        previewStateStore
	converters   []Converter
	sources      ArtifactSourceResolver
	approval     ApprovalVerifier
	ownerID      string
	autoStart    bool
	beforeCommit func()
	mu           sync.RWMutex
	asyncErrs    sync.Map
}

func NewPreviewService(store WorkStore, _ string) *PreviewService {
	var state previewStateStore
	if value, ok := store.(previewStateStore); ok {
		state = value
	}
	return &PreviewService{
		state:      state,
		converters: builtinConverters(),
		approval:   defaultApprovalVerifier{},
		ownerID:    newPreviewOwnerID(),
		autoStart:  true,
	}
}

func newPreviewOwnerID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("preview-%d-%d", os.Getpid(), time.Now().UnixNano())
}

// SetApprovalVerifier replaces the external-approval checker.
func (ps *PreviewService) SetApprovalVerifier(v ApprovalVerifier) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if v == nil {
		v = defaultApprovalVerifier{}
	}
	ps.approval = v
}

// SetArtifactSourceResolver installs the authoritative binary source boundary.
// Nil is deliberately fail-closed.
func (ps *PreviewService) SetArtifactSourceResolver(resolver ArtifactSourceResolver) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.sources = resolver
}

func (ps *PreviewService) RegisterConverter(c Converter) {
	if c == nil || !c.Identity().Valid() {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.converters = append([]Converter{c}, ps.converters...)
}

// ── Preview (synchronous, read-only) ──────────────────────────────────────

// Preview renders only with local converters. External converters are reachable
// exclusively through RequestConversion after explicit approval.
func (ps *PreviewService) Preview(
	ctx context.Context,
	input PreviewArtifactRequest,
	artifactRef ArtifactRef,
) (*ArtifactPreview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input.WorkID == "" || artifactRef.ID == "" {
		return nil, errors.New("work: Preview: workID and artifactRef.ID required")
	}
	plan, err := ps.resolvePreviewPlan(ctx, ArtifactSourceRequest{
		WorkID:             input.WorkID,
		DefinitionRevision: input.DefinitionRevision,
		SlotID:             input.SlotID,
		SlotRevision:       input.SlotRevision,
		ArtifactRefID:      artifactRef.ID,
	})
	if err != nil {
		return ps.degradedPreview(artifactRef, err.Error())
	}
	converter := ps.findConverter(plan.grade, plan.mimeType, false)
	if converter == nil {
		return ps.fileCardPreview(artifactRef, plan.grade, plan.mimeType, plan.size, plan.contentDigest)
	}
	identity := converter.Identity()
	cacheKey := previewCacheDigest(
		input.WorkID,
		input.DefinitionRevision,
		input.SlotID,
		input.SlotRevision,
		artifactRef.ID,
		plan.contentDigest,
		identity,
		false,
	)
	if ps.state != nil {
		cached, found, err := ps.state.LoadVisiblePreviewCache(input.WorkID, cacheKey)
		if err != nil {
			return nil, err
		}
		if found {
			cached.CachedAt = time.Now()
			return cached, nil
		}
	}
	if plan.grade == PreviewFileCard {
		preview, _ := ps.fileCardPreview(artifactRef, plan.grade, plan.mimeType, plan.size, plan.contentDigest)
		ps.decoratePreview(preview, input.WorkID, artifactRef.ID, plan, identity)
		ps.decorateFileCardState(input, plan, identity, preview)
		return preview, nil
	}

	source, cleanup, err := snapshotPreviewSource(plan.source.Data, plan.source.Name)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	preview, convertErr := converter.Convert(input.WorkID, source, plan.mimeType)
	if preview == nil {
		preview = &ArtifactPreview{}
	}
	ps.decoratePreview(preview, input.WorkID, artifactRef.ID, plan, identity)
	// Synchronous inline preview is never a durable conversion completion.
	preview.ConversionState = ConversionIdle
	if convertErr != nil {
		preview.Error = convertErr.Error()
		return preview, nil
	}
	current, err := ps.resolvePreviewPlan(ctx, ArtifactSourceRequest{
		WorkID:             input.WorkID,
		DefinitionRevision: input.DefinitionRevision,
		SlotID:             input.SlotID,
		SlotRevision:       input.SlotRevision,
		ArtifactRefID:      artifactRef.ID,
	})
	if err != nil || current.contentDigest != plan.contentDigest {
		if err == nil {
			err = errors.New("preview: artifact digest changed; late result discarded")
		}
		preview.Error = err.Error()
		return preview, nil
	}
	if ps.state != nil {
		preview.CachedAt = time.Now()
		if err := ps.state.PutPreviewCache(input.WorkID, cacheKey, preview); err != nil {
			preview.Error = err.Error()
			return preview, err
		}
	}
	return preview, nil
}

func (ps *PreviewService) decorateFileCardState(
	input PreviewArtifactRequest,
	plan previewPlan,
	converter ConverterIdentity,
	preview *ArtifactPreview,
) {
	if ps.state == nil || preview == nil {
		return
	}
	receipts, err := ps.state.ListConversionReceipts(input.WorkID)
	if err != nil {
		preview.Error = err.Error()
		return
	}
	var latest *conversionReceipt
	for i := range receipts {
		receipt := &receipts[i]
		if receipt.WorkID != input.WorkID ||
			receipt.DefinitionRevision != input.DefinitionRevision ||
			receipt.SlotID != input.SlotID ||
			receipt.SlotRevision != input.SlotRevision ||
			receipt.ArtifactRefID != input.ArtifactRefID ||
			receipt.ContentDigest != plan.contentDigest ||
			receipt.ConverterName != converter.Name ||
			receipt.ConverterVersion != converter.Version ||
			receipt.ConverterTarget != converter.Target ||
			receipt.AllowExternal {
			continue
		}
		if latest == nil || receipt.UpdatedAt.After(latest.UpdatedAt) {
			latest = receipt
		}
	}
	if latest == nil {
		return
	}
	preview.ConversionState = latest.State
	preview.Error = latest.Error
	if latest.State == ConversionCompleted {
		preview.ConversionState = ConversionFailed
		preview.Error = "conversion preview cache is missing; request conversion to repair"
	}
}

// ── RequestConversion (durable intent-first, async execution) ────────────

// RequestConversion persists an intent and returns before converter execution.
// Repeating the same request returns its durable state; a different intent with
// the same requestID is rejected. PumpConversions resumes pending/expired work.
func (ps *PreviewService) RequestConversion(ctx context.Context, input RequestArtifactConversionInput) (*RequestArtifactConversionResult, error) {
	result := &RequestArtifactConversionResult{}
	if err := ctx.Err(); err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.SlotID = strings.TrimSpace(input.SlotID)
	input.ArtifactRefID = strings.TrimSpace(input.ArtifactRefID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" || input.ArtifactRefID == "" || input.RequestID == "" ||
		input.SlotID == "" || input.DefinitionRevision <= 0 || input.SlotRevision <= 0 {
		return result, errors.New("work: RequestConversion: workId, positive definitionRevision, slotId, positive slotRevision, artifactId, and requestId required")
	}
	if ps.state == nil {
		err := errors.New("work: RequestConversion: preview persistence is unavailable")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}

	plan, err := ps.resolveConversionPlan(ctx, input)
	if err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	converter := ps.findConverter(plan.grade, plan.mimeType, input.AllowExternal)
	converterIdentity := ConverterIdentity{Name: "none", Version: "1", Target: "filecard"}
	if converter != nil {
		converterIdentity = converter.Identity()
		if !converterIdentity.Valid() {
			err := errors.New("conversion: converter identity is incomplete")
			result.TransportError = TransportErrorFrom(err)
			return result, err
		}
	}
	intentDigest := conversionIntentDigest(input, plan.contentDigest, converterIdentity)
	preexisting, preexistingFound, err := ps.state.LoadConversionReceipt(input.WorkID, input.RequestID)
	if err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if preexistingFound && preexisting.IntentDigest != intentDigest {
		err := fmt.Errorf("conversion: requestID %q reused with different intent", input.RequestID)
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	externalApproved := false
	if converter != nil && converterIsExternal(converter) {
		if preexistingFound && preexisting.IntentDigest == intentDigest && preexisting.ExternalApproved {
			externalApproved = true
		} else {
			approvalIntent := ExternalApprovalIntent{
				WorkID:             input.WorkID,
				DefinitionRevision: input.DefinitionRevision,
				SlotID:             input.SlotID,
				SlotRevision:       input.SlotRevision,
				ArtifactRefID:      input.ArtifactRefID,
				ContentDigest:      plan.contentDigest,
				Converter:          converterIdentity,
				AllowExternal:      input.AllowExternal,
				RequestID:          input.RequestID,
			}
			if !input.AllowExternal || strings.TrimSpace(input.ApprovalToken) == "" {
				err := errors.New("conversion: external converter requires explicit approval")
				result.TransportError = TransportErrorFrom(err)
				return result, err
			}
			if err := ps.verifyExternalApproval(approvalIntent, input.ApprovalToken); err != nil {
				result.TransportError = TransportErrorFrom(err)
				return result, err
			}
			externalApproved = true
		}
	}
	now := time.Now()
	schedule := false
	duplicate := false
	receipt, _, err := ps.state.MutateConversionReceipt(
		input.WorkID,
		input.RequestID,
		func(current *conversionReceipt, exists bool) (*conversionReceipt, error) {
			if exists {
				duplicate = true
				if current.IntentDigest != intentDigest {
					return nil, fmt.Errorf(
						"conversion: requestID %q reused with different intent",
						input.RequestID,
					)
				}
				switch current.State {
				case ConversionCompleted:
					return current, nil
				case ConversionPending:
					schedule = true
					return current, nil
				case ConversionRunning:
					if current.LeaseUntil.IsZero() || !current.LeaseUntil.After(now) {
						schedule = true
					}
					return current, nil
				case ConversionFailed:
					if current.RetryCount >= maxConversionRetries {
						return current, nil
					}
					next := *current
					next.State = ConversionPending
					next.Error = ""
					next.RetryCount++
					next.LeaseOwner = ""
					next.LeaseUntil = time.Time{}
					next.UpdatedAt = now
					schedule = true
					return &next, nil
				default:
					return nil, fmt.Errorf("conversion: invalid persisted state %q", current.State)
				}
			}
			schedule = true
			return &conversionReceipt{
				RequestID:          input.RequestID,
				WorkID:             input.WorkID,
				ArtifactRefID:      input.ArtifactRefID,
				SlotID:             input.SlotID,
				SlotRevision:       input.SlotRevision,
				DefinitionRevision: input.DefinitionRevision,
				ContentDigest:      plan.contentDigest,
				MimeType:           plan.mimeType,
				ConverterName:      converterIdentity.Name,
				ConverterVersion:   converterIdentity.Version,
				ConverterTarget:    converterIdentity.Target,
				AllowExternal:      input.AllowExternal,
				ExternalApproved:   externalApproved,
				IntentDigest:       intentDigest,
				State:              ConversionPending,
				UpdatedAt:          now,
			}, nil
		},
	)
	if err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	result.Duplicate = duplicate

	if receipt.State == ConversionCompleted {
		return ps.resultFromReceipt(input, plan, receipt, result)
	}
	if receipt.State == ConversionPending && duplicate && converter != nil {
		if ps.autoStart {
			go ps.executeConversion(input.WorkID, input.RequestID)
		}
		result.Preview = previewFromReceipt(input.WorkID, receipt)
		result.Committed = true
		result.Recoverable = true
		ps.attachAsyncError(input.WorkID, input.RequestID, result)
		return result, nil
	}
	if receipt.State == ConversionRunning {
		if ps.autoStart && (receipt.LeaseUntil.IsZero() || !receipt.LeaseUntil.After(now)) {
			go ps.executeConversion(input.WorkID, input.RequestID)
		}
		result.Preview = previewFromReceipt(input.WorkID, receipt)
		result.Committed = true
		result.Recoverable = true
		ps.attachAsyncError(input.WorkID, input.RequestID, result)
		return result, nil
	}
	if receipt.State == ConversionFailed && receipt.RetryCount >= maxConversionRetries {
		result.Committed = false
		result.Recoverable = false
		result.TransportError = &WorkTransportError{Message: receipt.Error}
		return result, nil
	}

	if converter == nil {
		preview := ps.fileCardDegradeOnly(plan.ref, plan.grade, plan.mimeType)
		ps.decoratePreview(preview, input.WorkID, input.ArtifactRefID, plan, converterIdentity)
		preview.ConversionState = ConversionCompleted
		cacheKey := ps.cacheKey(input, plan.contentDigest, converterIdentity)
		err = ps.commitConversion(ctx, input, receipt, cacheKey, preview)
		if err != nil {
			result.TransportError = TransportErrorFrom(err)
			result.Committed = true
			result.Recoverable = true
			return result, err
		}
		result.Preview = preview
		result.Committed = true
		result.Recoverable = false
		return result, nil
	}

	if schedule && ps.autoStart {
		go ps.executeConversion(input.WorkID, input.RequestID)
	}
	result.Preview = ps.pendingPreview(plan.ref, input.WorkID, plan)
	result.Committed = true
	result.Recoverable = true
	ps.attachAsyncError(input.WorkID, input.RequestID, result)
	return result, nil
}

// executeConversion claims one durable receipt. At most one live instance owns
// a claim; expired claims can be recovered after a crash.
func (ps *PreviewService) executeConversion(workID, requestID string) {
	receipt, claimed, err := ps.claimConversion(workID, requestID)
	if err != nil || !claimed {
		return
	}
	stopHeartbeat := make(chan struct{})
	go ps.heartbeatConversion(workID, requestID, receipt.IntentDigest, stopHeartbeat)
	defer close(stopHeartbeat)

	input := RequestArtifactConversionInput{
		WorkID:             workID,
		DefinitionRevision: receipt.DefinitionRevision,
		SlotID:             receipt.SlotID,
		SlotRevision:       receipt.SlotRevision,
		ArtifactRefID:      receipt.ArtifactRefID,
		RequestID:          requestID,
		AllowExternal:      receipt.AllowExternal,
	}
	ctx := context.Background()
	plan, err := ps.resolveConversionPlan(ctx, input)
	if err != nil {
		ps.failConversion(workID, requestID, receipt.IntentDigest, err)
		return
	}
	if plan.contentDigest != receipt.ContentDigest {
		ps.failConversion(workID, requestID, receipt.IntentDigest, errors.New("conversion: artifact digest changed; late result discarded"))
		return
	}
	identity := ConverterIdentity{
		Name:    receipt.ConverterName,
		Version: receipt.ConverterVersion,
		Target:  receipt.ConverterTarget,
	}
	converter := ps.findConverterByIdentity(identity, plan.grade, plan.mimeType)
	if converter == nil {
		ps.failConversion(workID, requestID, receipt.IntentDigest, fmt.Errorf("conversion: converter %q is unavailable", receipt.ConverterName))
		return
	}
	if converterIsExternal(converter) && !receipt.ExternalApproved {
		ps.failConversion(workID, requestID, receipt.IntentDigest, errors.New("conversion: external approval receipt is missing"))
		return
	}
	cacheKey := ps.cacheKey(input, plan.contentDigest, identity)
	if cached, found, loadErr := ps.state.LoadPreviewCache(workID, cacheKey); loadErr != nil {
		ps.failConversion(workID, requestID, receipt.IntentDigest, loadErr)
		return
	} else if found {
		cached.ConversionState = ConversionCompleted
		if err := ps.commitConversion(ctx, input, receipt, cacheKey, cached); err != nil {
			ps.failConversion(workID, requestID, receipt.IntentDigest, err)
		}
		return
	}

	source, cleanup, err := snapshotPreviewSource(plan.source.Data, plan.source.Name)
	if err != nil {
		ps.failConversion(workID, requestID, receipt.IntentDigest, err)
		return
	}
	preview, convertErr := converter.Convert(workID, source, plan.mimeType)
	cleanup()
	if convertErr != nil {
		ps.failConversion(workID, requestID, receipt.IntentDigest, convertErr)
		return
	}
	if preview == nil {
		preview = &ArtifactPreview{}
	}
	ps.decoratePreview(preview, workID, receipt.ArtifactRefID, plan, identity)
	preview.ConversionState = ConversionCompleted
	if err := ps.commitConversion(ctx, input, receipt, cacheKey, preview); err != nil {
		ps.failConversion(workID, requestID, receipt.IntentDigest, err)
		return
	}
}

// PumpConversions scans for pending/running receipts and resumes them.
// Call this on restart to recover stuck conversions.
func (ps *PreviewService) PumpConversions(ctx context.Context, workID string) (int, error) {
	return ps.pumpConversions(ctx, workID, maxConversionPump)
}

func (ps *PreviewService) pumpConversions(ctx context.Context, workID string, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if limit <= 0 || limit > maxConversionPump {
		limit = maxConversionPump
	}
	if ps.state == nil {
		return 0, errors.New("work: conversion pump persistence is unavailable")
	}
	receipts, err := ps.state.ListConversionReceipts(workID)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	sort.Slice(receipts, func(i, j int) bool {
		if receipts[i].UpdatedAt.Equal(receipts[j].UpdatedAt) {
			return receipts[i].RequestID < receipts[j].RequestID
		}
		return receipts[i].UpdatedAt.Before(receipts[j].UpdatedAt)
	})
	count := 0
	for _, r := range receipts {
		if count >= limit {
			break
		}
		if r.State == ConversionPending ||
			(r.State == ConversionRunning && (r.LeaseUntil.IsZero() || !r.LeaseUntil.After(now))) {
			go ps.executeConversion(workID, r.RequestID)
			count++
		}
	}
	return count, nil
}

func (ps *PreviewService) fileCardDegradeOnly(ref ArtifactRef, grade PreviewGrade, mimeType string) *ArtifactPreview {
	return &ArtifactPreview{
		ArtifactRefID:    ref.ID,
		Grade:            grade,
		MimeType:         mimeType,
		CanOpen:          true,
		CanConvert:       false,
		Summary:          officeSummary(ref.Name, mimeType, 0),
		ThumbnailDataURL: officeThumbnailSVG(mimeType),
		ConverterVersion: "1",
	}
}

type previewPlan struct {
	ref           ArtifactRef
	source        *ArtifactSource
	contentDigest string
	mimeType      string
	grade         PreviewGrade
	size          int64
}

func (ps *PreviewService) resolvePreviewPlan(ctx context.Context, request ArtifactSourceRequest) (previewPlan, error) {
	ps.mu.RLock()
	resolver := ps.sources
	ps.mu.RUnlock()
	if resolver == nil {
		return previewPlan{}, errors.New("artifact source resolver is unavailable")
	}
	source, err := resolver.ResolveArtifactSource(ctx, request)
	if err != nil {
		return previewPlan{}, err
	}
	if source == nil || source.ContentDigest == "" || ContentDigest(source.Data) != source.ContentDigest {
		return previewPlan{}, errors.New("artifact source resolver returned an invalid snapshot")
	}
	return previewPlan{
		ref:           source.Ref,
		source:        source,
		contentDigest: source.ContentDigest,
		mimeType:      source.MimeType,
		grade:         GradeArtifact(source.Name, source.MimeType),
		size:          int64(len(source.Data)),
	}, nil
}

func (ps *PreviewService) resolveConversionPlan(ctx context.Context, input RequestArtifactConversionInput) (previewPlan, error) {
	plan, err := ps.resolvePreviewPlan(ctx, ArtifactSourceRequest{
		WorkID:             input.WorkID,
		DefinitionRevision: input.DefinitionRevision,
		SlotID:             input.SlotID,
		SlotRevision:       input.SlotRevision,
		ArtifactRefID:      input.ArtifactRefID,
	})
	if err != nil {
		return previewPlan{}, fmt.Errorf("conversion: resolve artifact source: %w", err)
	}
	return plan, nil
}

func (ps *PreviewService) cacheKey(input RequestArtifactConversionInput, contentDigest string, converter ConverterIdentity) string {
	return previewCacheDigest(
		input.WorkID,
		input.DefinitionRevision,
		input.SlotID,
		input.SlotRevision,
		input.ArtifactRefID,
		contentDigest,
		converter,
		input.AllowExternal,
	)
}

func (ps *PreviewService) decoratePreview(
	preview *ArtifactPreview,
	workID string,
	artifactID string,
	plan previewPlan,
	converter ConverterIdentity,
) {
	preview.ArtifactRefID = artifactID
	preview.WorkID = workID
	preview.ContentDigest = plan.contentDigest
	preview.Grade = plan.grade
	preview.MimeType = plan.mimeType
	preview.FileSize = plan.size
	preview.ConverterVersion = converter.Version
	preview.CanOpen = true
	preview.CanConvert = plan.grade == PreviewFileCard && converter.Name != "none"
}

func (ps *PreviewService) pendingPreview(ref ArtifactRef, workID string, plan previewPlan) *ArtifactPreview {
	preview := ps.fileCardDegradeOnly(ref, plan.grade, plan.mimeType)
	preview.WorkID = workID
	preview.ContentDigest = plan.contentDigest
	preview.FileSize = plan.size
	preview.CanConvert = true
	preview.ConversionState = ConversionPending
	return preview
}

func (ps *PreviewService) verifyExternalApproval(intent ExternalApprovalIntent, token string) error {
	ps.mu.RLock()
	approval := ps.approval
	ps.mu.RUnlock()
	if approval == nil {
		return errors.New("conversion: external approval verifier is unavailable")
	}
	if err := approval.VerifyExternalApproval(intent, token); err != nil {
		return fmt.Errorf("conversion: external approval denied: %w", err)
	}
	return nil
}

func (ps *PreviewService) commitConversion(
	ctx context.Context,
	input RequestArtifactConversionInput,
	receipt *conversionReceipt,
	cacheKey string,
	preview *ArtifactPreview,
) error {
	ps.mu.RLock()
	resolver := ps.sources
	hook := ps.beforeCommit
	ps.mu.RUnlock()
	validator, ok := resolver.(ArtifactSourceCommitValidator)
	if !ok {
		return errors.New("conversion: source resolver cannot validate final commit")
	}
	if hook != nil {
		hook()
	}
	identity := ArtifactSourceRequest{
		WorkID:             input.WorkID,
		DefinitionRevision: input.DefinitionRevision,
		SlotID:             input.SlotID,
		SlotRevision:       input.SlotRevision,
		ArtifactRefID:      input.ArtifactRefID,
	}
	_, err := ps.state.CommitConversionResult(input.WorkID, conversionCommit{
		Identity:      identity,
		RequestID:     input.RequestID,
		IntentDigest:  receipt.IntentDigest,
		ContentDigest: receipt.ContentDigest,
		LeaseOwner:    receipt.LeaseOwner,
		CacheKey:      cacheKey,
		Preview:       preview,
		Validate: func(ref ArtifactRef) error {
			return validator.ValidateArtifactSource(ctx, input.WorkID, ref, receipt.ContentDigest)
		},
	})
	if err == nil {
		ps.asyncErrs.Delete(conversionAsyncKey(input.WorkID, input.RequestID))
	}
	return err
}

func (ps *PreviewService) resultFromReceipt(
	input RequestArtifactConversionInput,
	plan previewPlan,
	receipt *conversionReceipt,
	result *RequestArtifactConversionResult,
) (*RequestArtifactConversionResult, error) {
	if receipt.ResultDigest != "" {
		cached, found, err := ps.state.LoadPreviewCache(input.WorkID, receipt.ResultDigest)
		if err != nil {
			result.TransportError = TransportErrorFrom(err)
			result.Committed = true
			result.Recoverable = true
			return result, err
		}
		if found {
			cached.ConversionState = ConversionCompleted
			result.Preview = cached
			result.Committed = true
			result.Recoverable = false
			return result, nil
		}
	}
	// The receipt committed but its disposable derived cache is gone. Return it
	// to pending without consuming a retry; the cache can be rebuilt safely.
	_, _, err := ps.state.MutateConversionReceipt(input.WorkID, input.RequestID, func(current *conversionReceipt, found bool) (*conversionReceipt, error) {
		if !found || current.IntentDigest != receipt.IntentDigest {
			return nil, errors.New("conversion: receipt changed during cache recovery")
		}
		next := *current
		next.State = ConversionPending
		next.ResultDigest = ""
		next.Error = ""
		next.LeaseOwner = ""
		next.LeaseUntil = time.Time{}
		next.UpdatedAt = time.Now()
		return &next, nil
	})
	if err != nil {
		result.TransportError = TransportErrorFrom(err)
		result.Committed = true
		result.Recoverable = true
		return result, err
	}
	if ps.autoStart {
		go ps.executeConversion(input.WorkID, input.RequestID)
	}
	result.Preview = ps.pendingPreview(plan.ref, input.WorkID, plan)
	result.Committed = true
	result.Recoverable = true
	return result, nil
}

func previewFromReceipt(workID string, receipt *conversionReceipt) *ArtifactPreview {
	return &ArtifactPreview{
		ArtifactRefID:    receipt.ArtifactRefID,
		WorkID:           workID,
		ContentDigest:    receipt.ContentDigest,
		Grade:            PreviewFileCard,
		MimeType:         receipt.MimeType,
		CanOpen:          true,
		CanConvert:       receipt.ConverterName != "none",
		ConversionState:  receipt.State,
		ConverterVersion: receipt.ConverterVersion,
	}
}

func conversionAsyncKey(workID, requestID string) string {
	return workID + "\x00" + requestID
}

func (ps *PreviewService) attachAsyncError(workID, requestID string, result *RequestArtifactConversionResult) {
	if value, ok := ps.asyncErrs.Load(conversionAsyncKey(workID, requestID)); ok {
		if err, ok := value.(error); ok {
			result.TransportError = TransportErrorFrom(err)
		}
	}
}

func (ps *PreviewService) claimConversion(workID, requestID string) (*conversionReceipt, bool, error) {
	now := time.Now()
	claimed := false
	receipt, _, err := ps.state.MutateConversionReceipt(workID, requestID, func(current *conversionReceipt, found bool) (*conversionReceipt, error) {
		if !found {
			return nil, errors.New("conversion: pending receipt not found")
		}
		switch current.State {
		case ConversionPending:
		case ConversionRunning:
			if current.LeaseUntil.After(now) {
				return current, nil
			}
		default:
			return current, nil
		}
		next := *current
		next.State = ConversionRunning
		next.LeaseOwner = ps.ownerID
		next.LeaseUntil = now.Add(conversionLease)
		next.UpdatedAt = now
		claimed = true
		return &next, nil
	})
	return receipt, claimed, err
}

func (ps *PreviewService) heartbeatConversion(workID, requestID, intentDigest string, stop <-chan struct{}) {
	ticker := time.NewTicker(conversionLease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			_, _, err := ps.state.MutateConversionReceipt(workID, requestID, func(current *conversionReceipt, found bool) (*conversionReceipt, error) {
				if !found || current.State != ConversionRunning || current.IntentDigest != intentDigest || current.LeaseOwner != ps.ownerID {
					return current, nil
				}
				next := *current
				next.LeaseUntil = now.Add(conversionLease)
				next.UpdatedAt = now
				return &next, nil
			})
			if err != nil {
				return
			}
		}
	}
}

func (ps *PreviewService) failConversion(workID, requestID, intentDigest string, cause error) {
	_, _, err := ps.state.MutateConversionReceipt(workID, requestID, func(current *conversionReceipt, found bool) (*conversionReceipt, error) {
		if !found || current.IntentDigest != intentDigest || current.State != ConversionRunning || current.LeaseOwner != ps.ownerID {
			return current, nil
		}
		next := *current
		next.State = ConversionFailed
		next.Error = cause.Error()
		next.LeaseOwner = ""
		next.LeaseUntil = time.Time{}
		next.UpdatedAt = time.Now()
		return &next, nil
	})
	key := conversionAsyncKey(workID, requestID)
	if err != nil {
		ps.asyncErrs.Store(key, errors.Join(cause, err))
		return
	}
	ps.asyncErrs.Store(key, cause)
}

func snapshotPreviewSource(data []byte, name string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "work-preview-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("conversion: create source snapshot: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "artifact.bin"
	}
	targetPath := filepath.Join(dir, base)
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("conversion: create source snapshot: %w", err)
	}
	if _, err := target.Write(data); err != nil {
		_ = target.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("conversion: copy source snapshot: %w", err)
	}
	if err := target.Sync(); err != nil {
		_ = target.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("conversion: sync source snapshot: %w", err)
	}
	if err := target.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("conversion: close source snapshot: %w", err)
	}
	return targetPath, cleanup, nil
}

// ── Degradation ────────────────────────────────────────────────────────────

func (ps *PreviewService) degradedPreview(ref ArtifactRef, reason string) (*ArtifactPreview, error) {
	return &ArtifactPreview{ArtifactRefID: ref.ID, Grade: PreviewFallback, CanOpen: false, Error: reason}, nil
}

func (ps *PreviewService) fileCardPreview(ref ArtifactRef, grade PreviewGrade, mimeType string, size int64, contentDigest string) (*ArtifactPreview, error) {
	return &ArtifactPreview{
		ArtifactRefID: ref.ID, ContentDigest: contentDigest, Grade: grade, MimeType: mimeType,
		FileSize: size, Summary: officeSummary(ref.Name, mimeType, size),
		ThumbnailDataURL: officeThumbnailSVG(mimeType), CanOpen: true, CanConvert: true,
		ConversionState: ConversionIdle,
	}, nil
}

// ── Office summary / thumbnail ─────────────────────────────────────────────

func officeSummary(name, mimeType string, size int64) string {
	var kind string
	switch {
	case strings.Contains(mimeType, "wordprocessing") || hasExt(name, ".docx", ".doc"):
		kind = "Word 文档"
	case strings.Contains(mimeType, "spreadsheet") || hasExt(name, ".xlsx", ".xls"):
		kind = "Excel 表格"
	case strings.Contains(mimeType, "presentation") || hasExt(name, ".pptx", ".ppt"):
		kind = "PowerPoint 演示文稿"
	default:
		kind = "Office 文档"
	}
	return fmt.Sprintf("%s · %s — 点击「打开」使用系统程序查看，或请求转换为 PDF 预览。", kind, formatSize(size))
}

func hasExt(name string, exts ...string) bool {
	lower := strings.ToLower(name)
	for _, e := range exts {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	return false
}

func formatSize(size int64) string {
	switch {
	case size <= 0:
		return "未知大小"
	case size < 1024:
		return fmt.Sprintf("%d B", size)
	case size < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
}

func officeThumbnailSVG(mimeType string) string {
	var label, color string
	switch {
	case strings.Contains(mimeType, "wordprocessing") || strings.Contains(mimeType, "msword"):
		label, color = "DOC", "#2B579A"
	case strings.Contains(mimeType, "spreadsheet") || strings.Contains(mimeType, "excel"):
		label, color = "XLS", "#217346"
	case strings.Contains(mimeType, "presentation") || strings.Contains(mimeType, "powerpoint"):
		label, color = "PPT", "#D24726"
	default:
		label, color = "DOC", "#757575"
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64"><rect width="64" height="64" rx="6" fill="%s"/><text x="32" y="40" text-anchor="middle" fill="white" font-size="18" font-family="sans-serif" font-weight="bold">%s</text></svg>`, color, label)
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
}

// ── Converter lookup ───────────────────────────────────────────────────────

func (ps *PreviewService) findConverter(grade PreviewGrade, mimeType string, allowExternal bool) Converter {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	// Prefer a local converter even when external conversion is approved.
	for _, c := range ps.converters {
		if !converterIsExternal(c) && c.CanConvert(grade, mimeType) {
			return c
		}
	}
	if allowExternal {
		for _, c := range ps.converters {
			if converterIsExternal(c) && c.CanConvert(grade, mimeType) {
				return c
			}
		}
	}
	return nil
}

func (ps *PreviewService) findConverterByIdentity(identity ConverterIdentity, grade PreviewGrade, mimeType string) Converter {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	for _, converter := range ps.converters {
		if converter.Identity() == identity && converter.CanConvert(grade, mimeType) {
			return converter
		}
	}
	return nil
}

func converterIsExternal(converter Converter) bool {
	external, ok := converter.(ExternalConverter)
	return ok && external.External()
}

func builtinConverters() []Converter {
	return []Converter{&textConverter{}, &imageConverter{}, &pdfConverter{}}
}

// ── Converters ─────────────────────────────────────────────────────────────

type textConverter struct{}

func (c *textConverter) Identity() ConverterIdentity {
	return ConverterIdentity{Name: "text", Version: "1", Target: "inline-text"}
}
func (c *textConverter) CanConvert(grade PreviewGrade, mimeType string) bool {
	return grade == PreviewInline && (mimeType == "" || strings.HasPrefix(mimeType, "text/") || mimeType == "application/json" || mimeType == "application/xml" || strings.Contains(mimeType, "yaml"))
}
func (c *textConverter) Convert(workID, path, mimeType string) (*ArtifactPreview, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("text: open: %w", err)
	}
	defer f.Close()
	info, _ := f.Stat()
	if !info.Mode().IsRegular() {
		return nil, errors.New("text: not a regular file")
	}
	raw, _ := io.ReadAll(io.LimitReader(f, int64(maxInlineTextBytes+1)))
	if len(raw) > maxInlineTextBytes {
		raw = raw[:maxInlineTextBytes]
	}
	return &ArtifactPreview{TextContent: strings.ToValidUTF8(string(raw), string(utf8.RuneError)), FileSize: info.Size()}, nil
}

const maxInlineTextBytes = 256 * 1024

type imageConverter struct{}

func (c *imageConverter) Identity() ConverterIdentity {
	return ConverterIdentity{Name: "image", Version: "1", Target: "data-url"}
}
func (c *imageConverter) CanConvert(grade PreviewGrade, mimeType string) bool {
	return grade == PreviewInline && (mimeType == "" || strings.HasPrefix(mimeType, "image/"))
}
func (c *imageConverter) Convert(workID, path, mimeType string) (*ArtifactPreview, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("image: lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("image: symlinks not supported")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxImagePreviewBytes {
		return nil, fmt.Errorf("image: size %d", info.Size())
	}
	f, _ := os.Open(path)
	defer f.Close()
	raw, _ := io.ReadAll(io.LimitReader(f, maxImagePreviewBytes+1))
	if len(raw) == 0 || len(raw) > maxImagePreviewBytes {
		return nil, fmt.Errorf("image: size %d", len(raw))
	}
	mime := http.DetectContentType(raw)
	if !strings.HasPrefix(mime, "image/") {
		return nil, fmt.Errorf("image: not image")
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("image: decode: %w", err)
	}
	return &ArtifactPreview{DataURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), FileSize: info.Size()}, nil
}

const maxImagePreviewBytes = 10 * 1024 * 1024

type pdfConverter struct{}

func (c *pdfConverter) Identity() ConverterIdentity {
	return ConverterIdentity{Name: "pdf", Version: "1", Target: "pdf-data-url"}
}
func (c *pdfConverter) CanConvert(grade PreviewGrade, mimeType string) bool {
	return grade == PreviewInline && mimeType == "application/pdf"
}
func (c *pdfConverter) Convert(workID, path, mimeType string) (*ArtifactPreview, error) {
	info, _ := os.Lstat(path)
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("pdf: symlinks not allowed")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPDFPreviewBytes {
		return nil, fmt.Errorf("pdf: size %d", info.Size())
	}
	f, _ := os.Open(path)
	defer f.Close()
	raw, _ := io.ReadAll(io.LimitReader(f, maxPDFPreviewBytes+1))
	if len(raw) > maxPDFPreviewBytes {
		return nil, fmt.Errorf("pdf: too large")
	}
	if len(raw) < 5 || string(raw[:5]) != "%PDF-" {
		return nil, errors.New("pdf: invalid")
	}
	return &ArtifactPreview{PDFRaw: "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(raw), FileSize: info.Size()}, nil
}

const maxPDFPreviewBytes = 20 * 1024 * 1024

// ── Utility ────────────────────────────────────────────────────────────────

func fileContentDigest(path string, info os.FileInfo) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return digestPrefix + fmt.Sprintf("%x", h.Sum(nil)), nil
}
