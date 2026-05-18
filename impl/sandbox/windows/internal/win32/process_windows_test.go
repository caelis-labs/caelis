//go:build windows

package win32

import "testing"

func TestSplitDomainUser(t *testing.T) {
	tests := []struct {
		name       string
		username   string
		domain     string
		wantUser   string
		wantDomain string
	}{
		{name: "local account", username: "CaelisSandboxOffline", wantUser: "CaelisSandboxOffline", wantDomain: "."},
		{name: "qualified account", username: `WORKSTATION\caelis`, wantUser: "caelis", wantDomain: "WORKSTATION"},
		{name: "explicit domain wins", username: `OTHER\caelis`, domain: "DOMAIN", wantUser: "caelis", wantDomain: "DOMAIN"},
		{name: "upn", username: "user@example.test", wantUser: "user@example.test", wantDomain: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotDomain := splitDomainUser(tt.username, tt.domain)
			if gotUser != tt.wantUser || gotDomain != tt.wantDomain {
				t.Fatalf("splitDomainUser() = %q, %q; want %q, %q", gotUser, gotDomain, tt.wantUser, tt.wantDomain)
			}
		})
	}
}
