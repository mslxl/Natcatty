package serial

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// The dialog keeps its action row inside the card at the 720x480 minimum
// window: the fields scroll when the card reaches the height budget, while the
// warning, error, and action rows stay pinned below the scroll area.
const (
	dialogWidth           float32 = 520
	dialogField           float32 = 220
	dialogGap             float32 = 10
	dialogScrollMinHeight float32 = 120
	// dialogFixedHeight covers the rows that do not scroll: title,
	// description, a warning and an error line, the action row, and the gaps
	// between them.
	dialogFixedHeight float32 = 160
)

// View renders the serial connection dialog. It is a no-op while the dialog is
// hidden, so the shell can call it unconditionally above the other surfaces.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		if !s.Visible() {
			return
		}
		m := locales.In(c.Locale())
		t := c.Theme()

		open := true
		ui.Modal(c, &open, func() {
			ui.Column(c).Width(dialogWidth).Padding(20).Gap(dialogGap).Children(func() {
				ui.Text(c, m.SerialTitle()).FontSize(18).Bold()
				ui.Text(c, m.SerialDescription()).FontSize(12).TextColor(t.TextMuted)

				ui.Scroll(c).FillWidth().MaxHeight(formViewport(c, t)).Children(func() {
					ui.Column(c).FillWidth().Gap(dialogGap).Children(func() {
						portField(c, s, m, t)
						baudField(c, s, m, t)

						ui.Row(c).FillWidth().Gap(12).Children(func() {
							dataBitsField(c, s, m)
							stopBitsField(c, s, m)
						})

						ui.Row(c).FillWidth().Gap(12).Children(func() {
							parityField(c, s, m)
							flowField(c, s, m)
						})
					})
				})

				statusView(c, s, m, t)

				ui.Row(c).Gap(8).Children(func() {
					if ui.PrimaryButton(c, m.SerialConnect()).Key("serial-connect").Clicked() {
						_ = s.Connect()
						c.Invalidate()
					}
					if ui.Button(c, m.SerialCancel()).Key("serial-cancel").Clicked() {
						s.Hide()
						c.Invalidate()
					}
				})
			})
		})
		// Clicking outside the modal or pressing Escape clears the local open
		// flag; the dialog state is the source of truth for the next frame.
		if !open {
			s.Hide()
		}
	}
}

// formViewport is the height the scrolling fields may take. Modal caps the
// card at height-Space(10) and pads the panel and this column by Space(5) on
// each side, so the field viewport is what remains after those paddings and
// the fixed rows.
func formViewport(c *ui.Context, t *ui.Theme) float32 {
	_, height := c.Size()
	return max(dialogScrollMinHeight, height-t.Space(30)-dialogFixedHeight)
}

// statusView shows the stop-bits warning and any enumeration or connection
// failure below the scrolling fields, so they stay visible when the fields
// themselves scroll at the minimum window size.
func statusView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	warning := s.StopBitsNeedWarning()
	portsFailed := s.PortsError() != nil
	problem := s.Problem()
	if !warning && !portsFailed && problem == nil {
		return
	}
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		if warning {
			ui.Text(c, m.SerialStopBitsWarning()).FontSize(11).TextColor(t.TextMuted)
		}
		if portsFailed {
			ui.Text(c, m.SerialPortsFailed()).FontSize(12).TextColor(uitheme.DangerText(t))
		}
		if problem != nil {
			ui.Text(c, ProblemText(m, problem)).FontSize(12).TextColor(uitheme.DangerText(t))
		}
	})
}

// portField shows the detected ports as quick choices above the editable path.
// A device may be absent from enumeration, so the path stays editable.
func portField(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, m.SerialPort()).FontSize(12).TextColor(t.TextMuted)
		if ui.Button(c, m.SerialRefresh()).Key("serial-refresh").Clicked() {
			s.RefreshPorts()
			c.Invalidate()
		}
	})

	ports := s.Ports()
	if len(ports) == 0 {
		ui.Text(c, m.SerialNoPorts()).FontSize(12).TextColor(t.TextMuted)
	} else {
		portsView(c, s, ports)
	}

	path := s.Port()
	ui.TextInput(c.Key("serial-path"), &path).
		Label(m.SerialPort()).
		Placeholder(m.SerialPortPlaceholder()).
		Width(460)
	if path != s.Port() {
		s.SetPort(path)
	}
}

func portsView(c *ui.Context, s *State, ports []domainserial.PortInfo) {
	ui.Column(c).Gap(4).Children(func() {
		for _, port := range ports {
			label := port.Path
			if port.Manufacturer != "" {
				label += " — " + port.Manufacturer
			}
			var button ui.Element
			if port.Path == s.Port() {
				button = ui.PrimaryButton(c, label)
			} else {
				button = ui.Button(c, label)
			}
			button.Key("serial-port-" + port.Path).Width(460)
			if button.Clicked() {
				s.SetPort(port.Path)
				c.Invalidate()
			}
		}
	})
}

