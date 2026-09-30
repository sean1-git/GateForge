package main

import "testing"

func TestListenAddress(t *testing.T) {
	for _, tc := range []struct {
		name, explicit, port, want string
		invalid                    bool
	}{
		{"local stays loopback", "", "", "127.0.0.1:8080", false},
		{"Cloud Run ingress", "", "8080", "0.0.0.0:8080", false},
		{"custom platform port", "", "9000", "0.0.0.0:9000", false},
		{"flag overrides environment", "127.0.0.1:8443", "invalid", "127.0.0.1:8443", false},
		{"zero", "", "0", "", true},
		{"out of range", "", "65536", "", true},
		{"negative", "", "-1", "", true},
		{"whitespace", "", " 8080", "", true},
		{"non-numeric", "", "http", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listenAddress(tc.explicit, tc.port)
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("address=%q error=%v; want %q invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}
