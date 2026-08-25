package main

import "net"

// listenNetwork is the single entry point for Desktop-owned listeners.
// Production keeps net.Listen; TestMain replaces it before any test runs.
var listenNetwork = net.Listen
