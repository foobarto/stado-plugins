package main

import (
	"fmt"
	"net/url"
	"strconv"
)

type cdpWebSocketEndpoint struct {
	host       string
	hostHeader string
	path       string
	port       int
}

func parseCDPWebSocketURL(raw string) (cdpWebSocketEndpoint, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "ws" || parsed.Hostname() == "" || parsed.Port() == "" {
		return cdpWebSocketEndpoint{}, fmt.Errorf("CDP: invalid WebSocket URL %q", raw)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return cdpWebSocketEndpoint{}, fmt.Errorf("CDP: invalid WebSocket port %q", parsed.Port())
	}
	path := parsed.RequestURI()
	if path == "" {
		path = "/"
	}
	return cdpWebSocketEndpoint{
		host:       parsed.Hostname(),
		hostHeader: parsed.Host,
		path:       path,
		port:       port,
	}, nil
}
