package core

import (
	"fmt"
	"strings"
)

// The Kubernetes section markers mirror the container list's shape: each
// command's failure text lands inside its own section, and a Host without
// kubectl prints the shared unavailable marker. The manager never passes a
// kubeconfig or a credential; kubectl reads the Host's own file.
const (
	markerKubernetesBegin         = "__NC_K8S_BEGIN__"
	markerKubernetesEnd           = "__NC_K8S_END__"
	markerKubernetesNamespaces    = "__NC_K8S_NAMESPACES__"
	markerKubernetesNamespacesEnd = "__NC_K8S_NAMESPACES_END__"
	markerKubernetesPods          = "__NC_K8S_PODS__"
	markerKubernetesPodsEnd       = "__NC_K8S_PODS_END__"
	markerKubernetesError         = "__NC_K8S_ERROR__"
)

// kubernetesListCommand lists namespaces and the selected namespace's pods as
// JSON. An empty namespace omits -n so kubectl uses the namespace recorded in
// the Host's kubeconfig.
func kubernetesListCommand(namespace string) (string, error) {
	namespaceFlag := ""
	if namespace != "" {
		safe, err := sanitizeKubernetesNamespace(namespace)
		if err != nil {
			return "", err
		}
		namespaceFlag = " -n " + shellQuote(safe)
	}
	return `printf "%s\n" "` + markerKubernetesBegin + `"; ` +
		`if command -v kubectl >/dev/null 2>&1; then ` +
		`printf "%s\n" "` + markerKubernetesNamespaces + `"; ` +
		`_nc_out=$(kubectl get namespaces -o json 2>&1); _nc_status=$?; ` +
		`if [ "$_nc_status" -eq 0 ]; then printf "%s\n" "$_nc_out"; ` +
		`else printf "%s\n" "` + markerKubernetesError + `"; printf "%s\n" "$_nc_out"; fi; ` +
		`printf "%s\n" "` + markerKubernetesNamespacesEnd + `"; ` +
		`printf "%s\n" "` + markerKubernetesPods + `"; ` +
		`_nc_out=$(kubectl` + namespaceFlag + ` get pods -o json 2>&1); _nc_status=$?; ` +
		`if [ "$_nc_status" -eq 0 ]; then printf "%s\n" "$_nc_out"; ` +
		`else printf "%s\n" "` + markerKubernetesError + `"; printf "%s\n" "$_nc_out"; fi; ` +
		`printf "%s\n" "` + markerKubernetesPodsEnd + `"; ` +
		`else printf "%s\n" "` + markerUnavailable + `=kubectl"; fi; ` +
		`printf "%s\n" "` + markerKubernetesEnd + `"`, nil
}

// KubectlExecCommand returns the interactive command sent to a terminal Tab.
// The clear sequence and exec match tmux attach, so the attached client owns
// the terminal. The namespace comes from the pod's metadata, so the command is
// explicit even when the kubeconfig chooses the namespace automatically.
func KubectlExecCommand(namespace, pod, container string) (string, error) {
	podName, err := sanitizePodName(pod)
	if err != nil {
		return "", err
	}
	args := "kubectl"
	if namespace != "" {
		safe, nsErr := sanitizeKubernetesNamespace(namespace)
		if nsErr != nil {
			return "", nsErr
		}
		args += " -n " + shellQuote(safe)
	}
	args += " exec -it " + shellQuote(podName)
	if container != "" {
		safe, containerErr := sanitizeContainerName(container)
		if containerErr != nil {
			return "", containerErr
		}
		args += " -c " + shellQuote(safe)
	}
	args += ` -- sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh'`
	return `printf '\033[H\033[2J\033[3J'; exec ` + args, nil
}

// sanitizeKubernetesNamespace accepts a DNS-1123 label, the form Kubernetes
// requires of a namespace.
func sanitizeKubernetesNamespace(namespace string) (string, error) {
	return sanitizeKubernetesName(namespace, 63, false)
}

// sanitizePodName accepts a DNS-1123 subdomain, the form Kubernetes requires
// of a pod name.
func sanitizePodName(pod string) (string, error) {
	return sanitizeKubernetesName(pod, 253, true)
}

// sanitizeContainerName accepts a DNS-1123 label, the form Kubernetes
// requires of a container name.
func sanitizeContainerName(container string) (string, error) {
	return sanitizeKubernetesName(container, 63, false)
}

// sanitizeKubernetesName validates the unobtrusive character set Kubernetes
// object names use. Rejecting rather than rewriting keeps a mistyped or
// hostile value from targeting a different object.
func sanitizeKubernetesName(value string, max int, dots bool) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" || len(name) > max {
		return "", &Error{Reason: ReasonInternal,
			Cause: fmt.Errorf("invalid Kubernetes name: %q", value)}
	}
	for index, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' || (dots && r == '.'):
			if index == 0 || index == len(name)-1 {
				return "", &Error{Reason: ReasonInternal,
					Cause: fmt.Errorf("invalid Kubernetes name: %q", value)}
			}
		default:
			return "", &Error{Reason: ReasonInternal,
				Cause: fmt.Errorf("invalid Kubernetes name: %q", value)}
		}
	}
	return name, nil
}
