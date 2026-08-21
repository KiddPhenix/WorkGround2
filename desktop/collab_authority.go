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

func (a *App) openCollaborationAuthority(ctx context.Context, input HostCollaborationRoomInput) (*collaborationAuthority, error) {
	a.collaborationAuthorityMu.Lock()
	authority := a.collaborationAuthority
	if authority == nil {
		storeRoot := strings.TrimSpace(config.MemoryUserDir())
		if storeRoot == "" {
			a.collaborationAuthorityMu.Unlock()
			return nil, fmt.Errorf("collaboration data directory is unavailable")
		}
		store, err := collab.OpenFileStore(filepath.Join(storeRoot, "collaboration-host-v1"))
		if err != nil {
			a.collaborationAuthorityMu.Unlock()
			return nil, err
		}
		hub := collab.NewHub()
		authority = &collaborationAuthority{store: store, hub: hub, service: collab.NewService(store, hub)}
		a.collaborationAuthority = authority
	}
	a.collaborationAuthorityMu.Unlock()

	room := strings.TrimSpace(input.Room)
	roomName := strings.TrimSpace(input.RoomName)
	if roomName == "" {
		roomName = room
	}
	_, err := authority.service.CreateRoom(ctx, collab.CreateRoomInput{
		RequestID:   stableCollaborationID("create", room),
		ID:          room,
		Name:        roomName,
		Description: strings.TrimSpace(input.Description),
		Token:       strings.TrimSpace(input.Token),
	})
	if err != nil {
		return nil, err
	}
	return authority, nil
}
