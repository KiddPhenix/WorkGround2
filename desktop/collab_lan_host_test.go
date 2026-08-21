package main

import (
	"context"
	"testing"
	"time"

	"workground2/internal/collab"
)

func TestCollaborationV2LANHostMultiplexesRooms(t *testing.T) {
	isolateDesktopUserDirs(t)
	ctx := context.Background()
	app := &App{}
	host := &collaborationLANHost{}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := host.Close(shutdown); err != nil {
			t.Errorf("close shared LAN host: %v", err)
		}
	}()

	firstInput := HostCollaborationRoomInput{
		ListenHost: "127.0.0.1", Room: "room-a", RoomName: "Room A", SessionID: "session-a",
	}
	firstAuthority, err := app.openCollaborationAuthority(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	_, firstPort, releaseFirst, err := host.register(firstInput, firstAuthority)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := HostCollaborationRoomInput{
		ListenHost: "127.0.0.1", Room: "room-b", RoomName: "Room B", SessionID: "session-b",
	}
	secondAuthority, err := app.openCollaborationAuthority(ctx, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	_, secondPort, releaseSecond, err := host.register(secondInput, secondAuthority)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	if firstPort == 0 || secondPort != firstPort {
		t.Fatalf("shared ports = %d, %d", firstPort, secondPort)
	}

	peerA, _, snapshotA, err := joinCollaborationPeer(ctx, "127.0.0.1", firstPort, "room-a", "", collab.MemberDescriptor{
		ID: "member-a", Name: "Alice", Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Alice Agent", Status: collab.AgentIdle},
	}, "", collaborationProtocolV2)
	if err != nil {
		t.Fatalf("join room-a: %v", err)
	}
	peerB, _, snapshotB, err := joinCollaborationPeer(ctx, "127.0.0.1", firstPort, "room-b", "", collab.MemberDescriptor{
		ID: "member-b", Name: "Bob", Agent: collab.AgentDescriptor{ID: "agent-b", Name: "Bob Agent", Status: collab.AgentIdle},
	}, "", collaborationProtocolV2)
	if err != nil {
		t.Fatalf("join room-b: %v", err)
	}
	if snapshotA.Room.ID != "room-a" || snapshotB.Room.ID != "room-b" {
		t.Fatalf("room routing leaked: room-a=%q room-b=%q", snapshotA.Room.ID, snapshotB.Room.ID)
	}
	if _, err := peerA.Submit(ctx, collab.CommandEnvelope{
		RequestID: "post-a", Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "only room A"}},
	}); err != nil {
		t.Fatalf("post room-a: %v", err)
	}
	updatedB, err := peerB.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot room-b: %v", err)
	}
	for _, item := range updatedB.Timeline {
		if item.Chat != nil && item.Chat.Text == "only room A" {
			t.Fatalf("room-a timeline leaked into room-b: %+v", updatedB.Timeline)
		}
	}

	releaseFirst()
	if _, err := peerA.Snapshot(ctx); err == nil {
		t.Fatal("released room-a remained reachable")
	}
	if _, err := peerB.Snapshot(ctx); err != nil {
		t.Fatalf("releasing room-a interrupted room-b: %v", err)
	}

	conflictPort := 1
	if firstPort == conflictPort {
		conflictPort++
	}
	conflictInput := HostCollaborationRoomInput{
		ListenHost: "127.0.0.1", Port: conflictPort, Room: "room-c", RoomName: "Room C", SessionID: "session-c",
	}
	conflictAuthority, err := app.openCollaborationAuthority(ctx, conflictInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := host.register(conflictInput, conflictAuthority); err == nil {
		t.Fatal("different explicit port did not report the shared-listener conflict")
	}
}

func TestCollaborationAuthorityIsSharedByHostedRooms(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := &App{}
	first, err := app.openCollaborationAuthority(context.Background(), HostCollaborationRoomInput{Room: "room-a", RoomName: "Room A"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.openCollaborationAuthority(context.Background(), HostCollaborationRoomInput{Room: "room-b", RoomName: "Room B"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.store != second.store || first.service != second.service || first.hub != second.hub {
		t.Fatal("hosted rooms did not share one process authority")
	}
}

func TestCollaborationHostGeneratesStableV2RoomID(t *testing.T) {
	_, runtime, _ := newTestDesktopCollaboration(t)
	defer runtime.close()
	wantRoom := stableCollaborationID("room", "session-a")
	var got HostCollaborationRoomInput
	runtime.openHost = func(_ context.Context, input HostCollaborationRoomInput, identity collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		got = input
		snapshot := collab.Snapshot{
			Room:    collab.Room{ID: input.Room, Name: input.RoomName},
			Members: []collab.Member{{ID: identity.ID, Name: identity.Name, Agent: identity.Agent}},
		}
		return &collaborationConnection{
			peer: &fakeCollaborationPeer{snapshot: snapshot}, mode: "host", protocolVersion: input.ProtocolVersion,
			hostName: input.ListenHost, port: input.Port, room: input.Room, memberID: identity.ID, agentID: identity.Agent.ID,
			memberName: identity.Name, agentName: identity.Agent.Name, sessionID: input.SessionID,
			connectionSession: "cs-v2-generated", initialSnapshot: snapshot,
		}, nil
	}
	state, err := runtime.host(context.Background(), HostCollaborationRoomInput{
		ListenHost: "127.0.0.1", RoomName: "Generated Room", MemberName: "Alice", SessionID: "session-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Room != wantRoom || got.ProtocolVersion != collaborationProtocolV2 || state.Room != wantRoom || state.ProtocolVersion != collaborationProtocolV2 {
		t.Fatalf("generated V2 room mismatch: input=%+v state=%+v wantRoom=%q", got, state, wantRoom)
	}
}
