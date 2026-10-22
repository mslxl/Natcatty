package keybindings

// Overrides stores only a user's deviations from the catalog: binding ID to
// scheme to chord. An explicit Disabled entry is a real choice and is kept.
type Overrides map[string]map[string]string

// Normalize returns a copy with invalid schemes, invalid IDs, and empty
// chords removed.
func (o Overrides) Normalize() Overrides {
	out := make(Overrides, len(o))
	for id, schemes := range o {
		if _, ok := Lookup(id); !ok {
			continue
		}
		clean := make(map[string]string, len(schemes))
		for scheme, chord := range schemes {
			if _, ok := ParseScheme(scheme); !ok || scheme == string(SchemeDisabled) {
				continue
			}
			if chord == "" {
				continue
			}
			clean[scheme] = chord
		}
		if len(clean) > 0 {
			out[id] = clean
		}
	}
	return out
}

// Set returns a copy with id's override for scheme set to chord.
func (o Overrides) Set(id string, scheme Scheme, chord string) Overrides {
	out := o.clone()
	if scheme != SchemeMac && scheme != SchemePC {
		return out
	}
	if out[id] == nil {
		out[id] = make(map[string]string)
	}
	out[id][string(scheme)] = chord
	return out
}

// Reset returns a copy with id's override for scheme removed.
func (o Overrides) Reset(id string, scheme Scheme) Overrides {
	out := o.clone()
	schemes, ok := out[id]
	if !ok {
		return out
	}
	delete(schemes, string(scheme))
	if len(schemes) == 0 {
		delete(out, id)
	}
	return out
}

// Has reports whether id has an override for scheme.
func (o Overrides) Has(id string, scheme Scheme) bool {
	schemes, ok := o[id]
	if !ok {
		return false
	}
	_, ok = schemes[string(scheme)]
	return ok
}

func (o Overrides) clone() Overrides {
	out := make(Overrides, len(o))
	for id, schemes := range o {
		copyOfSchemes := make(map[string]string, len(schemes))
		for scheme, chord := range schemes {
			copyOfSchemes[scheme] = chord
		}
		out[id] = copyOfSchemes
	}
	return out
}

// EffectiveChord returns the chord that fires for binding under scheme,
// after applying overrides. It returns Disabled for a scheme that is off.
func EffectiveChord(binding Binding, scheme Scheme, overrides Overrides) string {
	if scheme != SchemeMac && scheme != SchemePC {
		return Disabled
	}
	if schemes, ok := overrides[binding.ID]; ok {
		if chord, ok := schemes[string(scheme)]; ok {
			return chord
		}
	}
	return DefaultChord(binding, scheme)
}

// Active is one binding that can fire in the active scheme.
type Active struct {
	Binding Binding
	Chord   string
	Parsed  Chord
}

// ActiveBindings returns every binding that fires under scheme, in catalog
// order. A disabled scheme, a Disabled default, and an explicit Disabled
// override all remove a binding.
func ActiveBindings(catalog []Binding, scheme Scheme, overrides Overrides) []Active {
	if scheme != SchemeMac && scheme != SchemePC {
		return nil
	}
	active := make([]Active, 0, len(catalog))
	for _, binding := range catalog {
		chord := EffectiveChord(binding, scheme, overrides)
		if chord == "" || chord == Disabled {
			continue
		}
		parsed, ok := ParseChord(chord)
		if !ok {
			continue
		}
		active = append(active, Active{Binding: binding, Chord: chord, Parsed: parsed})
	}
	return active
}
