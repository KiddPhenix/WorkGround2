package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var (
	validRelayID       = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	validRelayTokenEnv = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// SetCollaboration replaces the user-global relay list after normalizing and
// validating every endpoint. A non-loopback ws:// endpoint is accepted only
// when the same entry explicitly opts into allow_insecure.
func (c *Config) SetCollaboration(next CollaborationConfig) error {
	next = NormalizeCollaboration(next)
	if err := ValidateCollaboration(next); err != nil {
		return err
	}
	c.Collaboration = cloneCollaboration(next)
	return nil
}

// NormalizeCollaboration trims presentation fields without silently granting
// insecure transport permission or changing the user's relay order.
func NormalizeCollaboration(c CollaborationConfig) CollaborationConfig {
	out := CollaborationConfig{
		PreferLAN: c.PreferLAN, ConnectTimeout: c.ConnectTimeout, RouteStable: c.RouteStable,
		Relays: make([]RelayConfig, 0, len(c.Relays)),
	}
	for _, relay := range c.Relays {
		relay.ID = strings.TrimSpace(relay.ID)
		relay.Name = strings.TrimSpace(relay.Name)
		relay.URL = strings.TrimSpace(relay.URL)
		relay.AccessTokenEnv = strings.TrimSpace(relay.AccessTokenEnv)
		if relay.Name == "" {
			relay.Name = relay.ID
		}
		out.Relays = append(out.Relays, relay)
	}
	return out
}

// ValidateCollaboration checks the persisted trust boundary. Insecure relays
// must remain an explicit per-entry choice so editing unrelated settings cannot
// accidentally enable plaintext Room traffic.
func ValidateCollaboration(c CollaborationConfig) error {
	if c.ConnectTimeout < 1 || c.ConnectTimeout > 120 {
		return fmt.Errorf("collaboration connect_timeout_seconds %d: must be between 1 and 120", c.ConnectTimeout)
	}
	if c.RouteStable < 1 || c.RouteStable > 3600 {
		return fmt.Errorf("collaboration route_stable_seconds %d: must be between 1 and 3600", c.RouteStable)
	}
	seen := make(map[string]struct{}, len(c.Relays))
	for i, relay := range c.Relays {
		label := fmt.Sprintf("relay %d", i+1)
		if !validRelayID.MatchString(relay.ID) {
			return fmt.Errorf("%s id %q: must be 1-64 letters, digits, dot, underscore, or dash", label, relay.ID)
		}
		key := strings.ToLower(relay.ID)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate relay id %q", relay.ID)
		}
		seen[key] = struct{}{}

		u, err := url.Parse(relay.URL)
		if err != nil || u.Host == "" {
			return fmt.Errorf("relay %q url %q is invalid", relay.ID, relay.URL)
		}
		if u.User != nil || u.Fragment != "" {
			return fmt.Errorf("relay %q url must not contain credentials or a fragment", relay.ID)
		}
		switch strings.ToLower(u.Scheme) {
		case "wss":
		case "ws":
			if !relayLoopbackHost(u.Hostname()) && !relay.AllowInsecure {
				return fmt.Errorf("relay %q uses ws://; explicitly enable allow_insecure to accept plaintext transport risk", relay.ID)
			}
		default:
			return fmt.Errorf("relay %q url must use wss:// or ws://", relay.ID)
		}
		if relay.Priority < 0 || relay.Priority > 1000 {
			return fmt.Errorf("relay %q priority %d: must be between 0 and 1000", relay.ID, relay.Priority)
		}
		if relay.AccessTokenEnv != "" && !validRelayTokenEnv.MatchString(relay.AccessTokenEnv) {
			return fmt.Errorf("relay %q access_token_env %q is not a valid environment variable name", relay.ID, relay.AccessTokenEnv)
		}
	}
	return nil
}

func relayLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func cloneCollaboration(c CollaborationConfig) CollaborationConfig {
	out := c
	out.Relays = append([]RelayConfig(nil), c.Relays...)
	return out
}

func normalizeCollaborationConfig(c *Config) {
	if c == nil {
		return
	}
	c.Collaboration = NormalizeCollaboration(c.Collaboration)
}
