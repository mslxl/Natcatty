package sysmanager

import (
	"errors"
	"strings"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// The runtime switcher and the Kubernetes tab are only sound if the probe
// reports each engine and kubectl, the command builders target the chosen
// engine, and the pod parser reads kubectl's own JSON. These tests stay on
// fixed shells and fixtures; no docker or kubectl binary is needed.

func TestCapabilitiesOfferOnlyDetectedContainerRuntimes(t *testing.T) {
	tests := []struct {
		name  string
		probe string
		want  []ContainerRuntime
	}{
		{"docker only", "__NC_HAS_docker=1\n",
			[]ContainerRuntime{RuntimeDocker}},
		{"podman only", "__NC_HAS_podman=1\n",
			[]ContainerRuntime{RuntimePodman}},
		{"nerdctl only", "__NC_HAS_nerdctl=1\n",
			[]ContainerRuntime{RuntimeNerdctl}},
		{"all three in display order", "__NC_HAS_nerdctl=1\n__NC_HAS_docker=1\n__NC_HAS_podman=1\n",
			[]ContainerRuntime{RuntimeDocker, RuntimePodman, RuntimeNerdctl}},
		{"none", "__NC_OS=Linux\n", nil},
	}
	for _, test := range tests {
		caps := parseCapabilities("__NC_OS=Linux\n" + test.probe)
		got := caps.ContainerRuntimes()
		if len(got) != len(test.want) {
			t.Errorf("%s runtimes = %v, want %v", test.name, got, test.want)
			continue
		}
		for index := range test.want {
			if got[index] != test.want[index] {
				t.Errorf("%s runtimes = %v, want %v", test.name, got, test.want)
				break
			}
		}
	}
}

func TestContainerListCommandUsesTheSelectedRuntime(t *testing.T) {
	for _, runtime := range ContainerRuntimes {
		bin := runtime.Binary()
		command := containerListCommand(runtime)
		for _, want := range []string{
			"command -v " + bin,
			bin + " ps -a --format '{{json .}}'",
			bin + " images --format '{{json .}}'",
			markerUnavailable + "=" + bin,
		} {
			if !strings.Contains(command, want) {
				t.Errorf("%s command is missing %q: %q", runtime, want, command)
			}
		}
		if runtime != RuntimeDocker && strings.Contains(command, "docker ") {
			t.Errorf("%s command invoked docker: %q", runtime, command)
		}
	}
	if dockerListCommand() != containerListCommand(RuntimeDocker) {
		t.Error("the Docker-only wrapper diverged from the selected-runtime builder")
	}
}

func TestContainerActionCommandsUseTheSelectedRuntime(t *testing.T) {
	for _, runtime := range ContainerRuntimes {
		bin := runtime.Binary()
		tests := []struct {
			action DockerAction
			want   string
		}{
			{DockerStart, bin + " start 3f2a1b"},
			{DockerStop, bin + " stop 3f2a1b"},
			{DockerRemove, bin + " rm -f 3f2a1b"},
		}
		for _, test := range tests {
			got, err := ContainerActionCommand(runtime, "3f2a1b", test.action)
			if err != nil || got != test.want {
				t.Errorf("%s %s = %q, %v; want %q", runtime, test.action, got, err, test.want)
			}
		}
		if got, err := ContainerInspectCommand(runtime, "3f2a1b"); err != nil ||
			got != bin+" inspect 3f2a1b" {
			t.Errorf("%s inspect = %q, %v", runtime, got, err)
		}
		if got, err := ContainerLogsCommand(runtime, "3f2a1b", 10); err != nil ||
			got != bin+" logs --tail 10 3f2a1b" {
			t.Errorf("%s logs = %q, %v", runtime, got, err)
		}
		if _, err := ContainerActionCommand(runtime, "", DockerStart); err == nil {
			t.Errorf("%s accepted an empty container id", runtime)
		}
	}
	if got := NormalizeContainerRuntime("containerd"); got != RuntimeDocker {
		// Normalize is total: an unknown engine must never produce a command.
		t.Errorf("NormalizeContainerRuntime(containerd) = %q", got)
	}
	if string(NormalizeContainerRuntime("podman")) != "podman" {
		t.Error("NormalizeContainerRuntime changed a known engine")
	}
}

func TestKubernetesCommandsListNamespacesAndPods(t *testing.T) {
	command, err := kubernetesListCommand("")
	if err != nil {
		t.Fatalf("kubernetes list: %v", err)
	}
	for _, want := range []string{
		markerKubernetesBegin,
		"kubectl get namespaces -o json",
		"kubectl get pods -o json",
		markerUnavailable + "=kubectl",
	} {
		if !strings.Contains(command, want) {
			t.Errorf("unscoped command is missing %q: %q", want, command)
		}
	}
	if strings.Contains(command, " get pods -n ") {
		t.Errorf("an empty namespace must let kubeconfig choose: %q", command)
	}

	scoped, err := kubernetesListCommand("team-a")
	if err != nil {
		t.Fatalf("scoped kubernetes list: %v", err)
	}
	if !strings.Contains(scoped, "kubectl -n 'team-a' get pods -o json") {
		t.Errorf("scoped command = %q", scoped)
	}
	for _, bad := range []string{"Bad_Name", "../etcd", "", "team a"} {
		if bad == "" {
			continue
		}
		if _, err := kubernetesListCommand(bad); err == nil {
			t.Errorf("kubernetesListCommand accepted %q", bad)
		}
	}
}

func TestKubectlExecCommandTargetsThePodAndContainer(t *testing.T) {
	command, err := KubectlExecCommand("team-a", "web-0", "app")
	if err != nil {
		t.Fatalf("kubectl exec: %v", err)
	}
	for _, want := range []string{
		"exec kubectl -n 'team-a' exec -it 'web-0' -c 'app' -- sh -c ",
		`printf '\033[H\033[2J\033[3J'`,
	} {
		if !strings.Contains(command, want) {
			t.Errorf("exec command is missing %q: %q", want, command)
		}
	}

	noContainer, err := KubectlExecCommand("team-a", "web-0", "")
	if err != nil || strings.Contains(noContainer, "exec -it 'web-0' -c") {
		t.Errorf("exec without a container = %q, %v", noContainer, err)
	}
	quoted, err := KubectlExecCommand("team-a", "web-0", "it's")
	if err == nil {
		t.Errorf("an invalid container name was accepted: %q", quoted)
	}
	for _, bad := range []struct{ namespace, pod, container string }{
		{"Bad_Name", "web-0", "app"},
		{"team-a", "Web_0", "app"},
		{"team-a", "web-0", "App_Name"},
	} {
		if _, err := KubectlExecCommand(bad.namespace, bad.pod, bad.container); err == nil {
			t.Errorf("KubectlExecCommand accepted %+v", bad)
		}
	}
	if got := (Pod{Containers: []string{"app", "sidecar"},
		RunningContainers: []string{"sidecar"}}).TerminalContainer(); got != "sidecar" {
		t.Errorf("terminal container = %q, want the first running one", got)
	}
	if got := (Pod{Containers: []string{"app"}}).TerminalContainer(); got != "app" {
		t.Errorf("terminal container = %q, want the first declared one", got)
	}
}

func TestParseKubernetesListReadsPodsAndNamespaces(t *testing.T) {
	stdout := markerKubernetesBegin + "\n" +
		markerKubernetesNamespaces + "\n" +
		"W0101 warning: deprecated API\n" +
		`{"items":[{"metadata":{"name":"default"}},{"metadata":{"name":"team-a"}}]}` + "\n" +
		markerKubernetesNamespacesEnd + "\n" +
		markerKubernetesPods + "\n" +
		`{"items":[` +
		`{"metadata":{"name":"web-0","namespace":"team-a","creationTimestamp":"2025-10-01T10:00:00Z"},` +
		`"spec":{"containers":[{"name":"app"},{"name":"sidecar"}]},` +
		`"status":{"phase":"Running","containerStatuses":[` +
		`{"name":"app","restartCount":2,"state":{"running":{}}},` +
		`{"name":"sidecar","restartCount":1,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}},` +
		`{"metadata":{"name":"job-1","namespace":"team-a","creationTimestamp":"2025-09-30T10:00:00Z"},` +
		`"spec":{"containers":[{"name":"worker"}]},"status":{"phase":"Succeeded"}}` +
		`]}` + "\n" +
		markerKubernetesPodsEnd + "\n" +
		markerKubernetesEnd + "\n"

	now := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)
	inventory, err := parseKubernetesList(stdout, now)
	if err != nil {
		t.Fatalf("parse kubernetes list: %v", err)
	}
	if len(inventory.Namespaces) != 2 || inventory.Namespaces[0] != "default" ||
		inventory.Namespaces[1] != "team-a" {
		t.Errorf("namespaces = %v", inventory.Namespaces)
	}
	if inventory.Namespace != "team-a" {
		t.Errorf("effective namespace = %q, want the pod list's namespace", inventory.Namespace)
	}
	if len(inventory.Pods) != 2 {
		t.Fatalf("pods = %+v", inventory.Pods)
	}
	web := inventory.Pods[0]
	if web.Name != "web-0" || web.Namespace != "team-a" || web.Status != "CrashLoopBackOff" ||
		web.Restarts != 3 || web.Age != "2h" {
		t.Errorf("web pod = %+v", web)
	}
	if len(web.Containers) != 2 || web.Containers[0] != "app" || web.Containers[1] != "sidecar" {
		t.Errorf("web containers = %v", web.Containers)
	}
	if len(web.RunningContainers) != 1 || web.RunningContainers[0] != "app" {
		t.Errorf("web running containers = %v", web.RunningContainers)
	}
	job := inventory.Pods[1]
	if job.Status != "Completed" || job.Age != "1d" {
		t.Errorf("job pod = %+v", job)
	}

	// An empty reply is a live empty inventory. No pod named a namespace, so
	// the parser must not invent one: only the kubeconfig knows which namespace
	// kubectl actually queried.
	empty, err := parseKubernetesList(markerKubernetesBegin+"\n"+
		markerKubernetesNamespaces+"\n"+markerKubernetesNamespacesEnd+"\n"+
		markerKubernetesPods+"\n"+markerKubernetesPodsEnd+"\n"+
		markerKubernetesEnd+"\n", now)
	if err != nil || len(empty.Pods) != 0 || empty.Namespace != "" {
		t.Errorf("empty kubernetes reply = %+v, %v", empty, err)
	}
	if empty.Pods == nil {
		t.Error("the empty pod state must be an empty slice, not nil")
	}
}

