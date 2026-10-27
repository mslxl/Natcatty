package settings

import (
	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// associationsSection renders the default opener and the per-extension list.
// It sits inside the SFTP & Editor area because the settings group is the
// same one the surface reads.
func associationsSection(c *ui.Context, prefs *PreferencesState,
	state *AssociationsState, m locales.Messages, t *ui.Theme) {
	if state == nil {
		return
	}
	defaultRow(c, prefs, state, m, t)
	ui.Text(c, m.SettingsFileAssociations()).Bold()
	ui.Text(c, m.SettingsFileAssociationsDescription()).FontSize(11).TextColor(t.TextMuted)
	associationsList(c, state, m)
	associationsForm(c, state, m)
}

func defaultRow(c *ui.Context, prefs *PreferencesState, state *AssociationsState,
	m locales.Messages, t *ui.Theme) {
	build := func() {
		kinds, labels := openerChoices(m)
		current := labels[indexOfOpeners(kinds, state.DefaultOpener().Kind)]
		if ui.Select(c, &current, labels).Width(200).Changed() {
			state.SelectDefaultKind(kinds[indexOf(labels, current)])
			c.Invalidate()
		}
		if state.DefaultOpener().Kind == domain.OpenerExternal {
			label := m.SettingsChooseApplication()
			if name := state.DefaultOpener().Application.Name; name != "" {
				label = name
			}
			if ui.Button(c, label).Clicked() {
				state.ChooseDefaultApplication()
				c.Invalidate()
			}
		}
	}
	if prefs != nil {
		fieldRow(c, prefs, m, t, domain.FieldDefaultOpener, m.SettingsDefaultOpener(), build)
	} else {
		ui.Field(c, m.SettingsDefaultOpener(), func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(build)
		})
	}
	ui.Text(c, m.SettingsDefaultOpenerDescription()).FontSize(11).TextColor(t.TextMuted)
	associationProblem(c, state, m, t)
}

func associationsList(c *ui.Context, state *AssociationsState, m locales.Messages) {
	list := state.Associations()
	if len(list) == 0 {
		ui.Text(c, m.SettingsAssociationEmpty()).FontSize(12).TextColor(c.Theme().TextMuted)
		return
	}
	for _, association := range list {
		association := association
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "."+association.Extension).Width(110).FontSize(13)
			ui.Text(c, openerLabel(m, association.Kind, association.Application)).
				Grow(1).MinWidth(0).FontSize(13)
			if ui.Button(c, m.SettingsAssociationEdit()).FontSize(11).Clicked() {
				state.BeginEdit(association.Extension)
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsAssociationRemove()).FontSize(11).Clicked() {
				state.Remove(association.Extension)
				c.Invalidate()
			}
		})
	}
}

func associationsForm(c *ui.Context, state *AssociationsState, m locales.Messages) {
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
		ui.Field(c, m.SettingsAssociationExtension(), func() {
			ui.TextInput(c, state.Extension()).
				Placeholder(m.SettingsAssociationExtensionPlaceholder()).
				Width(140)
		})
		ui.Field(c, m.SettingsAssociationOpener(), func() {
			kinds, labels := openerChoices(m)
			current := labels[indexOfOpeners(kinds, state.Kind())]
			if ui.Select(c, &current, labels).Width(200).Changed() {
				state.SelectKind(kinds[indexOf(labels, current)])
				c.Invalidate()
			}
		})
		if state.Kind() == domain.OpenerExternal {
			label := m.SettingsChooseApplication()
			if name := state.Application().Name; name != "" {
				label = name
			}
			if ui.Button(c, label).Clicked() {
				state.ChooseAssociationApplication()
				c.Invalidate()
			}
		}
	})
	if editing := state.Editing(); editing != "" {
		ui.Text(c, m.SettingsAssociationEditing(editing)).FontSize(11).TextColor(c.Theme().TextMuted)
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		label := m.SettingsAssociationAdd()
		if state.Editing() != "" {
			label = m.SettingsAssociationSave()
		}
		if ui.Button(c, label).Clicked() {
			state.Save()
			c.Invalidate()
		}
		if state.Editing() != "" {
			if ui.Button(c, m.SettingsCancel()).Clicked() {
				state.CancelEdit()
				c.Invalidate()
			}
		}
	})
	associationProblem(c, state, m, c.Theme())
}

func associationProblem(c *ui.Context, state *AssociationsState, m locales.Messages, t *ui.Theme) {
	var message string
	switch state.Problem() {
	case AssociationProblemInvalidExtension:
		message = m.SettingsAssociationInvalid()
	case AssociationProblemApplicationRequired:
		message = m.SettingsAssociationNeedApp()
	case AssociationProblemSaveFailed:
		message = m.SettingsSaveFailed()
	}
	if message != "" {
		ui.Text(c, message).FontSize(12).TextColor(uitheme.DangerText(t))
	}
}

func openerLabel(m locales.Messages, kind domain.OpenerKind,
	application domain.ExternalApplication) string {
	switch kind {
	case domain.OpenerInternal:
		return m.SettingsOpenerInternal()
	case domain.OpenerExternal:
		if application.Name != "" {
			return application.Name
		}
		return m.SettingsOpenerExternal()
	default:
		return m.SettingsOpenerSystem()
	}
}

func openerChoices(m locales.Messages) ([]domain.OpenerKind, []string) {
	return []domain.OpenerKind{
		domain.OpenerInternal, domain.OpenerSystem, domain.OpenerExternal,
	}, []string{
		m.SettingsOpenerInternal(), m.SettingsOpenerSystem(), m.SettingsOpenerExternal(),
	}
}

func indexOfOpeners(values []domain.OpenerKind, value domain.OpenerKind) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 1
}
