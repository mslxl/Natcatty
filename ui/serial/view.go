package serial

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	"github.com/mslxl/natcatty/ui/locales"
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
			ui.Column(c).Width(520).Padding(20).Gap(10).Children(func() {
				ui.Text(c, m.SerialTitle()).FontSize(18).Bold()
				ui.Text(c, m.SerialDescription()).FontSize(12).TextColor(t.TextMuted)

				portField(c, s, m, t)
				baudField(c, s, m, t)

				ui.Row(c).Gap(12).Children(func() {
					dataBitsField(c, s, m)
					stopBitsField(c, s, m)
				})
				if s.StopBitsNeedWarning() {
					ui.Text(c, m.SerialStopBitsWarning()).FontSize(11).TextColor(t.TextMuted)
				}

				ui.Row(c).Gap(12).Children(func() {
					parityField(c, s, m)
					flowField(c, s, m)
				})

				if s.PortsError() != nil {
					ui.Text(c, m.SerialPortsFailed()).FontSize(12).TextColor(t.Danger)
				}
				if err := s.Problem(); err != nil {
					ui.Text(c, ProblemText(m, err)).TextColor(t.Danger)
				}

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
// Connect. The base combobox updates the bound text while typing, unlike the
// styled Combobox, which only commits a listed choice.
func baudField(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	options := make([]string, 0, len(domainserial.BaudRates))
	for _, rate := range domainserial.BaudRates {
		options = append(options, strconv.Itoa(rate))
	}
	baud := s.BaudRate()
	box := ui.ComboboxBase(c.Key("serial-baud"), &baud)
	box.Input.Label(m.SerialBaudRate()).
		Placeholder(m.SerialBaudRatePlaceholder()).
		Width(240)
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
	ui.Select(c.Key("serial-data-bits"), &chosen, labels).Label(m.SerialDataBits()).Width(220)
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
	ui.Select(c.Key("serial-stop-bits"), &chosen, labels).Label(m.SerialStopBits()).Width(220)
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
	ui.Select(c.Key(key), &selected, labels).Label(label).Width(220)
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