func TestParseKubernetesListMapsFailuresToTypedReasons(t *testing.T) {
	namespacesForbidden := markerKubernetesBegin + "\n" +
		markerKubernetesNamespaces + "\n" + markerKubernetesError + "\n" +
		`Error from server (Forbidden): namespaces is forbidden: User "dev" cannot list resource "namespaces"` + "\n" +
		markerKubernetesNamespacesEnd + "\n" +
		markerKubernetesPods + "\n" +
		`{"items":[{"metadata":{"name":"web-0","namespace":"default"},` +
		`"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running"}}]}` + "\n" +
		markerKubernetesPodsEnd + "\n" + markerKubernetesEnd + "\n"
	inventory, err := parseKubernetesList(namespacesForbidden,
		time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("a forbidden namespace list must still load pods: %v", err)
	}
	if len(inventory.Namespaces) != 0 || len(inventory.Pods) != 1 {
		t.Errorf("inventory = %+v", inventory)
	}

	unreachable := markerKubernetesBegin + "\n" +
		markerKubernetesNamespaces + "\n" + markerKubernetesNamespacesEnd + "\n" +
		markerKubernetesPods + "\n" + markerKubernetesError + "\n" +
		"Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: connection refused\n" +
		markerKubernetesPodsEnd + "\n" + markerKubernetesEnd + "\n"
	if _, err := parseKubernetesList(unreachable, time.Now()); !hasReason(err, ReasonClusterUnreachable) {
		t.Errorf("unreachable cluster error = %v", err)
	}

	forbidden := markerKubernetesBegin + "\n" +
		markerKubernetesNamespaces + "\n" + markerKubernetesNamespacesEnd + "\n" +
		markerKubernetesPods + "\n" + markerKubernetesError + "\n" +
		"Error from server (Forbidden): pods is forbidden\n" +
		markerKubernetesPodsEnd + "\n" + markerKubernetesEnd + "\n"
	if _, err := parseKubernetesList(forbidden, time.Now()); !hasReason(err, ReasonKubernetesForbidden) {
		t.Errorf("forbidden pods error = %v", err)
	}

	if _, err := parseKubernetesList(markerKubernetesBegin+"\n"+
		markerUnavailable+"=kubectl\n"+markerKubernetesEnd+"\n", time.Now()); !hasReason(err, ReasonMissingKubectl) {
		t.Errorf("missing kubectl error = %v", err)
	}
}

