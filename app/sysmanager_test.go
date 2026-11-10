//go:build !windows

package app

import (
	"context"
	"testing"
	"time"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// A local Session's System tool samples this computer through the same
// sampler a Host uses, with no connection manager behind it.
func TestSystemSamplesThisComputer(t *testing.T) {
	service := appsysmanager.NewService(sysmanagerConnector{})
	defer service.Close()
	changed := make(chan struct{}, 1)
	defer service.Subscribe(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})()
	if err := service.Start(thisComputer(), appsysmanager.TabOverview, time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(20 * time.Second)
	for {
		state := service.Snapshot().Tabs[appsysmanager.TabOverview]
		switch state.Status {
		case appsysmanager.StatusLive:
			if state.Overview.MemTotalMB <= 0 || state.Overview.Cores <= 0 {
				t.Fatalf("overview = %+v, want this computer's memory and cores", state.Overview)
			}
			return
		case appsysmanager.StatusError, appsysmanager.StatusLogin, appsysmanager.StatusUnsupported:
			t.Fatalf("overview = %s %s %s", state.Status, state.Reason, state.Detail)
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("overview stayed %s for 20s", state.Status)
		}
	}
}

func TestThisComputerLeadsTheSystemToolsHosts(t *testing.T) {
	vault := hostList{{ID: "h1", Label: "Host one"}}
	got, err := withThisComputer{vault}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != thisComputerID || got[0].Protocol != domainvault.ProtocolLocal || got[1].ID != "h1" {
		t.Fatalf("hosts = %+v, want this computer, then the Vault's", got)
	}
}

type hostList []domainvault.Host

func (l hostList) List(context.Context) ([]domainvault.Host, error) { return l, nil }