// baudField is editable so a device outside the catalog's usual rates can
// still be reached; validation and the platform capability check run on
// Connect. It is built on the base combobox, unlike the styled Combobox, which
// only commits a listed choice, but it takes the styled look and the down
// chevron so it reads as a drop-down with the other fields.
func baudField(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	options := make([]string, 0, len(domainserial.BaudRates))
	for _, rate := range domainserial.BaudRates {
		options = append(options, strconv.Itoa(rate))
	}
	baud := s.BaudRate()
	var box ui.ComboboxParts
	ui.Field(c, m.SerialBaudRate(), func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(1.5)).
			Padding(t.Space(1.5), t.Space(2.5)).Radius(t.Radius).
			Background(t.Surface).Border(1, t.Border).Children(func() {
			box = ui.ComboboxBase(c.Key("serial-baud"), &baud)
			box.Input.Grow(1).MinWidth(0).Placeholder(m.SerialBaudRatePlaceholder())
			arrow := ui.Text(c, "▾").FontSize(10).TextColor(t.TextMuted)
			arrow.OnClick(func() {
				box.SetOpen(!box.Open())
				box.Input.Focus()
			})
		})
	}).Width(dialogField)

	box.Popup(func(panel ui.Element) {
		panel.Padding(4).Background(t.Background).Border(1, t.Border).Radius(t.Radius)
		for _, rate := range options {
			item := box.Item(rate).Padding(6, 10)
			switch {
			case item.Highlighted():
				item.Background(t.Accent).TextColor(t.AccentText)
			case rate == s.BaudRate():
				item.Background(t.Surface)
			}
			item.Children(func() { ui.Text(c, rate) })
		}
	})
	if chosen, ok := box.Chosen(); ok {
		baud = chosen
	}
	if baud != s.BaudRate() {
		s.SetBaudRate(baud)
	}
}

func dataBitsField(c *ui.Context, s *State, m locales.Messages) {
	labels := []string{"5", "6", "7", "8"}
	chosen := s.DataBits()
	if !contains(labels, chosen) {
		chosen = "8"
	}
	ui.Field(c, m.SerialDataBits(), func() {
		ui.Select(c.Key("serial-data-bits"), &chosen, labels).FillWidth()
	}).Width(dialogField)
	if chosen != s.DataBits() {
		s.SetDataBits(chosen)
	}
}

func stopBitsField(c *ui.Context, s *State, m locales.Messages) {
	var labels []string
	for _, bits := range []domainserial.StopBits{
		domainserial.StopBitsOne,
		domainserial.StopBitsOneAndHalf,
		domainserial.StopBitsTwo,
	} {
		labels = append(labels, strconv.FormatFloat(float64(bits), 'g', -1, 64))
	}
	chosen := s.StopBits()
	if !contains(labels, chosen) {
		chosen = labels[0]
	}
	ui.Field(c, m.SerialStopBits(), func() {
		ui.Select(c.Key("serial-stop-bits"), &chosen, labels).FillWidth()
	}).Width(dialogField)
	if chosen != s.StopBits() {
		s.SetStopBits(chosen)
	}
}

func parityField(c *ui.Context, s *State, m locales.Messages) {
	values := []string{
		string(domainserial.ParityNone),
		string(domainserial.ParityEven),
		string(domainserial.ParityOdd),
		string(domainserial.ParityMark),
		string(domainserial.ParitySpace),
	}
	labels := []string{
		m.SerialParityNone(),
		m.SerialParityEven(),
		m.SerialParityOdd(),
		m.SerialParityMark(),
		m.SerialParitySpace(),
	}
	choice(c, m.SerialParity(), "serial-parity", s.Parity(), values, labels, s.SetParity)
}

func flowField(c *ui.Context, s *State, m locales.Messages) {
	values := []string{
		string(domainserial.FlowNone),
		string(domainserial.FlowXONXOFF),
		string(domainserial.FlowRTSCTS),
	}
	labels := []string{
		m.SerialFlowNone(),
		m.SerialFlowXONXOFF(),
		m.SerialFlowRTSCTS(),
	}
	choice(c, m.SerialFlowControl(), "serial-flow", s.FlowControl(), values, labels, s.SetFlowControl)
}

// choice renders a localized drop-down whose stored value is a stable domain
// token. A value with no label keeps the current choice rather than silently
// changing it to the first option.
func choice(c *ui.Context, label, key, current string,
	values, labels []string, set func(string)) {
	index := -1
	for i, value := range values {
		if value == current {
			index = i
			break
		}
	}
	selected := ""
	if index >= 0 {
		selected = labels[index]
	}
	ui.Field(c, label, func() {
		ui.Select(c.Key(key), &selected, labels).FillWidth()
	}).Width(dialogField)
	if index < 0 || selected == labels[index] {
		return
	}
	for i, option := range labels {
		if option == selected {
			set(values[i])
			return
		}
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
