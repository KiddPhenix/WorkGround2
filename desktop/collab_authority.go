package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"workground2/internal/collab"
	"workground2/internal/config"
)

// collaborationAuthority owns the single authoritative Room service. LAN and
// Relay endpoints are adapters around this object and may come and go without
// replacing Room state.
type collaborationAuthority struct {
	store   *collab.FileStore
	hub     *collab.Hub
	service *collab.Service
}

func openCollaborationAuthority(ctx context.Context, input HostCollaborationRoomInput) (*collaborationAuthority, error) {
	storeRoot := strings.TrimSpace(config.MemoryUserDir())
	if storeRoot == "" {
		return nil, fmt.Errorf("collaboration data directory is unavailable")
	}
	store, err := collab.OpenFileStore(filepath.Join(storeRoot, "collaboration-host-v1"))
	if err != nil {
		return nil, err
	}
	hub := collab.NewHub()
	service := collab.NewService(store, hub)
	room := strings.TrimSpace(input.Room)
	roomName := strings.TrimSpace(input.RoomName)
	if roomName == "" {
		roomName = room
	}
	_, err = service.CreateRoom(ctx, collab.CreateRoomInput{
		RequestID:   stableCollaborationID("create", room),
		ID:          room,
		Name:        roomName,
		Description: strings.TrimSpace(input.Description),
		Token:       strings.TrimSpace(input.Token),
	})
	if err != nil {
		return nil, err
	}
	return &collaborationAuthority{store: store, hub: hub, service: service}, nil
}
