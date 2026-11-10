package plugin

import "time"

// SourcePin is the trust-on-first-use record for one plugin id and source.
// The registry is a distribution index, not a trust anchor: a package whose
// source identity no longer matches the pin requires explicit confirmation.
type SourcePin struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	FirstSeen time.Time `json:"first_seen"`
}

// RegistryPreferences is the persisted registry policy: automatic updates are
// off by default, stable is the default channel, and beta is opt-in per plugin.
type RegistryPreferences struct {
	AutomaticUpdates bool               `json:"automatic_updates"`
	Channels         map[string]Channel `json:"channels,omitempty"`
}

// ChannelFor returns the channel the user opted into for a plugin, defaulting
// to stable. An unknown persisted value falls back to stable.
func (p RegistryPreferences) ChannelFor(id string) Channel {
	if channel, ok := p.Channels[id]; ok && channel.Valid() {
		return channel
	}
	return ChannelStable
}

// SetChannel records a per-plugin channel choice.
func (p *RegistryPreferences) SetChannel(id string, channel Channel) {
	if !channel.Valid() {
		channel = ChannelStable
	}
	if p.Channels == nil {
		p.Channels = make(map[string]Channel)
	}
	p.Channels[id] = channel
}
