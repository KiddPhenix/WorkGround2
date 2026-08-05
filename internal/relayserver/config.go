// Package relayserver implements the lightweight WorkGround2 WebSocket relay.
package relayserver

import "time"

type Config struct {
	RelayID           string
	Listen            string
	PublicURL         string
	AccessMode        string
	AccessToken       string
	AllowDiscovery    bool
	AdvertisementTTL  time.Duration
	HostHeartbeat     time.Duration
	IdleTimeout       time.Duration
	CapabilityTTL     time.Duration
	JoinRefTTL        time.Duration
	MaxTunnels        int
	MaxPeersPerTunnel int
	MaxFrameBytes     int64
	ControlQueue      int
	DataQueue         int
	WriteTimeout      time.Duration
	RequestsPerMinute int
	FramesPerSecond   int
	BytesPerSecond    int64
	MaxStreamsPerPeer int
}

func DefaultConfig() Config {
	return Config{
		RelayID: "local", Listen: ":8443", AccessMode: "public", AllowDiscovery: true,
		AdvertisementTTL: 120 * time.Second, HostHeartbeat: 30 * time.Second,
		IdleTimeout: 120 * time.Second, CapabilityTTL: 7 * 24 * time.Hour,
		JoinRefTTL: 60 * time.Second, MaxTunnels: 10000, MaxPeersPerTunnel: 256,
		MaxFrameBytes: 1 << 20, ControlQueue: 128, DataQueue: 64,
		WriteTimeout: 10 * time.Second, RequestsPerMinute: 120,
		FramesPerSecond: 256, BytesPerSecond: 8 << 20, MaxStreamsPerPeer: 32,
	}
}

func (c *Config) normalize() {
	d := DefaultConfig()
	if c.RelayID == "" {
		c.RelayID = d.RelayID
	}
	if c.Listen == "" {
		c.Listen = d.Listen
	}
	if c.AccessMode == "" {
		c.AccessMode = d.AccessMode
	}
	if c.AdvertisementTTL <= 0 {
		c.AdvertisementTTL = d.AdvertisementTTL
	}
	if c.HostHeartbeat <= 0 {
		c.HostHeartbeat = d.HostHeartbeat
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = d.IdleTimeout
	}
	if c.CapabilityTTL <= 0 {
		c.CapabilityTTL = d.CapabilityTTL
	}
	if c.JoinRefTTL <= 0 {
		c.JoinRefTTL = d.JoinRefTTL
	}
	if c.MaxTunnels <= 0 {
		c.MaxTunnels = d.MaxTunnels
	}
	if c.MaxPeersPerTunnel <= 0 {
		c.MaxPeersPerTunnel = d.MaxPeersPerTunnel
	}
	if c.MaxFrameBytes <= 0 {
		c.MaxFrameBytes = d.MaxFrameBytes
	}
	if c.ControlQueue <= 0 {
		c.ControlQueue = d.ControlQueue
	}
	if c.DataQueue <= 0 {
		c.DataQueue = d.DataQueue
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = d.WriteTimeout
	}
	if c.RequestsPerMinute <= 0 {
		c.RequestsPerMinute = d.RequestsPerMinute
	}
	if c.FramesPerSecond <= 0 {
		c.FramesPerSecond = d.FramesPerSecond
	}
	if c.BytesPerSecond <= 0 {
		c.BytesPerSecond = d.BytesPerSecond
	}
	if c.MaxStreamsPerPeer <= 0 {
		c.MaxStreamsPerPeer = d.MaxStreamsPerPeer
	}
}
