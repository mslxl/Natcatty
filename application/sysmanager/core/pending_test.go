package core

import (
	"context"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestRunPendingDerivesTheContainerActionFromTheKind makes Kind the single
// source of truth: a malformed DockerAction field cannot make the executor run
// a different command than the one the confirmed text named.
func TestRunPendingDerivesTheContainerActionFromTheKind(t *testing.T) {
	connector := newFakeConnector()
	service := NewService(connector)
	host := fakeHost("pending-kind")

	result, err := service.RunPending(context.Background(), PendingAction{
		Kind:          ActionDockerRemove,
		DockerAction:  DockerStop,
		Host:          host,
		ContainerID:   "3f2a1b",
		ContainerName: "web",
		Runtime:       RuntimeDocker,
	})
	if err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	commands := connector.commands("pending-kind")
	if len(commands) != 1 || commands[0] != "docker rm -f 3f2a1b" {
		t.Fatalf("commands = %v, want [docker rm -f 3f2a1b]", commands)
	}
	if result.Kind != ActionDockerRemove {
		t.Fatalf("result kind = %q, want %q", result.Kind, ActionDockerRemove)
	}
}

// TestRunPendingDerivesStartAndStopFromTheKind checks the other two kinds so a
// future edit cannot special-case only remove.
func TestRunPendingDerivesStartAndStopFromTheKind(t *testing.T) {
	cases := []struct {
		kind ActionKind
		bad  DockerAction
		want string
	}{
		{ActionDockerStart, DockerRemove, "docker start 3f2a1b"},
		{ActionDockerStop, DockerStart, "docker stop 3f2a1b"},
	}
	for _, test := range cases {
		connector := newFakeConnector()
		service := NewService(connector)
		host := domainvault.Host{ID: "pending-kind", Label: "pending-kind"}
		_, err := service.RunPending(context.Background(), PendingAction{
			Kind:         test.kind,
			DockerAction: test.bad,
			Host:         host,
			ContainerID:  "3f2a1b",
			Runtime:      RuntimeDocker,
		})
		if err != nil {
			t.Fatalf("%s: RunPending: %v", test.kind, err)
		}
		commands := connector.commands("pending-kind")
		if len(commands) != 1 || commands[0] != test.want {
			t.Fatalf("%s: commands = %v, want [%s]", test.kind, commands, test.want)
		}
	}
}
