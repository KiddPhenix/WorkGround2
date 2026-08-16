package decision

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/fileutil"
)

var (
	ErrNotFound          = errors.New("decision not found")
	ErrNotPresented      = errors.New("decision is not presented")
	ErrInvalidTransition = errors.New("invalid decision transition")
)

type Broker struct {
	mu          sync.Mutex
	path        string
	now         func() time.Time
	state       Snapshot
	subscribers map[int]chan Change
	nextSubID   int
}

func Open(path string) (*Broker, error) {
	b := &Broker{
		path:        strings.TrimSpace(path),
		now:         time.Now,
		subscribers: make(map[int]chan Change),
		state: Snapshot{
			Version:  SchemaVersion,
			Settings: Settings{ExternalMode: ExternalSmart, SmartGrace: 30 * time.Second},
		},
	}
	if b.path == "" {
		return b, nil
	}
	raw, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil
		}
		return nil, fmt.Errorf("open decision broker: %w", err)
	}
	if err := json.Unmarshal(raw, &b.state); err != nil {
		return nil, fmt.Errorf("decode decision broker: %w", err)
	}
	if b.state.Version > SchemaVersion {
		return nil, fmt.Errorf("decision broker schema %d is newer than supported %d", b.state.Version, SchemaVersion)
	}
	b.state.Version = SchemaVersion
	b.normalizeLocked()
	if err := b.saveLocked(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Broker) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneSnapshot(b.state)
}

func (b *Broker) Get(id string) (Decision, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := b.findDecisionLocked(id)
	if idx < 0 {
		return Decision{}, false
	}
	return cloneDecision(b.state.Decisions[idx]), true
}

func (b *Broker) Active() (Decision, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := b.activeIndexLocked()
	if idx < 0 {
		return Decision{}, false
	}
	return cloneDecision(b.state.Decisions[idx]), true
}

func (b *Broker) List(filter ListFilter) []Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	statuses := make(map[Status]bool, len(filter.Statuses))
	for _, status := range filter.Statuses {
		statuses[status] = true
	}
	out := make([]Decision, 0, len(b.state.Decisions))
	for _, d := range b.state.Decisions {
		if len(statuses) > 0 && !statuses[d.Status] {
			continue
		}
		if filter.Origin != nil && !originMatches(d.Origin, *filter.Origin) {
			continue
		}
		out = append(out, cloneDecision(d))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].QueueSeq < out[j].QueueSeq })
	return out
}