func TestKubernetesAvailabilityFollowsTheProbe(t *testing.T) {
	withKubectl := parseCapabilities("__NC_OS=Linux\n__NC_HAS_kubectl=1\n")
	if ok, reason := withKubectl.Available(TabKubernetes); !ok || reason != ReasonNone {
		t.Errorf("kubectl availability = %v, %q", ok, reason)
	}
	without := parseCapabilities("__NC_OS=Linux\n__NC_HAS_docker=1\n")
	if ok, reason := without.Available(TabKubernetes); ok || reason != ReasonMissingKubectl {
		t.Errorf("missing kubectl availability = %v, %q", ok, reason)
	}
	// A Docker-only Host is unchanged: the Docker tab is still offered.
	if ok, reason := without.Available(TabDocker); !ok || reason != ReasonNone {
		t.Errorf("docker availability = %v, %q", ok, reason)
	}
}

func hasReason(err error, want Reason) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Reason == want
}

func TestServiceSamplesTheSelectedContainerRuntime(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_podman=1\n"}, nil
		case strings.Contains(command, "__NC_DOCKER_BEGIN__"):
			return ExecResult{Stdout: fakeDockerList}, nil
		default:
			return ExecResult{HasExitCode: true}, nil
		}
	}
	service := NewService(connector)
	defer service.Close()
	host := fakeHost("podman-only")

	service.SetContainerRuntime(RuntimePodman)
	if err := service.Start(host, TabDocker, 20*time.Millisecond); err != nil {
		t.Fatalf("start docker: %v", err)
	}
	waitStatus(t, service, TabDocker, StatusLive)

	snapshot := service.Snapshot()
	if snapshot.ContainerRuntime != RuntimePodman {
		t.Errorf("snapshot runtime = %q, want podman", snapshot.ContainerRuntime)
	}
	state := snapshot.Tabs[TabDocker]
	if len(state.Containers) != 2 || len(state.Images) != 2 {
		t.Errorf("podman state = %+v", state)
	}
	found := false
	for _, command := range connector.commands("podman-only") {
		if strings.Contains(command, "podman ps -a --format") {
			found = true
		}
		if strings.Contains(command, "docker ps -a") {
			t.Errorf("the podman sample ran docker: %q", command)
		}
	}
	if !found {
		t.Errorf("commands = %v, want podman ps", connector.commands("podman-only"))
	}
}

