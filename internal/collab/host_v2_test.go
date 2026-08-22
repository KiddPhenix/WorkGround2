package collab

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestV2HandlerRoutesActiveRoomsByPath(t *testing.T) {
	store, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, NewHub())
	for _, room := range []string{"room-a", "room-b"} {
		if _, err := service.CreateRoom(context.Background(), CreateRoomInput{RequestID: "create-" + room, ID: room, Name: room}); err != nil {
			t.Fatal(err)
		}
	}
	active := map[string]bool{"room-a": true, "room-b": true}
	server := httptest.NewServer(NewV2Handler(service, func(room string) bool { return active[room] }))
	defer server.Close()

	join := func(room, member string) JoinResult {
		body, _ := json.Marshal(JoinInput{RequestID: "join-" + member, Member: MemberDescriptor{ID: member, Name: member, Agent: AgentDescriptor{ID: "agent-" + member, Name: member}}})
		resp, err := http.Post(server.URL+"/collab/v2/rooms/"+room+"/join", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Collab-Protocol") != "2" {
			t.Fatalf("join %s status=%d protocol=%q", room, resp.StatusCode, resp.Header.Get("X-Collab-Protocol"))
		}
		var result JoinResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	joinedA := join("room-a", "member-a")
	joinedB := join("room-b", "member-b")
	snapshot := func(room, session string) Snapshot {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/collab/v2/rooms/"+room+"/snapshot", nil)
		req.Header.Set("Authorization", "Bearer "+session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var value Snapshot
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&value) != nil {
			t.Fatalf("snapshot %s status=%d", room, resp.StatusCode)
		}
		return value
	}
	if gotA, gotB := snapshot("room-a", joinedA.ConnectionSession), snapshot("room-b", joinedB.ConnectionSession); gotA.Room.ID != "room-a" || gotB.Room.ID != "room-b" {
		t.Fatalf("snapshot rooms = %q, %q", gotA.Room.ID, gotB.Room.ID)
	}

	active["room-a"] = false
	resp, err := http.Get(server.URL + "/collab/v2/rooms/room-a/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("inactive room status = %d, want 404", resp.StatusCode)
	}
}

func TestV2HandlerRejectsBodyRoomMismatch(t *testing.T) {
	store, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, NewHub())
	if _, err := service.CreateRoom(context.Background(), CreateRoomInput{RequestID: "create", ID: "room-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewV2Handler(service, func(room string) bool { return room == "room-a" }))
	defer server.Close()
	body, _ := json.Marshal(JoinInput{RequestID: "join", Room: "room-b", Member: MemberDescriptor{ID: "member", Name: "Member", Agent: AgentDescriptor{ID: "agent", Name: "Agent"}}})
	resp, err := http.Post(server.URL+"/collab/v2/rooms/room-a/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched room status = %d, want 400", resp.StatusCode)
	}
}
