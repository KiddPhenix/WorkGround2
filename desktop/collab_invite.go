package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type collaborationInviteV2 struct {
	Version   int                       `json:"version"`
	Room      string                    `json:"room"`
	HostKey   string                    `json:"hostKey"`
	Routes    []CollaborationRouteInput `json:"routes"`
	RoomToken string                    `json:"roomToken,omitempty"`
}

func buildCollaborationInviteV2(value collaborationInviteV2) (string, error) {
	value.Version = 2
	if strings.TrimSpace(value.Room) == "" || strings.TrimSpace(value.HostKey) == "" || len(value.Routes) == 0 {
		return "", fmt.Errorf("invalid collaboration V2 invite")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return "workground2://join#v2." + base64.RawURLEncoding.EncodeToString(data), nil
}

func applyCollaborationInvite(input *JoinCollaborationRoomInput) error {
	value := strings.TrimSpace(input.Invite)
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || !strings.EqualFold(u.Scheme, "workground2") {
		return fmt.Errorf("invalid collaboration invite")
	}
	if strings.EqualFold(u.Hostname(), "join") && strings.HasPrefix(u.Fragment, "v2.") {
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(u.Fragment, "v2."))
		if err != nil {
			return fmt.Errorf("invalid collaboration V2 invite")
		}
		var invite collaborationInviteV2
		if json.Unmarshal(data, &invite) != nil || invite.Version != 2 || strings.TrimSpace(invite.Room) == "" || strings.TrimSpace(invite.HostKey) == "" || len(invite.Routes) == 0 {
			return fmt.Errorf("invalid collaboration V2 invite")
		}
		input.Room, input.HostKey, input.Routes = invite.Room, invite.HostKey, append([]CollaborationRouteInput(nil), invite.Routes...)
		if input.Token == "" {
			input.Token = invite.RoomToken
		}
		return nil
	}
	port, err := strconv.Atoi(u.Port())
	room, decodeErr := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/"))
	if err != nil || decodeErr != nil || u.Hostname() == "" || port < 1 || port > 65535 || strings.TrimSpace(room) == "" {
		return fmt.Errorf("invalid collaboration invite")
	}
	input.Host, input.Port, input.Room = u.Hostname(), port, room
	if input.Token == "" {
		input.Token = strings.TrimSpace(u.Query().Get("token"))
	}
	return nil
}
