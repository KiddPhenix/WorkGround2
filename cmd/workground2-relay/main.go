// Command workground2-relay runs the standalone WorkGround2 Room relay.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"workground2/internal/relayserver"
)

const version = "dev"

type fileConfig struct {
	Relay relayFileConfig `toml:"relay"`
}

type relayFileConfig struct {
	Listen                  string `toml:"listen"`
	PublicURL               string `toml:"public_url"`
	DataDir                 string `toml:"data_dir"`
	AccessMode              string `toml:"access_mode"`
	MasterKeyEnv            string `toml:"master_key_env"`
	AccessTokenEnv          string `toml:"access_token_env"`
	AllowDiscovery          *bool  `toml:"allow_discovery"`
	AdvertisementTTLSeconds int    `toml:"advertisement_ttl_seconds"`
	HostHeartbeatSeconds    int    `toml:"host_heartbeat_seconds"`
	IdleTimeoutSeconds      int    `toml:"idle_timeout_seconds"`
	MaxTunnels              int    `toml:"max_tunnels"`
	MaxPeersPerTunnel       int    `toml:"max_peers_per_tunnel"`
	MaxFrameBytes           int64  `toml:"max_frame_bytes"`
	MetricsListen           string `toml:"metrics_listen"`
}

type options struct {
	config        string
	dataDir       string
	tlsCert       string
	tlsKey        string
	metricsListen string
	cfg           relayserver.Config
	showVersion   bool
	masterKeyEnv  string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseOptions(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Println(version)
		return nil
	}
	key, err := masterKey(opts.dataDir, opts.masterKeyEnv)
	if err != nil {
		return err
	}
	relay, err := relayserver.New(opts.cfg, key)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: opts.cfg.Listen, Handler: relay.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: opts.cfg.IdleTimeout}
	protocol := "ws"
	if opts.tlsCert != "" {
		protocol = "wss"
	}
	listener, err := net.Listen("tcp", opts.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", opts.cfg.Listen, err)
	}
	defer listener.Close()
	slog.Info("starting room relay", "listen", listener.Addr().String(), "protocol", protocol, "discovery", opts.cfg.AllowDiscovery, "access_mode", opts.cfg.AccessMode, "data_dir", opts.dataDir)
	if protocol == "ws" {
		slog.Warn("relay TLS is disabled; use only on a trusted network or behind a TLS reverse proxy")
	}
	errCh := make(chan error, 2)
	var metricsServer *http.Server
	if opts.metricsListen != "" {
		metricsListener, listenErr := net.Listen("tcp", opts.metricsListen)
		if listenErr != nil {
			return fmt.Errorf("listen for metrics on %s: %w", opts.metricsListen, listenErr)
		}
		defer metricsListener.Close()
		metricsServer = &http.Server{Handler: relay.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second}
		slog.Info("starting relay metrics", "listen", metricsListener.Addr().String())
		go func() { errCh <- metricsServer.Serve(metricsListener) }()
	}
	go func() {
		if protocol == "wss" {
			errCh <- httpServer.ServeTLS(listener, opts.tlsCert, opts.tlsKey)
		} else {
			errCh <- httpServer.Serve(listener)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve relay on %s: %w", opts.cfg.Listen, err)
	case <-ctx.Done():
		relay.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if metricsServer != nil {
			_ = metricsServer.Shutdown(shutdownCtx)
		}
		return httpServer.Shutdown(shutdownCtx)
	}
}

