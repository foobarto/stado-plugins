package main

import "testing"

func TestParseCDPWebSocketURL(t *testing.T) {
	endpoint, err := parseCDPWebSocketURL("ws://127.0.0.1:9222/devtools/browser/abc?token=1")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.host != "127.0.0.1" || endpoint.hostHeader != "127.0.0.1:9222" || endpoint.port != 9222 || endpoint.path != "/devtools/browser/abc?token=1" {
		t.Fatalf("unexpected endpoint: %+v", endpoint)
	}
}

func TestParseCDPWebSocketURLIPv6(t *testing.T) {
	endpoint, err := parseCDPWebSocketURL("ws://[::1]:9223/devtools/page/1")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.host != "::1" || endpoint.hostHeader != "[::1]:9223" || endpoint.port != 9223 {
		t.Fatalf("unexpected endpoint: %+v", endpoint)
	}
}

func TestParseCDPWebSocketURLRejectsMissingPortAndTLS(t *testing.T) {
	for _, raw := range []string{"ws://127.0.0.1/devtools", "wss://127.0.0.1:9222/devtools", "not a URL"} {
		if _, err := parseCDPWebSocketURL(raw); err == nil {
			t.Fatalf("accepted invalid URL %q", raw)
		}
	}
}
