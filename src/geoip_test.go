package main

import "testing"

func TestCountryFlag(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"FI", "\U0001F1EB\U0001F1EE"},
		{"DE", "\U0001F1E9\U0001F1EA"},
		{"US", "\U0001F1FA\U0001F1F8"},
		{"fi", "\U0001F1EB\U0001F1EE"},
		{"", ""},
		{"X", ""},
		{"ABC", ""},
	}
	for _, tt := range tests {
		got := countryFlag(tt.code)
		if got != tt.want {
			t.Errorf("countryFlag(%q) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestResolveCountries_Empty(t *testing.T) {
	result := resolveCountries(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestResolveCountries_CacheHit(t *testing.T) {
	countryCacheMu.Lock()
	countryCache["cached.example.com"] = "FI"
	countryCacheMu.Unlock()
	defer func() {
		countryCacheMu.Lock()
		delete(countryCache, "cached.example.com")
		countryCacheMu.Unlock()
	}()

	result := resolveCountries([]string{"cached.example.com:443"})
	if result["cached.example.com:443"] != "FI" {
		t.Errorf("expected FI from cache, got %q", result["cached.example.com:443"])
	}
}

func TestPingResult_HasCountryFields(t *testing.T) {
	r := PingResult{Name: "test", LatencyMs: 10, Country: "FI", Flag: "\U0001F1EB\U0001F1EE"}
	if r.Country != "FI" {
		t.Errorf("country = %q", r.Country)
	}
	if r.Flag != "\U0001F1EB\U0001F1EE" {
		t.Errorf("flag = %q", r.Flag)
	}
}

func TestProfileCountryCode_LocalMetadata(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Germany Frankfurt", "DE"},
		{"Swiss Zurich", "CH"},
		{"Astana", "KZ"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		if got := profileCountryCode(Profile{Name: tt.name, Link: "vless://user@example.invalid:443"}); got != tt.want {
			t.Errorf("profileCountryCode(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