func (b *Broker) Create(req CreateRequest) (CreateResult, error) {
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.IdempotencyKey == "" {
		return CreateResult{}, errors.New("decision idempotency key is required")
	}
	if err := validatePresentation(req.Presentation); err != nil {
		return CreateResult{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, existing := range b.state.Decisions {
		if existing.IdempotencyKey == req.IdempotencyKey {
			return CreateResult{Decision: cloneDecision(existing), Duplicate: true}, nil
		}
	}
	before := cloneSnapshot(b.state)
	now := b.now().UTC()
	id, err := newID("D")
	if err != nil {
		return CreateResult{}, err
	}
	b.state.NextQueueSeq++
	status := StatusQueued
	var presentedAt *time.Time
	if b.activeIndexLocked() < 0 {
		status = StatusPresented
		presentedAt = timePtr(now)
	}
	d := Decision{
		ID:             id,
		IdempotencyKey: req.IdempotencyKey,
		Kind:           firstNonEmpty(strings.TrimSpace(req.Kind), "ask"),
		Origin:         req.Origin,
		Presentation:   normalizePresentation(req.Presentation),
		Status:         status,
		Revision:       1,
		QueueSeq:       b.state.NextQueueSeq,
		CreatedAt:      now,
		PresentedAt:    presentedAt,
		BusinessDueAt:  cloneTime(req.BusinessDueAt),
		StaleAfter:     cloneTime(req.StaleAfter),
	}
	b.state.Decisions = append(b.state.Decisions, d)
	b.bumpLocked(now)
	b.auditLocked(d.ID, "created", d.Origin.Kind, now)
	if err := b.saveLocked(); err != nil {
		b.state = before
		return CreateResult{}, err
	}
	change := Change{Revision: b.state.Revision, Kind: "created", Decision: cloneDecision(d)}
	b.publishLocked(change)
	return CreateResult{Decision: cloneDecision(d)}, nil
}

func (b *Broker) Resolve(id string, answer Answer, responder Responder) (ResolveResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := b.findDecisionLocked(id)
	if idx < 0 {
		return ResolveResult{}, ErrNotFound
	}
	current := b.state.Decisions[idx]
	if current.Status == StatusDecided || current.Status == StatusApplied || current.Status == StatusApplyFailed {
		return ResolveResult{Decision: cloneDecision(current), AlreadyResolved: true}, nil
	}
	if current.Status != StatusPresented && current.Status != StatusDeferred {
		return ResolveResult{}, ErrNotPresented
	}
	answer = normalizeAnswer(answer)
	if err := validateAnswer(current.Presentation, answer); err != nil {
		return ResolveResult{}, err
	}
	before := cloneSnapshot(b.state)
	now := b.now().UTC()
	current.Status = StatusDecided
	current.Answer = &answer
	responder.Kind = strings.TrimSpace(responder.Kind)
	responder.ID = strings.TrimSpace(responder.ID)
	responder.Label = strings.TrimSpace(responder.Label)
	responder.EndpointID = strings.TrimSpace(responder.EndpointID)
	current.Responder = &responder
	current.DecidedAt = timePtr(now)
	current.LastError = ""
	current.Revision++
	b.state.Decisions[idx] = current
	promoted := b.promoteLocked(now)
	b.bumpLocked(now)
	b.auditLocked(current.ID, "resolved", responder.Label, now)
	if err := b.saveLocked(); err != nil {
		b.state = before
		return ResolveResult{}, err
	}
	change := Change{Revision: b.state.Revision, Kind: "resolved", Decision: cloneDecision(current), Promoted: cloneDecisionPtr(promoted)}
	b.publishLocked(change)
	return ResolveResult{Decision: cloneDecision(current), Promoted: cloneDecisionPtr(promoted)}, nil
}

func (b *Broker) Defer(id string) (Transition, error) {
	return b.transitionOpen(id, "deferred", func(d *Decision, now time.Time) error {
		if d.Status != StatusPresented {
			return ErrNotPresented
		}
		d.Status = StatusDeferred
		d.Revision++
		return nil
	})
}

func (b *Broker) Resume(id string) (Transition, error) {
	return b.transitionOpen(id, "resumed", func(d *Decision, now time.Time) error {
		if d.Status != StatusDeferred {
			return ErrInvalidTransition
		}
		d.Status = StatusQueued
		d.Revision++
		return nil
	})
}

func (b *Broker) Cancel(id string) (Transition, error) {
	return b.transitionOpen(id, "cancelled", func(d *Decision, now time.Time) error {
		switch d.Status {
		case StatusQueued, StatusPresented, StatusDeferred, StatusApplyFailed:
			d.Status = StatusCancelled
			d.Revision++
			return nil
		default:
			return ErrInvalidTransition
		}
	})
}

func (b *Broker) MarkApplied(id string) (Decision, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := b.findDecisionLocked(id)
	if idx < 0 {
		return Decision{}, ErrNotFound
	}
	if b.state.Decisions[idx].Status == StatusApplied {
		return cloneDecision(b.state.Decisions[idx]), nil
	}
	if b.state.Decisions[idx].Status != StatusDecided && b.state.Decisions[idx].Status != StatusApplyFailed {
		return Decision{}, ErrInvalidTransition
	}
	before := cloneSnapshot(b.state)
	now := b.now().UTC()
	d := b.state.Decisions[idx]
	d.Status = StatusApplied
	d.AppliedAt = timePtr(now)
	d.LastError = ""
	d.Revision++
	b.state.Decisions[idx] = d
	b.bumpLocked(now)
	b.auditLocked(d.ID, "applied", "", now)
	if err := b.saveLocked(); err != nil {
		b.state = before
		return Decision{}, err
	}
	b.publishLocked(Change{Revision: b.state.Revision, Kind: "applied", Decision: cloneDecision(d)})
	return cloneDecision(d), nil
}

func (b *Broker) MarkApplyFailed(id string, applyErr error) (Decision, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := b.findDecisionLocked(id)
	if idx < 0 {
		return Decision{}, ErrNotFound
	}
	if b.state.Decisions[idx].Status != StatusDecided && b.state.Decisions[idx].Status != StatusApplyFailed {
		return Decision{}, ErrInvalidTransition
	}
	before := cloneSnapshot(b.state)
	now := b.now().UTC()
	d := b.state.Decisions[idx]
	d.Status = StatusApplyFailed
	if applyErr != nil {
		d.LastError = applyErr.Error()
	}
	d.Revision++
	b.state.Decisions[idx] = d
	b.bumpLocked(now)
	b.auditLocked(d.ID, "apply_failed", d.LastError, now)
	if err := b.saveLocked(); err != nil {
		b.state = before
		return Decision{}, err
	}
	b.publishLocked(Change{Revision: b.state.Revision, Kind: "apply_failed", Decision: cloneDecision(d)})
	return cloneDecision(d), nil
}

func (b *Broker) Settings() Settings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneSettings(b.state.Settings)
}

func (b *Broker) SetSettings(settings Settings) (Settings, error) {
	settings.ExternalMode = normalizeExternalMode(settings.ExternalMode)
	if settings.SmartGrace < 0 {
		return Settings{}, errors.New("smart grace cannot be negative")
	}
	if settings.SmartGrace == 0 {
		settings.SmartGrace = 30 * time.Second
	}
	settings.LocalOnlyUntil = cloneTime(settings.LocalOnlyUntil)
	b.mu.Lock()
	defer b.mu.Unlock()
	before := cloneSnapshot(b.state)
	b.state.Settings = settings
	b.bumpLocked(b.now().UTC())
	if err := b.saveLocked(); err != nil {
		b.state = before
		return Settings{}, err
	}
	return cloneSettings(settings), nil
}

func (b *Broker) ExternalDeliveryEnabled(now time.Time, desktopActive bool, presentedAt *time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	settings := b.state.Settings
	switch normalizeExternalMode(settings.ExternalMode) {
	case ExternalOff:
		return false
	case ExternalLocalOnly:
		return settings.LocalOnlyUntil != nil && !now.Before(*settings.LocalOnlyUntil)
	case ExternalAlways:
		return true
	default:
		if !desktopActive {
			return true
		}
		if presentedAt == nil {
			return false
		}
		return !now.Before(presentedAt.Add(settings.SmartGrace))
	}
}

func (b *Broker) Channels() []Channel {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]Channel(nil), b.state.Channels...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (b *Broker) UpsertChannel(channel Channel) (Channel, error) {
	channel.ID = strings.TrimSpace(channel.ID)
	channel.Name = strings.TrimSpace(channel.Name)
	channel.Kind = strings.ToLower(strings.TrimSpace(channel.Kind))
	channel.ConnectionID = strings.TrimSpace(channel.ConnectionID)
	channel.Domain = strings.TrimSpace(channel.Domain)
	channel.ChatID = strings.TrimSpace(channel.ChatID)
	channel.ChatType = strings.TrimSpace(channel.ChatType)
	if channel.ID == "" {
		id, err := newID("CH")
		if err != nil {
			return Channel{}, err
		}
		channel.ID = id
	}
	if channel.Name == "" || channel.Kind == "" {
		return Channel{}, errors.New("decision channel name and kind are required")
	}
	if channel.Kind != "desktop" && channel.ChatID == "" {
		return Channel{}, errors.New("external decision channel chat id is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	before := cloneSnapshot(b.state)
	updated := false
	for i := range b.state.Channels {
		if b.state.Channels[i].ID == channel.ID {
			b.state.Channels[i] = channel
			updated = true
			break
		}
	}
	if !updated {
		b.state.Channels = append(b.state.Channels, channel)
	}
	b.bumpLocked(b.now().UTC())
	if err := b.saveLocked(); err != nil {
		b.state = before
		return Channel{}, err
	}
	return channel, nil
}

func (b *Broker) DeleteChannel(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	id = strings.TrimSpace(id)
	before := cloneSnapshot(b.state)
	next := b.state.Channels[:0]
	found := false
	for _, channel := range b.state.Channels {
		if channel.ID == id {
			found = true
			continue
		}
		next = append(next, channel)
	}
	if !found {
		return ErrNotFound
	}
	b.state.Channels = next
	b.bumpLocked(b.now().UTC())
	if err := b.saveLocked(); err != nil {
		b.state = before
		return err
	}
	return nil
}

func (b *Broker) EnqueueDelivery(endpointID, decisionID string, event DeliveryEvent) (Delivery, bool, error) {
	endpointID = strings.TrimSpace(endpointID)
	decisionID = strings.TrimSpace(decisionID)
	if endpointID == "" || decisionID == "" {
		return Delivery{}, false, errors.New("delivery endpoint and decision are required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.findDecisionLocked(decisionID) < 0 {
		return Delivery{}, false, ErrNotFound
	}
	for _, existing := range b.state.Deliveries {
		if existing.EndpointID == endpointID && existing.DecisionID == decisionID && existing.Event == event {
			return existing, true, nil
		}
	}
	before := cloneSnapshot(b.state)
	now := b.now().UTC()
	seq := int64(1)
	for _, delivery := range b.state.Deliveries {
		if delivery.EndpointID == endpointID && delivery.Sequence >= seq {
			seq = delivery.Sequence + 1
		}
	}
	id, err := newID("DL")
	if err != nil {
		return Delivery{}, false, err
	}
	delivery := Delivery{ID: id, DecisionID: decisionID, EndpointID: endpointID, Sequence: seq, Event: event, Status: DeliveryPending, CreatedAt: now, UpdatedAt: now}
	b.state.Deliveries = append(b.state.Deliveries, delivery)
	b.bumpLocked(now)
	b.auditLocked(decisionID, "delivery_queued", endpointID+":"+string(event), now)
	if err := b.saveLocked(); err != nil {
		b.state = before
		return Delivery{}, false, err
	}
	return delivery, false, nil
}

func (b *Broker) NextDelivery(endpointID string, now time.Time) (Delivery, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var candidates []Delivery
	for _, delivery := range b.state.Deliveries {
		if delivery.EndpointID != strings.TrimSpace(endpointID) {
			continue
		}
		if delivery.Status != DeliveryPending && delivery.Status != DeliveryFailed {
			continue
		}
		if !delivery.NextRetryAt.IsZero() && now.Before(delivery.NextRetryAt) {
			continue
		}
		candidates = append(candidates, delivery)
	}
	if len(candidates) == 0 {
		return Delivery{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Sequence < candidates[j].Sequence })
	return candidates[0], true
}

func (b *Broker) CompleteDelivery(id, remoteMessage string, sendErr error, retryAt time.Time) (Delivery, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := -1
	for i := range b.state.Deliveries {
		if b.state.Deliveries[i].ID == strings.TrimSpace(id) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Delivery{}, ErrNotFound
	}
	before := cloneSnapshot(b.state)
	now := b.now().UTC()
	d := b.state.Deliveries[idx]
	d.Attempts++
	d.UpdatedAt = now
	if sendErr != nil {
		d.Status = DeliveryFailed
		d.LastError = sendErr.Error()
		d.NextRetryAt = retryAt.UTC()
	} else {
		d.Status = DeliverySent
		d.LastError = ""
		d.NextRetryAt = time.Time{}
		d.RemoteMessage = strings.TrimSpace(remoteMessage)
	}
	b.state.Deliveries[idx] = d
	b.bumpLocked(now)
	auditKind := "delivery_sent"
	if sendErr != nil {
		auditKind = "delivery_failed"
	}
	b.auditLocked(d.DecisionID, auditKind, d.EndpointID+":"+d.LastError, now)
	if err := b.saveLocked(); err != nil {
		b.state = before
		return Delivery{}, err
	}
	return d, nil
}

func (b *Broker) Subscribe(buffer int) (<-chan Change, func()) {
	if buffer <= 0 {
		buffer = 16
	}
	b.mu.Lock()
	id := b.nextSubID
	b.nextSubID++
	ch := make(chan Change, buffer)
	b.subscribers[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			close(ch)
			b.mu.Unlock()
		})
	}
}

func (b *Broker) transitionOpen(id, kind string, change func(*Decision, time.Time) error) (Transition, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := b.findDecisionLocked(id)
	if idx < 0 {
		return Transition{}, ErrNotFound
	}
	before := cloneSnapshot(b.state)
	now := b.now().UTC()
	d := b.state.Decisions[idx]
	if err := change(&d, now); err != nil {
		return Transition{}, err
	}
	b.state.Decisions[idx] = d
	promoted := b.promoteLocked(now)
	b.bumpLocked(now)
	b.auditLocked(d.ID, kind, "", now)
	if err := b.saveLocked(); err != nil {
		b.state = before
		return Transition{}, err
	}
	transition := Transition{Decision: cloneDecision(d), Promoted: cloneDecisionPtr(promoted)}
	b.publishLocked(Change{Revision: b.state.Revision, Kind: kind, Decision: transition.Decision, Promoted: transition.Promoted})
	return transition, nil
}

func (b *Broker) normalizeLocked() {
	if b.state.Settings.ExternalMode == "" {
		b.state.Settings.ExternalMode = ExternalSmart
	}
	if b.state.Settings.SmartGrace <= 0 {
		b.state.Settings.SmartGrace = 30 * time.Second
	}
	var active = -1
	for i := range b.state.Decisions {
		d := &b.state.Decisions[i]
		if d.QueueSeq > b.state.NextQueueSeq {
			b.state.NextQueueSeq = d.QueueSeq
		}
		if d.Status != StatusPresented {
			continue
		}
		if active < 0 || d.QueueSeq < b.state.Decisions[active].QueueSeq {
			if active >= 0 {
				b.state.Decisions[active].Status = StatusQueued
				b.state.Decisions[active].PresentedAt = nil
			}
			active = i
			continue
		}
		d.Status = StatusQueued
		d.PresentedAt = nil
	}
	if active < 0 {
		b.promoteLocked(b.now().UTC())
	}
}

func (b *Broker) promoteLocked(now time.Time) *Decision {
	if b.activeIndexLocked() >= 0 {
		return nil
	}
	idx := -1
	for i := range b.state.Decisions {
		if b.state.Decisions[i].Status != StatusQueued {
			continue
		}
		if idx < 0 || b.state.Decisions[i].QueueSeq < b.state.Decisions[idx].QueueSeq {
			idx = i
		}
	}
	if idx < 0 {
		return nil
	}
	d := &b.state.Decisions[idx]
	d.Status = StatusPresented
	d.PresentedAt = timePtr(now)
	d.Revision++
	copy := cloneDecision(*d)
	return &copy
}

func (b *Broker) findDecisionLocked(id string) int {
	id = strings.TrimSpace(id)
	for i := range b.state.Decisions {
		if b.state.Decisions[i].ID == id {
			return i
		}
	}
	return -1
}

func (b *Broker) activeIndexLocked() int {
	for i := range b.state.Decisions {
		if b.state.Decisions[i].Status == StatusPresented {
			return i
		}
	}
	return -1
}

func (b *Broker) bumpLocked(now time.Time) {
	b.state.Version = SchemaVersion
	b.state.Revision++
	b.state.UpdatedAt = now
}

func (b *Broker) auditLocked(decisionID, kind, detail string, now time.Time) {
	const maxAuditEntries = 10000
	entry := AuditEntry{
		ID:         fmt.Sprintf("A-%d-%d", b.state.Revision, len(b.state.Audit)+1),
		DecisionID: strings.TrimSpace(decisionID),
		Kind:       strings.TrimSpace(kind),
		Revision:   b.state.Revision,
		Detail:     strings.TrimSpace(detail),
		At:         now.UTC(),
	}
	b.state.Audit = append(b.state.Audit, entry)
	if len(b.state.Audit) > maxAuditEntries {
		b.state.Audit = append([]AuditEntry(nil), b.state.Audit[len(b.state.Audit)-maxAuditEntries:]...)
	}
}

func (b *Broker) saveLocked() error {
	if b.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(b.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode decision broker: %w", err)
	}
	raw = append(raw, '\n')
	if err := fileutil.AtomicWriteFile(b.path, raw, 0o600); err != nil {
		return fmt.Errorf("save decision broker: %w", err)
	}
	return nil
}

func (b *Broker) publishLocked(change Change) {
	for _, ch := range b.subscribers {
		select {
		case ch <- change:
		default:
		}
	}
}

func validatePresentation(p Presentation) error {
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.TaskSummary) == "" || strings.TrimSpace(p.WhyNow) == "" || strings.TrimSpace(p.NoAnswerPolicy) == "" {
		return errors.New("insufficient_context: title, task_summary, why_now and no_answer_policy are required")
	}
	if len(p.Questions) == 0 || len(p.Questions) > 4 {
		return errors.New("decision requires 1-4 questions")
	}
	for i, question := range p.Questions {
		if strings.TrimSpace(question.Prompt) == "" || len(question.Options) < 2 || len(question.Options) > 4 {
			return fmt.Errorf("question %d requires a prompt and 2-4 options", i+1)
		}
		seen := make(map[string]bool, len(question.Options))
		for j, option := range question.Options {
			label := strings.TrimSpace(option.Label)
			if label == "" || strings.TrimSpace(option.Impact) == "" {
				return fmt.Errorf("question %d option %d requires label and impact", i+1, j+1)
			}
			if seen[label] {
				return fmt.Errorf("question %d has duplicate option %q", i+1, label)
			}
			seen[label] = true
		}
	}
	if p.Recommendation != nil {
		if strings.TrimSpace(p.Recommendation.Reason) == "" || strings.TrimSpace(p.Recommendation.Option) == "" {
			return errors.New("recommendation option and reason are required")
		}
		if _, ok := recommendationQuestion(p, p.Recommendation.QuestionID); !ok {
			return errors.New("recommendation question was not found")
		}
	}
	return nil
}

func validateAnswer(p Presentation, answer Answer) error {
	byID := make(map[string]Selection, len(answer.Selections))
	for _, selection := range answer.Selections {
		if _, exists := byID[selection.QuestionID]; exists {
			return fmt.Errorf("duplicate answer for question %s", selection.QuestionID)
		}
		byID[selection.QuestionID] = selection
	}
	for _, question := range p.Questions {
		selection, ok := byID[question.ID]
		if !ok || len(selection.Selected) == 0 {
			return fmt.Errorf("question %s requires an answer", question.ID)
		}
		if !question.MultiSelect && len(selection.Selected) != 1 {
			return fmt.Errorf("question %s accepts one option", question.ID)
		}
		valid := make(map[string]bool, len(question.Options))
		for _, option := range question.Options {
			valid[option.Label] = true
		}
		for _, selected := range selection.Selected {
			if !valid[selected] {
				return fmt.Errorf("question %s has unknown option %q", question.ID, selected)
			}
		}
	}
	return nil
}

func normalizePresentation(p Presentation) Presentation {
	p.Title = strings.TrimSpace(p.Title)
	p.TaskSummary = strings.TrimSpace(p.TaskSummary)
	p.WhyNow = strings.TrimSpace(p.WhyNow)
	p.NoAnswerPolicy = strings.TrimSpace(p.NoAnswerPolicy)
	for i := range p.Questions {
		q := &p.Questions[i]
		q.ID = strings.TrimSpace(q.ID)
		if q.ID == "" {
			q.ID = fmt.Sprintf("q%d", i+1)
		}
		q.Header = strings.TrimSpace(q.Header)
		q.Prompt = strings.TrimSpace(q.Prompt)
		for j := range q.Options {
			q.Options[j].Label = strings.TrimSpace(q.Options[j].Label)
			q.Options[j].Impact = strings.TrimSpace(q.Options[j].Impact)
		}
	}
	if p.Recommendation != nil {
		p.Recommendation.QuestionID = strings.TrimSpace(p.Recommendation.QuestionID)
		if p.Recommendation.QuestionID == "" && len(p.Questions) == 1 {
			p.Recommendation.QuestionID = p.Questions[0].ID
		}
		p.Recommendation.Option = strings.TrimSpace(p.Recommendation.Option)
		p.Recommendation.Reason = strings.TrimSpace(p.Recommendation.Reason)
	}
	return p
}

func normalizeAnswer(answer Answer) Answer {
	for i := range answer.Selections {
		answer.Selections[i].QuestionID = strings.TrimSpace(answer.Selections[i].QuestionID)
		for j := range answer.Selections[i].Selected {
			answer.Selections[i].Selected[j] = strings.TrimSpace(answer.Selections[i].Selected[j])
		}
	}
	return answer
}

func recommendationQuestion(p Presentation, id string) (Question, bool) {
	id = strings.TrimSpace(id)
	if id == "" && len(p.Questions) == 1 {
		id = p.Questions[0].ID
	}
	for _, question := range p.Questions {
		if question.ID != id {
			continue
		}
		for _, option := range question.Options {
			if option.Label == strings.TrimSpace(p.Recommendation.Option) {
				return question, true
			}
		}
		return Question{}, false
	}
	return Question{}, false
}

func normalizeExternalMode(mode ExternalMode) ExternalMode {
	switch mode {
	case ExternalAlways, ExternalLocalOnly, ExternalOff:
		return mode
	default:
		return ExternalSmart
	}
}

func originMatches(value, want Origin) bool {
	if want.Kind != "" && value.Kind != want.Kind {
		return false
	}
	if want.AgentID != "" && value.AgentID != want.AgentID {
		return false
	}
	if want.ThreadID != "" && value.ThreadID != want.ThreadID {
		return false
	}
	if want.SessionID != "" && value.SessionID != want.SessionID {
		return false
	}
	if want.SessionPath != "" && value.SessionPath != want.SessionPath {
		return false
	}
	return want.WorkspaceRoot == "" || value.WorkspaceRoot == want.WorkspaceRoot
}

func newID(prefix string) (string, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate decision id: %w", err)
	}
	return prefix + "-" + strings.TrimRight(base32.StdEncoding.EncodeToString(raw[:]), "="), nil
}

func cloneSnapshot(in Snapshot) Snapshot {
	raw, _ := json.Marshal(in)
	var out Snapshot
	_ = json.Unmarshal(raw, &out)
	return out
}

func cloneDecision(in Decision) Decision {
	raw, _ := json.Marshal(in)
	var out Decision
	_ = json.Unmarshal(raw, &out)
	return out
}

func cloneDecisionPtr(in *Decision) *Decision {
	if in == nil {
		return nil
	}
	out := cloneDecision(*in)
	return &out
}

func cloneSettings(in Settings) Settings {
	in.LocalOnlyUntil = cloneTime(in.LocalOnlyUntil)
	return in
}

func cloneTime(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func timePtr(value time.Time) *time.Time { return &value }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