// A surface pushes the effective runtime only after it sees the probe, so the
// service itself must not sample Docker on a Host that has only podman; doing
// so would flash a false daemon error before the switcher catches up.
func TestServiceUsesTheDetectedRuntimeBeforeTheSurfacePushesIt(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_podman=1\n"}, nil
		case strings.Contains(command, "__NC_DOCKER_BEGIN__"):
			return ExecResult{Stdout: fakeDockerList}, nil
		default:
			return ExecResult{HasExitCode: true}, nil
		}
	}
	service := NewService(connector)
	defer service.Close()
	host := fakeHost("podman-unpushed")

	// Deliberately do not call SetContainerRuntime: this is the window before
	// the surface reconciles the probe into a choice.
	if err := service.Start(host, TabDocker, 20*time.Millisecond); err != nil {
		t.Fatalf("start docker: %v", err)
	}
	waitStatus(t, service, TabDocker, StatusLive)

	if got := service.ContainerRuntime(); got != RuntimePodman {
		t.Errorf("service runtime = %q, want the detected podman", got)
	}
	if got := service.Snapshot().ContainerRuntime; got != RuntimePodman {
		t.Errorf("snapshot runtime = %q, want podman", got)
	}
	for _, command := range connector.commands("podman-unpushed") {
		if strings.Contains(command, "docker ps -a") {
			t.Errorf("the first sample ran docker on a podman-only Host: %q", command)
		}
	}
}

