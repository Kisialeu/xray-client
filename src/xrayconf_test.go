package main

import "testing"

func TestRewriteOutboundDialIPRejectsMalformedEntry(t *testing.T) {
	settings := map[string]any{"vnext": []any{"not an object"}}
	if err := rewriteOutboundDialIP(settings, "192.0.2.1"); err == nil {
		t.Fatal("malformed outbound entry was accepted")
	}
}

func TestRewriteOutboundDialIPUpdatesAddress(t *testing.T) {
	entry := map[string]any{"address": "example.com"}
	settings := map[string]any{"servers": []any{entry}}
	if err := rewriteOutboundDialIP(settings, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if entry["address"] != "192.0.2.1" {
		t.Fatalf("address = %v, want 192.0.2.1", entry["address"])
	}
}
