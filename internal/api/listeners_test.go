package api

import (
	"net/http/httptest"
	"testing"
)

func TestRequestedShareFormat(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		userAgent string
		want      string
	}{
		{name: "explicit format wins", query: "?format=sing-box", userAgent: "Clash-Verge/v2", want: "sing-box"},
		{name: "clash verge", userAgent: "Clash-Verge/v2.4.2", want: "clash"},
		{name: "mihomo", userAgent: "mihomo/1.19", want: "clash"},
		{name: "sing-box", userAgent: "sing-box 1.12", want: "sing-box"},
		{name: "v2rayn remains default", userAgent: "v2rayN/7.0", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/sub/token"+test.query, nil)
			request.Header.Set("User-Agent", test.userAgent)
			if got := requestedShareFormat(request); got != test.want {
				t.Fatalf("requestedShareFormat() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCanonicalShareFormat(t *testing.T) {
	tests := map[string]string{
		"":         "v2rayn",
		"v2rayn":   "v2rayn",
		"clash":    "clash",
		"Mihomo":   "clash",
		"singbox":  "sing-box",
		"sing-box": "sing-box",
		"uri":      "uri",
	}
	for input, want := range tests {
		if got := canonicalShareFormat(input); got != want {
			t.Errorf("canonicalShareFormat(%q) = %q, want %q", input, got, want)
		}
	}
}
