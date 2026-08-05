package main

import "testing"

func TestNoArgumentsUseServeDefaults(t *testing.T) {
	opts, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.cfg.Listen != ":8443" || !opts.cfg.AllowDiscovery || opts.cfg.AccessMode != "public" {
		t.Fatalf("unexpected defaults: %#v", opts.cfg)
	}
}

func TestServeIsOptional(t *testing.T) {
	a, err := parseOptions([]string{"--listen", "127.0.0.1:9000"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseOptions([]string{"serve", "--listen", "127.0.0.1:9000"})
	if err != nil {
		t.Fatal(err)
	}
	if a.cfg.Listen != b.cfg.Listen {
		t.Fatalf("serve changed parsing: %q != %q", a.cfg.Listen, b.cfg.Listen)
	}
}

func TestHelpExitsSuccessfully(t *testing.T) {
	if err := run([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
}