// A cluster that briefly refuses connections is an outage, not a terminal
// fact: the tab must retry and recover without the user reopening it.
func TestServiceRetriesAnUnreachableCluster(t *testing.T) {
	success := markerKubernetesBegin + "\n" +
		markerKubernetesNamespaces + "\n" +
		`{"items":[{"metadata":{"name":"team-a"}}]}` + "\n" +
		markerKubernetesNamespacesEnd + "\n" +
		markerKubernetesPods + "\n" +
		`{"items":[{"metadata":{"name":"web-0","namespace":"team-a"},` +
		`"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running"}}]}` + "\n" +
		markerKubernetesPodsEnd + "\n" + markerKubernetesEnd + "\n"
	unreachable := markerKubernetesBegin + "\n" +
		markerKubernetesNamespaces + "\n" + markerKubernetesNamespacesEnd + "\n" +
		markerKubernetesPods + "\n" + markerKubernetesError + "\n" +
		"Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: connection refused\n" +
		markerKubernetesPodsEnd + "\n" + markerKubernetesEnd + "\n"
	failed := false
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_kubectl=1\n"}, nil
		case strings.Contains(command, markerKubernetesBegin):
			if !failed {
				failed = true
				return ExecResult{Stdout: unreachable}, nil
			}
			return ExecResult{Stdout: success}, nil
		default:
			return ExecResult{HasExitCode: true}, nil
		}
	}
	service := NewService(connector)
	defer service.Close()
	host := fakeHost("kube-flaky")

	if err := service.Start(host, TabKubernetes, 20*time.Millisecond); err != nil {
		t.Fatalf("start kubernetes: %v", err)
	}
	waitFor(t, 2*time.Second, "the cluster to recover after one outage", func() bool {
		state := service.Snapshot().Tabs[TabKubernetes]
		return state.Status == StatusLive && len(state.Kubernetes.Pods) == 1
	})
	samples := 0
	for _, command := range connector.commands("kube-flaky") {
		if strings.Contains(command, "kubectl get pods -o json") {
			samples++
		}
	}
	if samples < 2 {
		t.Errorf("samples = %d, want a retry after the outage", samples)
	}
}

func TestServiceSamplesKubernetesInTheSelectedNamespace(t *testing.T) {
	fixture := markerKubernetesBegin + "\n" +
		markerKubernetesNamespaces + "\n" +
		`{"items":[{"metadata":{"name":"default"}},{"metadata":{"name":"team-a"}}]}` + "\n" +
		markerKubernetesNamespacesEnd + "\n" +
		markerKubernetesPods + "\n" +
		`{"items":[{"metadata":{"name":"web-0","namespace":"team-a"},` +
		`"spec":{"containers":[{"name":"app"}]},` +
		`"status":{"phase":"Running","containerStatuses":[` +
		`{"name":"app","state":{"running":{}}}]}}]}` + "\n" +
		markerKubernetesPodsEnd + "\n" +
		markerKubernetesEnd + "\n"
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_kubectl=1\n"}, nil
		case strings.Contains(command, markerKubernetesBegin):
			return ExecResult{Stdout: fixture}, nil
		default:
			return ExecResult{HasExitCode: true}, nil
		}
	}
	service := NewService(connector)
	defer service.Close()
	host := fakeHost("kube")

	if err := service.Start(host, TabKubernetes, 20*time.Millisecond); err != nil {
		t.Fatalf("start kubernetes: %v", err)
	}
	waitStatus(t, service, TabKubernetes, StatusLive)
	unscoped := false
	for _, command := range connector.commands("kube") {
		if strings.Contains(command, "kubectl get pods -o json") {
			unscoped = true
		}
	}
	if !unscoped {
		t.Errorf("commands = %v, want an unscoped sample first", connector.commands("kube"))
	}

	// Selecting a namespace restarts the active tab with -n, which is the only
	// scope the command builder may add.
	if err := service.SetKubernetesNamespace("team-a"); err != nil {
		t.Fatalf("select namespace: %v", err)
	}
	waitFor(t, 2*time.Second, "the scoped pod sample", func() bool {
		for _, command := range connector.commands("kube") {
			if strings.Contains(command, "kubectl -n 'team-a' get pods -o json") {
				return service.Snapshot().Tabs[TabKubernetes].Status == StatusLive
			}
		}
		return false
	})

	snapshot := service.Snapshot()
	if snapshot.KubernetesNamespace != "team-a" {
		t.Errorf("snapshot namespace = %q", snapshot.KubernetesNamespace)
	}
	state := snapshot.Tabs[TabKubernetes]
	if len(state.Kubernetes.Namespaces) != 2 || len(state.Kubernetes.Pods) != 1 {
		t.Errorf("kubernetes state = %+v", state.Kubernetes)
	}
	if pod := state.Kubernetes.Pods[0]; pod.Name != "web-0" ||
		pod.Namespace != "team-a" || pod.Status != "Running" {
		t.Errorf("pod = %+v", pod)
	}

	// An invalid namespace is rejected before it reaches the transport.
	before := len(connector.commands("kube"))
	if err := service.SetKubernetesNamespace("Bad_Name"); err == nil {
		t.Error("an invalid namespace was accepted")
	}
	if after := len(connector.commands("kube")); after != before {
		t.Errorf("an invalid namespace ran %d command(s)", after-before)
	}
}
