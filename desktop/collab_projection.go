package main

import (
	"encoding/json"
	"fmt"
	"sort"

	"workground2/internal/collab"
)

func projectCollaborationEvents(base collab.Snapshot, events []collab.RoomEvent) (collab.Snapshot, error) {
	next := cloneCollaborationState(CollaborationState{Snapshot: base}).Snapshot
	ordered := append([]collab.RoomEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	for _, event := range ordered {
		if event.Room != next.Room.ID {
			return collab.Snapshot{}, fmt.Errorf("collaboration event room mismatch: got %q, want %q", event.Room, next.Room.ID)
		}
		if event.Sequence <= next.LatestSequence {
			continue
		}
		if event.Sequence != next.LatestSequence+1 {
			return collab.Snapshot{}, fmt.Errorf("collaboration event gap after %d: got %d", next.LatestSequence, event.Sequence)
		}
		if err := projectCollaborationEvent(&next, event); err != nil {
			return collab.Snapshot{}, err
		}
		next.LatestSequence = event.Sequence
		next.Room.LatestSequence = event.Sequence
	}
	return next, nil
}

func projectCollaborationEvent(snapshot *collab.Snapshot, event collab.RoomEvent) error {
	switch event.Type {
	case "room.created":
		var room collab.Room
		if err := json.Unmarshal(event.Payload, &room); err != nil || room.ID != event.Room {
			return fmt.Errorf("project collaboration room event %d: invalid payload", event.Sequence)
		}
		snapshot.Room = room
		return nil
	case "member.online", "member.left":
		var member collab.Member
		if err := json.Unmarshal(event.Payload, &member); err != nil || member.ID == "" {
			return fmt.Errorf("project collaboration member event %d: invalid payload", event.Sequence)
		}
		upsertCollaborationMember(snapshot, member)
		return nil
	case "members.stale":
		var value struct {
			MemberIDs []string `json:"memberIds"`
		}
		if err := json.Unmarshal(event.Payload, &value); err != nil || len(value.MemberIDs) == 0 {
			return fmt.Errorf("project collaboration stale event %d: invalid payload", event.Sequence)
		}
		ids := make(map[string]struct{}, len(value.MemberIDs))
		for _, id := range value.MemberIDs {
			ids[id] = struct{}{}
		}
		for i := range snapshot.Members {
			if _, ok := ids[snapshot.Members[i].ID]; ok {
				snapshot.Members[i].Status = collab.MemberOffline
				snapshot.Members[i].Agent.Status = collab.AgentOffline
				delete(ids, snapshot.Members[i].ID)
			}
		}
		if len(ids) != 0 {
			return fmt.Errorf("project collaboration stale event %d: member is missing", event.Sequence)
		}
		return nil
	case "member.joined", "member.rejoined", "agent.updated":
		// Journals created before chunked snapshots do not carry the changed
		// Member projection in these event payloads. Force a bounded Snapshot
		// refresh instead of guessing profile or roster state.
		return fmt.Errorf("collaboration event %q requires authoritative snapshot", event.Type)
	default:
		var item collab.TimelineItem
		if err := json.Unmarshal(event.Payload, &item); err != nil || item.ID == "" || item.Sequence != event.Sequence || item.Type == "" {
			return fmt.Errorf("project collaboration timeline event %d: invalid payload", event.Sequence)
		}
		upsertCollaborationTimeline(snapshot, item)
		if item.AgentRun != nil {
			projectCollaborationAgentStatus(snapshot, *item.AgentRun)
		}
		return nil
	}
}

func upsertCollaborationMember(snapshot *collab.Snapshot, member collab.Member) {
	for i := range snapshot.Members {
		if snapshot.Members[i].ID == member.ID {
			snapshot.Members[i] = member
			return
		}
	}
	snapshot.Members = append(snapshot.Members, member)
	sort.SliceStable(snapshot.Members, func(i, j int) bool {
		return snapshot.Members[i].JoinedAt.Before(snapshot.Members[j].JoinedAt) || snapshot.Members[i].JoinedAt.Equal(snapshot.Members[j].JoinedAt) && snapshot.Members[i].ID < snapshot.Members[j].ID
	})
}

func upsertCollaborationTimeline(snapshot *collab.Snapshot, item collab.TimelineItem) {
	if item.Type == collab.TimelineAgentRequest || item.Type == collab.TimelineAgentRun || item.Type == collab.TimelineAgentResult || item.Type == collab.TimelineFile {
		for i := range snapshot.Timeline {
			if snapshot.Timeline[i].ID == item.ID {
				snapshot.Timeline[i] = item
				return
			}
		}
	}
	snapshot.Timeline = append(snapshot.Timeline, item)
}

func projectCollaborationAgentStatus(snapshot *collab.Snapshot, changed collab.AgentRun) {
	memberIndex := -1
	for i := range snapshot.Members {
		if snapshot.Members[i].ID == changed.OwnerID {
			memberIndex = i
			break
		}
	}
	if memberIndex < 0 {
		return
	}
	status, active := collab.AgentIdle, false
	for _, item := range snapshot.Timeline {
		if item.AgentRun == nil || item.AgentRun.OwnerID != changed.OwnerID {
			continue
		}
		switch item.AgentRun.Status {
		case collab.RunWaitingApproval:
			status, active = collab.AgentWaitingApproval, true
		case collab.RunQueued, collab.RunRunning:
			if status != collab.AgentWaitingApproval {
				status, active = collab.AgentRunning, true
			}
		}
	}
	member := snapshot.Members[memberIndex]
	if active {
		member.Agent.Status = status
	} else if changed.Status == collab.RunFailed {
		member.Agent.Status = collab.AgentError
	} else if member.Status == collab.MemberOnline {
		member.Agent.Status = collab.AgentIdle
	} else {
		member.Agent.Status = collab.AgentOffline
	}
	snapshot.Members[memberIndex] = member
}