func parseOptions(args []string) (options, error) {
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	configPath := findArg(args, "--config")
	cfg := relayserver.DefaultConfig()
	dataDir, err := defaultDataDir()
	if err != nil {
		return options{}, err
	}
	var file relayFileConfig
	if configPath != "" {
		var wrapped fileConfig
		if _, err := toml.DecodeFile(configPath, &wrapped); err != nil {
			return options{}, fmt.Errorf("load relay config: %w", err)
		}
		file = wrapped.Relay
		applyFile(&cfg, &dataDir, file)
	}
	applyEnv(&cfg, &dataDir)
	fs := flag.NewFlagSet("workground2-relay", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", cfg.Listen, "listen address (default :8443)")
	_ = fs.String("config", configPath, "relay TOML configuration file")
	data := fs.String("data-dir", dataDir, "directory for relay local state")
	publicURL := fs.String("public-url", cfg.PublicURL, "public WebSocket URL")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	accessMode := fs.String("access-mode", cfg.AccessMode, "access mode: public or token")
	allowDiscovery := fs.Bool("allow-discovery", cfg.AllowDiscovery, "enable active Room discovery")
	metricsListen := fs.String("metrics-listen", file.MetricsListen, "optional separate metrics listener")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("unknown arguments: %s", strings.Join(fs.Args(), " "))
	}
	cfg.Listen, cfg.PublicURL, cfg.AccessMode, cfg.AllowDiscovery = *listen, *publicURL, *accessMode, *allowDiscovery
	if (*tlsCert == "") != (*tlsKey == "") {
		return options{}, errors.New("--tls-cert and --tls-key must be configured together")
	}
	if cfg.Listen == "" {
		return options{}, errors.New("relay listen address is required")
	}
	return options{config: configPath, dataDir: *data, tlsCert: *tlsCert, tlsKey: *tlsKey, metricsListen: *metricsListen, cfg: cfg, showVersion: *showVersion, masterKeyEnv: file.MasterKeyEnv}, nil
}

func applyFile(cfg *relayserver.Config, dataDir *string, file relayFileConfig) {
	if file.Listen != "" {
		cfg.Listen = file.Listen
	}
	if file.PublicURL != "" {
		cfg.PublicURL = file.PublicURL
	}
	if file.DataDir != "" {
		*dataDir = file.DataDir
	}
	if file.AccessMode != "" {
		cfg.AccessMode = file.AccessMode
	}
	if file.AccessTokenEnv != "" {
		cfg.AccessToken = os.Getenv(file.AccessTokenEnv)
	}
	if file.AllowDiscovery != nil {
		cfg.AllowDiscovery = *file.AllowDiscovery
	}
	if file.AdvertisementTTLSeconds > 0 {
		cfg.AdvertisementTTL = time.Duration(file.AdvertisementTTLSeconds) * time.Second
	}
	if file.HostHeartbeatSeconds > 0 {
		cfg.HostHeartbeat = time.Duration(file.HostHeartbeatSeconds) * time.Second
	}
	if file.IdleTimeoutSeconds > 0 {
		cfg.IdleTimeout = time.Duration(file.IdleTimeoutSeconds) * time.Second
	}
	if file.MaxTunnels > 0 {
		cfg.MaxTunnels = file.MaxTunnels
	}
	if file.MaxPeersPerTunnel > 0 {
		cfg.MaxPeersPerTunnel = file.MaxPeersPerTunnel
	}
	if file.MaxFrameBytes > 0 {
		cfg.MaxFrameBytes = file.MaxFrameBytes
	}
}

func applyEnv(cfg *relayserver.Config, dataDir *string) {
	if v := os.Getenv("WG2_RELAY_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("WG2_RELAY_PUBLIC_URL"); v != "" {
		cfg.PublicURL = v
	}
	if v := os.Getenv("WG2_RELAY_DATA_DIR"); v != "" {
		*dataDir = v
	}
	if v := os.Getenv("WG2_RELAY_ACCESS_MODE"); v != "" {
		cfg.AccessMode = v
	}
	if v := os.Getenv("WG2_RELAY_ACCESS_TOKEN"); v != "" {
		cfg.AccessToken = v
	}
	if v := os.Getenv("WG2_RELAY_ALLOW_DISCOVERY"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.AllowDiscovery = b
		}
	}
}

func masterKey(dataDir, configuredEnv string) ([]byte, error) {
	raw := os.Getenv("WG2_RELAY_MASTER_KEY")
	if raw == "" && configuredEnv != "" {
		raw = os.Getenv(configuredEnv)
	}
	if raw != "" {
		if b, err := base64.RawStdEncoding.DecodeString(raw); err == nil && len(b) >= 32 {
			return b, nil
		}
		if len(raw) >= 32 {
			return []byte(raw), nil
		}
		return nil, errors.New("WG2_RELAY_MASTER_KEY must contain at least 32 bytes")
	}
	return relayserver.LoadOrCreateMasterKey(dataDir)
}

func defaultDataDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(root, "WorkGround2", "relay"), nil
}
func findArg(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"=")
		}
	}
	return ""
}
