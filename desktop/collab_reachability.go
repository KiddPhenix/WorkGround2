package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (c *desktopCollaboration) startRelayBindings(ctx context.Context, conn *collaborationConnection, input HostCollaborationRoomInput) error {
	if len(input.RelayIDs) == 0 {
		return nil
	}
	var failures []string
	for _, relayID := range input.RelayIDs {
		relayID = strings.TrimSpace(relayID)
		if relayID == "" {
			continue
		}
		binding, route, err := c.openRelayHost(ctx, conn, relayID, input)
		conn.routes = append(conn.routes, route)
		if err != nil {
			failures = append(failures, relayID+": "+err.Error())
			continue
		}
		conn.relayBindings = append(conn.relayBindings, binding)
	}
	if len(failures) > 0 {
		return fmt.Errorf("relay bind failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (c *desktopCollaboration) retryRelayBindings(ctx context.Context, conn *collaborationConnection) {
	needsRetry := false
	for _, route := range conn.routes {
		if route.Kind == "relay" && route.Status != "connected" {
			needsRetry = true
			break
		}
	}
	if !needsRetry {
		return
	}
	for _, binding := range conn.relayBindings {
		closeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = binding.Close(closeCtx)
		cancel()
	}
	conn.relayBindings = nil
	lanRoutes := conn.routes[:0]
	for _, route := range conn.routes {
		if route.Kind == "lan" {
			lanRoutes = append(lanRoutes, route)
		}
	}
	conn.routes = lanRoutes
	visibility := "private"
	if conn.advertisement != nil {
		visibility = conn.advertisement.Visibility
	}
	conn.advertisement = nil
	err := c.startRelayBindings(ctx, conn, HostCollaborationRoomInput{
		Room: conn.room, RoomName: conn.roomName, Description: conn.description, Token: conn.joinToken,
		RelayIDs: append([]string(nil), conn.relayIDs...), Visibility: visibility,
		Advertisement: &RoomAdvertisementInput{Name: conn.roomName, Description: conn.description},
	})
	c.mu.Lock()
	if c.conn == conn {
		c.state.Routes = publicCollaborationRoutes(conn.routes)
		c.state.Advertisement = conn.advertisement
		if err != nil {
			c.state.LastError, c.state.Retryable = err.Error(), true
		}
		c.persistLocked()
	}
	c.mu.Unlock()
}
