package core

// ContainerRuntime is one container engine the Docker tab can drive. The
// engine name is also the remote binary, so every command template takes the
// same shape with a different executable.
type ContainerRuntime string

const (
	RuntimeDocker  ContainerRuntime = "docker"
	RuntimePodman  ContainerRuntime = "podman"
	RuntimeNerdctl ContainerRuntime = "nerdctl"
)

// ContainerRuntimes lists the engines in the order the switcher offers them.
// The order is stable regardless of which ones the Host has, so a Host with
// only Docker keeps its historical single-runtime behavior.
var ContainerRuntimes = []ContainerRuntime{RuntimeDocker, RuntimePodman, RuntimeNerdctl}

// NormalizeContainerRuntime returns a known engine, defaulting to Docker for
// an empty or unknown value.
func NormalizeContainerRuntime(runtime ContainerRuntime) ContainerRuntime {
	for _, known := range ContainerRuntimes {
		if runtime == known {
			return runtime
		}
	}
	return RuntimeDocker
}

// Binary returns the remote executable for the engine.
func (r ContainerRuntime) Binary() string {
	return string(NormalizeContainerRuntime(r))
}

// ContainerRuntimes returns the engines present on the Host, in switcher
// order. A Host with only Docker returns exactly Docker.
func (c Capabilities) ContainerRuntimes() []ContainerRuntime {
	detected := make([]ContainerRuntime, 0, len(ContainerRuntimes))
	for _, runtime := range ContainerRuntimes {
		switch runtime {
		case RuntimeDocker:
			if c.HasDocker {
				detected = append(detected, runtime)
			}
		case RuntimePodman:
			if c.HasPodman {
				detected = append(detected, runtime)
			}
		case RuntimeNerdctl:
			if c.HasNerdctl {
				detected = append(detected, runtime)
			}
		}
	}
	return detected
}

// Offers reports whether the probe found the engine.
func (c Capabilities) Offers(runtime ContainerRuntime) bool {
	for _, detected := range c.ContainerRuntimes() {
		if detected == runtime {
			return true
		}
	}
	return false
}

// FirstContainerRuntime returns the first detected engine, or Docker when the
// Host has none. The fallback keeps a command well-formed for the degraded
// state the probe reports separately.
func (c Capabilities) FirstContainerRuntime() ContainerRuntime {
	if detected := c.ContainerRuntimes(); len(detected) > 0 {
		return detected[0]
	}
	return RuntimeDocker
}
