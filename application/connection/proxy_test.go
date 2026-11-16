package connection

import "testing"

// TestResolveProxyFollowsHostGroupGlobalDirect is the route-resolution table:
// each level wins over the ones below it, and an explicit direct choice stops
// every inherited proxy, including the app-wide default.
func TestResolveProxyFollowsHostGroupGlobalDirect(t *testing.T) {
	manual := &Proxy{Type: ProxyHTTP, Host: "manual.example.com", Port: 3128}
	groupManual := &Proxy{Type: ProxySOCKS5, Host: "group.example.com", Port: 1080}
	tests := []struct {
		name   string
		host   ProxySource
		group  ProxySource
		global string
		want   ProxyChoice
	}{
		{
			name:   "host manual beats host profile group and global",
			host:   ProxySource{Manual: manual, ProfileID: "host-profile"},
			group:  ProxySource{ProfileID: "group-profile"},
			global: "global-profile",
			want:   ProxyChoice{Manual: manual},
		},
		{
			name:   "host profile beats group and global",
			host:   ProxySource{ProfileID: "host-profile"},
			group:  ProxySource{ProfileID: "group-profile"},
			global: "global-profile",
			want:   ProxyChoice{ProfileID: "host-profile"},
		},
		{
			name:   "host direct beats its own proxy and both defaults",
			host:   ProxySource{Direct: true, Manual: manual, ProfileID: "host-profile"},
			group:  ProxySource{ProfileID: "group-profile"},
			global: "global-profile",
			want:   ProxyChoice{Direct: true},
		},
		{
			name:   "group profile beats the global default",
			group:  ProxySource{ProfileID: "group-profile"},
			global: "global-profile",
			want:   ProxyChoice{ProfileID: "group-profile"},
		},
		{
			name:   "group manual beats its profile",
			group:  ProxySource{Manual: groupManual, ProfileID: "group-profile"},
			global: "global-profile",
			want:   ProxyChoice{Manual: groupManual},
		},
		{
			name:   "group direct stops the global default",
			group:  ProxySource{Direct: true},
			global: "global-profile",
			want:   ProxyChoice{Direct: true},
		},
		{
			name:   "global default applies to an unconfigured host",
			global: "global-profile",
			want:   ProxyChoice{ProfileID: "global-profile"},
		},
		{
			name:   "no global default restores a direct connection",
			global: "",
			want:   ProxyChoice{Direct: true},
		},
		{
			name:   "a blank global default is not a profile",
			global: "   ",
			want:   ProxyChoice{Direct: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveProxy(test.host, test.group, test.global)
			if got.Direct != test.want.Direct || got.ProfileID != test.want.ProfileID ||
				got.Manual != test.want.Manual {
				t.Fatalf("ResolveProxy = %+v, want %+v", got, test.want)
			}
		})
	}
}
