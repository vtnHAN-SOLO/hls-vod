package clientip

import (
	"net/http/httptest"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		list    string
		trusts  bool
		wantErr bool
	}{
		{"", false, false},
		{" , ", false, false},
		{"10.8.0.1", true, false},
		{"10.8.0.1/32, 10.244.0.0/16", true, false},
		{"fd00::/8", true, false},
		{"10.0.0.0/33", false, true},
		{"edge", false, true},
		{"10.0.0.1,", true, false},
		{"fe80::1%eth0", false, true},
	}
	for _, tt := range tests {
		r, err := Parse(tt.list)
		if (err != nil) != tt.wantErr {
			t.Errorf("Parse(%q) err = %v, wantErr %v", tt.list, err, tt.wantErr)
			continue
		}
		if err == nil && r.Trusts() != tt.trusts {
			t.Errorf("Parse(%q).Trusts() = %v", tt.list, r.Trusts())
		}
	}
}

func TestIP(t *testing.T) {
	// The production shape: the edge proxy reaches the ingress controller
	// through a tunnel (10.8.0.1), the ingress controller reaches the api
	// from the pod CIDR.
	const proxies = "10.8.0.1/32, 10.244.0.0/16"
	tests := []struct {
		name    string
		trusted string
		peer    string
		xff     []string
		want    string
	}{
		{"no proxies: the peer, header ignored", "", "198.51.100.9:5555", []string{"1.2.3.4"}, "198.51.100.9"},
		{"untrusted peer: header ignored", proxies, "198.51.100.9:5555", []string{"1.2.3.4"}, "198.51.100.9"},
		{"through both proxies", proxies, "10.244.1.7:40000", []string{"203.0.113.5, 10.8.0.1"}, "203.0.113.5"},
		{"a spoofed leftmost entry is skipped", proxies, "10.244.1.7:40000",
			[]string{"1.2.3.4, 203.0.113.5, 10.8.0.1"}, "203.0.113.5"},
		{"a spoofed trusted-looking entry left of the client stays unused", proxies, "10.244.1.7:40000",
			[]string{"10.8.0.1, 203.0.113.5, 10.8.0.1"}, "203.0.113.5"},
		{"several header lines read as one list", proxies, "10.244.1.7:40000",
			[]string{"1.2.3.4", "203.0.113.5", "10.8.0.1"}, "203.0.113.5"},
		{"malformed entry: the last trusted address", proxies, "10.244.1.7:40000",
			[]string{"203.0.113.5, garbage, 10.8.0.1"}, "10.8.0.1"},
		{"malformed right next to the peer: the peer", proxies, "10.244.1.7:40000",
			[]string{"203.0.113.5, not-an-ip"}, "10.244.1.7"},
		{"empty entry is malformed", proxies, "10.244.1.7:40000", []string{"203.0.113.5,,10.8.0.1"}, "10.8.0.1"},
		{"trusted peer, no header: the peer", proxies, "10.244.1.7:40000", nil, "10.244.1.7"},
		{"all trusted: the leftmost", proxies, "10.244.1.7:40000", []string{"10.244.3.3, 10.8.0.1"}, "10.244.3.3"},
		{"IPv6 client", proxies, "10.244.1.7:40000", []string{"2001:db8::7, 10.8.0.1"}, "2001:db8::7"},
		{"bracketed IPv6 with a port", proxies, "10.244.1.7:40000", []string{"[2001:db8::7]:443, 10.8.0.1"}, "2001:db8::7"},
		{"IPv4 with a port", proxies, "10.244.1.7:40000", []string{"203.0.113.5:1234, 10.8.0.1"}, "203.0.113.5"},
		{"IPv4-mapped peer matches the IPv4 range", proxies, "[::ffff:10.244.1.7]:40000",
			[]string{"203.0.113.5, 10.8.0.1"}, "203.0.113.5"},
		{"zone is malformed", proxies, "10.244.1.7:40000", []string{"fe80::1%eth0, 10.8.0.1"}, "10.8.0.1"},
		{"no port in RemoteAddr: as it is", proxies, "pipe", []string{"1.2.3.4"}, "pipe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Parse(tt.trusted)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
			req.RemoteAddr = tt.peer
			for _, v := range tt.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if got := r.IP(req); got != tt.want {
				t.Fatalf("IP = %q, want %q", got, tt.want)
			}
		})
	}
	var zero *Resolver
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := zero.IP(req); got != "10.0.0.1" {
		t.Fatalf("nil resolver: %q", got)
	}
}
