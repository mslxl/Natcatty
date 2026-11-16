package sysmanager

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
)

// renderSurface lays out the manager once so tests can ask what a user can
// reach in a degraded state. Two frames let controls settle their layout.
func renderSurface(t *testing.T, state *State) *ui.Tester {
	t.Helper()
	tester := ui.NewTester(View(state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	tester.Frame()
	return tester
}

// A Host with docker and podman whose docker daemon is stopped must still be
// able to choose podman: the runtime switcher cannot live behind the live
// content gate, and choosing another engine must clear the unavailable state.
func TestFailingContainerEngineKeepsAnotherEngineSelectable(t *testing.T) {
	service := newFakeExtendedService()
	snapshot := demoExtendedSnapshot()
	snapshot.CapabilitiesHost = "alpha"
	snapshot.Capabilities = appsysmanager.Capabilities{HasDocker: true, HasPodman: true}
	snapshot.ContainerRuntime = appsysmanager.RuntimeDocker
	snapshot.Tabs[appsysmanager.TabDocker] = appsysmanager.TabState{
		Tab: appsysmanager.TabDocker, Status: appsysmanager.StatusUnavailable,
		Reason: appsysmanager.ReasonDockerDaemon, At: time.Now(),
	}
	service.snapshot = snapshot
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	state.SetTab(appsysmanager.TabDocker)
	state.Flush()

	if got := state.TabState(appsysmanager.TabDocker).Status; got != appsysmanager.StatusUnavailable {
		t.Fatalf("initial docker status = %q, want unavailable", got)
	}
	if got := state.AvailableContainerRuntimes(); !sameRuntimes(got,
		[]appsysmanager.ContainerRuntime{appsysmanager.RuntimeDocker, appsysmanager.RuntimePodman}) {
		t.Fatalf("offered runtimes = %v, want both detected engines", got)
	}

	state.SetContainerRuntime(appsysmanager.RuntimePodman)
	if got := state.ContainerRuntime(); got != appsysmanager.RuntimePodman {
		t.Fatalf("switched runtime = %q, want podman", got)
	}
	service.mu.Lock()
	selected := append([]appsysmanager.ContainerRuntime(nil), service.runtimes...)
	service.mu.Unlock()
	if len(selected) != 1 || selected[0] != appsysmanager.RuntimePodman {
		t.Errorf("service runtimes = %v, want one podman selection", selected)
	}

	state.Flush()
	if got := state.TabState(appsysmanager.TabDocker).Status; got != appsysmanager.StatusConnecting {
		t.Errorf("docker status after switching = %q, want the unavailable state cleared", got)
	}
}

// The switcher must be rendered, not merely available in state: the original
// defect returned the status panel before any Docker content was built.
func TestDegradedDockerTabRendersTheRuntimeSwitcher(t *testing.T) {
	service := newFakeExtendedService()
	snapshot := demoExtendedSnapshot()
	snapshot.CapabilitiesHost = "alpha"
	snapshot.Capabilities = appsysmanager.Capabilities{HasDocker: true, HasPodman: true}
	snapshot.ContainerRuntime = appsysmanager.RuntimeDocker
	snapshot.Tabs[appsysmanager.TabDocker] = appsysmanager.TabState{
		Tab: appsysmanager.TabDocker, Status: appsysmanager.StatusUnavailable,
		Reason: appsysmanager.ReasonDockerDaemon, At: time.Now(),
	}
	service.snapshot = snapshot
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	state.SetTab(appsysmanager.TabDocker)

	tester := renderSurface(t, state)
	if _, ok := tester.Find("Runtime"); !ok {
		t.Fatalf("the runtime switcher is not reachable in the degraded Docker tab; texts = %v",
			tester.Texts())
	}
}

// A successful namespace list followed by a refused pods list keeps the
// namespaces valid. The selector must stay reachable so the user can pick
// another namespace, and picking one must reach the service.
func TestForbiddenPodsKeepTheNamespaceSelectorReachable(t *testing.T) {
	service := newFakeExtendedService()
	snapshot := kubeSnapshot()
	snapshot.KubernetesNamespace = ""
	snapshot.Tabs[appsysmanager.TabKubernetes] = appsysmanager.TabState{
		Tab: appsysmanager.TabKubernetes, Status: appsysmanager.StatusUnavailable,
		Reason: appsysmanager.ReasonKubernetesForbidden, At: time.Now(),
		Kubernetes: appsysmanager.Kubernetes{
			Namespaces: []string{"default", "team-a"},
		},
	}
	service.snapshot = snapshot
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	state.SetTab(appsysmanager.TabKubernetes)
	state.Flush()

	if got := state.KubernetesNamespaces(); len(got) != 2 {
		t.Fatalf("namespaces = %v, want the names listed before the refusal", got)
	}

	tester := renderSurface(t, state)
	if _, ok := tester.Find("Namespace"); !ok {
		t.Fatalf("the namespace selector is not reachable after a forbidden pods list; texts = %v",
			tester.Texts())
	}

	if err := state.SetKubernetesNamespace("team-a"); err != nil {
		t.Fatalf("select namespace: %v", err)
	}
	service.mu.Lock()
	namespaces := append([]string(nil), service.namespaces...)
	service.mu.Unlock()
	if len(namespaces) != 1 || namespaces[0] != "team-a" {
		t.Errorf("service namespaces = %v, want team-a", namespaces)
	}
}
