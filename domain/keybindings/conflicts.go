package keybindings

// MenuShortcut is one accelerator the native menu holds. The app passes the
// menu's live accelerators so conflict detection covers a binding that would
// shadow a real menu command.
type MenuShortcut struct {
	ID          string
	Label       string
	Accelerator string
	// Derived is true when the accelerator was produced from the same
	// binding catalog. Such an entry is the binding itself, not a foreign
	// command, and must not be reported as a conflict with it.
	Derived bool
}

// Conflict is one chord claimed more than once in the active scheme.
type Conflict struct {
	Chord    string
	Bindings []Binding
	Menu     []MenuShortcut
}

// BindingIDs returns the conflicting binding IDs.
func (c Conflict) BindingIDs() []string {
	ids := make([]string, len(c.Bindings))
	for i, binding := range c.Bindings {
		ids[i] = binding.ID
	}
	return ids
}

// HasMenu reports whether a menu accelerator is part of the conflict.
func (c Conflict) HasMenu() bool { return len(c.Menu) > 0 }

// FindConflicts reports every chord claimed by two or more active bindings,
// or by an active binding and a foreign menu accelerator. Disabled bindings
// never participate because they are not in active.
func FindConflicts(active []Active, menus []MenuShortcut, goos string) []Conflict {
	type entry struct {
		bindings []Binding
		menus    []MenuShortcut
		chord    string
	}
	entries := make([]entry, 0)

	for i, candidate := range active {
		for _, other := range active[i+1:] {
			if !candidate.Parsed.Overlaps(other.Parsed) {
				continue
			}
			// Terminal and SFTP bindings are local to their own surface.
			// The original ships Command+C for both terminal copy and
			// SFTP copy; they never shadow each other, so they are not a
			// conflict. A duplicate within one context still is. A global
			// binding (tabs, navigation, app) does reach both surfaces, so
			// it always conflicts with a local binding of the same chord:
			// the terminal or SFTP surface would silently keep the key.
			left, right := conflictDomain(candidate.Binding), conflictDomain(other.Binding)
			if left != right && left != CategoryApp && right != CategoryApp {
				continue
			}
			chord := candidate.Chord
			if candidate.Parsed.Range || candidate.Parsed.Arrows {
				chord = candidate.Chord
			} else if other.Parsed.Range || other.Parsed.Arrows {
				chord = other.Chord
			}
			entries = append(entries, entry{
				bindings: []Binding{candidate.Binding, other.Binding},
				chord:    chord,
			})
		}
		for _, menu := range menus {
			if menu.Derived || menu.Accelerator == "" {
				continue
			}
			parsed, ok := AcceleratorChord(menu.Accelerator, goos)
			if !ok || !candidate.Parsed.Overlaps(parsed) {
				continue
			}
			entries = append(entries, entry{
				bindings: []Binding{candidate.Binding},
				menus:    []MenuShortcut{menu},
				chord:    candidate.Chord,
			})
		}
	}

	// Group identical reports so the UI shows one row per chord.
	seen := make(map[string]int)
	conflicts := make([]Conflict, 0, len(entries))
	merge := func(index int, e entry) {
		conflicts[index].Chord = e.chord
		for _, binding := range e.bindings {
			if !containsBinding(conflicts[index].Bindings, binding.ID) {
				conflicts[index].Bindings = append(conflicts[index].Bindings, binding)
			}
		}
		for _, menu := range e.menus {
			if !containsMenu(conflicts[index].Menu, menu.ID) {
				conflicts[index].Menu = append(conflicts[index].Menu, menu)
			}
		}
	}
	for _, e := range entries {
		key := e.chord
		if index, ok := seen[key]; ok {
			merge(index, e)
			continue
		}
		seen[key] = len(conflicts)
		conflicts = append(conflicts, Conflict{Chord: e.chord})
		merge(len(conflicts)-1, e)
	}
	return conflicts
}

func containsBinding(bindings []Binding, id string) bool {
	for _, binding := range bindings {
		if binding.ID == id {
			return true
		}
	}
	return false
}

func containsMenu(menus []MenuShortcut, id string) bool {
	for _, menu := range menus {
		if menu.ID == id {
			return true
		}
	}
	return false
}

// conflictDomain partitions bindings by where they are dispatched. A
// terminal binding and an SFTP binding may share a chord because only one of
// those surfaces is focused at a time.
func conflictDomain(binding Binding) Category {
	switch binding.Category {
	case CategoryTerminal:
		return CategoryTerminal
	case CategorySFTP:
		return CategorySFTP
	default:
		return CategoryApp
	}
}
