package builtin

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID:  "net.ping",
		New: func() lens.Lens { return &netPing{} },
	})
}

// netPing turns ping's replies into a bar per round trip, a loss badge and a
// summary line. Timeouts count as a zero-height bar so the picture shows the
// gaps.
type netPing struct{}

type pingSample struct {
	seq     int
	rtt     float64
	timeout bool
}

type pingState struct {
	samples               []pingSample
	addr                  string
	timeouts              int
	transmitted, received int
	loss                  float64
	hasLoss               bool
	min, avg, max         float64
	hasStats              bool
	any                   bool
}

func (p *netPing) ID() string { return "net.ping" }

func (p *netPing) Match(cmd model.Cmd) bool {
	return cmd.Program == "ping" || cmd.Program == "ping6"
}

func (p *netPing) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &pingState{}, nil
}

func (p *netPing) Line(state any, l model.Line) error {
	st, ok := state.(*pingState)
	if !ok {
		return errBadState
	}
	// Unix banner: "PING example.com (93.184.216.34): 56 data bytes". The guard on
	// the parenthesised address keeps "Ping statistics for …" out of it.
	if f := strings.Fields(l.Text); len(f) >= 3 && strings.EqualFold(f[0], "PING") &&
		strings.HasPrefix(f[2], "(") {
		st.addr = strings.TrimSuffix(f[1], ":")
	}
	if pingWindowsLine(st, strings.TrimSpace(l.Text)) {
		return nil
	}
	st.observe(l.Text)
	return nil
}

func (p *netPing) Finish(state any, status int) error { return nil }

func (p *netPing) Event(state any, ev lens.Event) error { return nil }

func (st *pingState) observe(text string) {
	if text == "" {
		return
	}
	if strings.Contains(text, "packets transmitted") {
		st.transmitted = firstInt(text)
		if i := strings.Index(text, "received"); i > 0 {
			st.received = lastInt(text[:i])
		}
		if i := strings.Index(text, "% packet loss"); i > 0 {
			if f, ok := lastFloat(text[:i]); ok {
				st.loss = f
				st.hasLoss = true
			}
		}
		st.hasStats = true
		st.any = true
		return
	}
	if strings.Contains(text, "min/avg/max") {
		body := text
		if i := strings.IndexByte(body, '='); i >= 0 {
			body = body[i+1:]
		}
		if nums := parseFloats(body); len(nums) >= 3 {
			st.min, st.avg, st.max = nums[0], nums[1], nums[2]
			st.hasStats = true
			st.any = true
		}
		return
	}
	// A timeout names icmp_seq too, so check it before the reply form.
	if isPingTimeout(text) {
		st.samples = append(st.samples, pingSample{seq: firstInt(text), timeout: true})
		st.timeouts++
		st.any = true
		return
	}
	if seq, rtt, ok := parsePingReply(text); ok {
		st.samples = append(st.samples, pingSample{seq: seq, rtt: rtt})
		st.any = true
	}
}

// parsePingReply reads `icmp_seq=N ... time=X ms` (and the `time<1 ms` form). Busybox
// and toybox print `seq=N` instead of `icmp_seq=N`; the same reply otherwise, so both
// spellings name the sample and keep the bars in order.
func parsePingReply(text string) (seq int, rtt float64, ok bool) {
	seq = -1
	for _, key := range []string{"icmp_seq=", "icmp_seq ", "seq=", "seq "} {
		if i := strings.Index(text, key); i >= 0 {
			seq = leadingInt(text[i+len(key):])
			break
		}
	}
	hasTime := false
	for _, key := range []string{"time=", "time<"} {
		if i := strings.Index(text, key); i >= 0 {
			if f, ok := leadingFloat(text[i+len(key):]); ok {
				rtt, hasTime = f, true
				break
			}
		}
	}
	if seq < 0 && !hasTime {
		return 0, 0, false
	}
	if seq < 0 {
		seq = 0
	}
	return seq, rtt, true
}

func isPingTimeout(text string) bool {
	return strings.Contains(text, "timeout for icmp_seq") ||
		strings.Contains(text, "no answer yet for icmp_seq") ||
		strings.Contains(text, "Destination Host Unreachable")
}

// lastInt is the last integer in s, or 0.
func lastInt(s string) int {
	nums := parseFloats(s)
	if len(nums) == 0 {
		return 0
	}
	return int(nums[len(nums)-1])
}

// lastFloat is the last number in s.
func lastFloat(s string) (float64, bool) {
	nums := parseFloats(s)
	if len(nums) == 0 {
		return 0, false
	}
	return nums[len(nums)-1], true
}

func (p *netPing) View(state any) (lens.Node, error) {
	st, ok := state.(*pingState)
	if !ok {
		return nil, errBadState
	}
	if !st.any {
		return nil, nil
	}
	tone := toneSuccess
	switch {
	case st.hasLoss && st.loss >= 5:
		tone = toneError
	case st.hasLoss && st.loss > 0:
		tone = toneWarning
	}

	var spans []lens.Span
	if st.hasStats && st.transmitted > 0 {
		spans = append(spans, lens.Span{
			Text:  fmt.Sprintf("%d transmitted, %d received", st.transmitted, st.received),
			Style: "num",
		})
	}
	if st.hasLoss {
		if len(spans) > 0 {
			spans = append(spans, lens.Span{Text: " · ", Style: toneMuted})
		}
		spans = append(spans, lens.Span{Text: fmt.Sprintf("%.1f%% loss", st.loss), Style: tone})
	}
	if st.min > 0 || st.avg > 0 || st.max > 0 {
		if len(spans) > 0 {
			spans = append(spans, lens.Span{Text: " · ", Style: toneMuted})
		}
		spans = append(spans, lens.Span{
			Text:  fmt.Sprintf("rtt min/avg/max %.3f/%.3f/%.3f ms", st.min, st.avg, st.max),
			Style: "num",
		})
	}
	if len(spans) == 0 {
		spans = append(spans, lens.Span{Text: "ping", Style: toneMuted})
	}

	children := []lens.Node{lens.Text{Spans: spans}}
	// Terminal's net.ping span fields are `Addr/timeout/Current/Min/Max/StdDev`
	// (strings near 149976960); show them with those names.
	if kv := pingStats(st); len(kv.Items) > 0 {
		children = append(children, kv)
	}
	if len(st.samples) > 0 {
		series := make([]lens.Series, 0, len(st.samples))
		for _, s := range st.samples {
			label := "#" + strconv.Itoa(s.seq)
			if s.timeout {
				label += " timeout"
			}
			series = append(series, lens.Series{Label: label, Value: s.rtt})
		}
		children = append(children, lens.Bars{Series: series})
	}
	badge := "0% loss"
	if st.hasLoss {
		badge = fmt.Sprintf("%.0f%% loss", st.loss)
	}
	children = append(children, lens.Badge{Text: badge, Tone: tone})
	return lens.Cols(children...), nil
}

// pingStats builds the Addr/Current/Min/Max/StdDev rows Terminal names for this
// lens (strings near 149976960).
func pingStats(st *pingState) lens.KV {
	var kv lens.KV
	add := func(k, v string) {
		kv.Items = append(kv.Items, [2][]lens.Span{
			{{Text: k, Style: "muted"}},
			{{Text: v, Style: "num"}},
		})
	}
	add("Addr", st.addr)
	if st.timeouts > 0 {
		kv.Items = append(kv.Items, [2][]lens.Span{
			{{Text: "timeout", Style: "muted"}},
			{{Text: strconv.Itoa(st.timeouts), Style: "error num"}},
		})
	}
	var rtts []float64
	current := ""
	for _, s := range st.samples {
		if s.timeout {
			continue
		}
		rtts = append(rtts, s.rtt)
		current = fmt.Sprintf("%.3f ms", s.rtt)
	}
	if current != "" {
		add("Current", current)
	}
	if len(rtts) > 0 {
		min, max, sum := rtts[0], rtts[0], 0.0
		for _, v := range rtts {
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
			sum += v
		}
		avg := sum / float64(len(rtts))
		varsum := 0.0
		for _, v := range rtts {
			d := v - avg
			varsum += d * d
		}
		add("Min", fmt.Sprintf("%.3f ms", min))
		add("Max", fmt.Sprintf("%.3f ms", max))
		add("StdDev", fmt.Sprintf("%.3f ms", math.Sqrt(varsum/float64(len(rtts)))))
	}
	return kv
}

// pingWindowsLine parses the shape Windows' ping prints (`Reply from … time=1ms`,
// `Packets: Sent = …`). Terminal's evidenced fields are the Unix ones; this is an
// extension so the same lens works on the platform Terminal ships for too.
func pingWindowsLine(st *pingState, t string) bool {
	switch {
	case strings.HasPrefix(t, "Pinging "):
		f := strings.Fields(t)
		if len(f) >= 2 {
			st.addr = f[1]
		}
		st.any = true
		return true
	case strings.HasPrefix(t, "Reply from "):
		rtt := 0.0
		// Windows prints either "time=12ms" or "time<1ms"; the second form has no
		// '=', so looking only for "time=" left every sub-millisecond reply at zero
		// (zero-height bars and a missing rtt summary).
		rest, less := "", false
		if i := strings.Index(t, "time="); i >= 0 {
			rest = t[i+len("time="):]
		} else if i := strings.Index(t, "time<"); i >= 0 {
			rest, less = t[i+len("time<"):], true
		}
		if rest != "" {
			if j := strings.Index(rest, "ms"); j > 0 {
				rest = rest[:j]
			}
			if v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64); err == nil {
				rtt = v
				if less {
					rtt = v / 2 // "time<1ms": between 0 and the bound
				}
			}
		}
		st.samples = append(st.samples, pingSample{seq: len(st.samples) + 1, rtt: rtt})
		st.received++
		st.any = true
		return true
	case strings.HasPrefix(t, "Request timed out"):
		st.samples = append(st.samples, pingSample{seq: len(st.samples) + 1, timeout: true})
		st.timeouts++
		st.any = true
		return true
	case strings.HasPrefix(t, "PING: transmit failed"), strings.Contains(t, "transmit failed"):
		st.samples = append(st.samples, pingSample{seq: len(st.samples) + 1, timeout: true})
		st.timeouts++
		st.any = true
		return true
	case strings.HasPrefix(t, "Packets: Sent") || strings.Contains(t, "Packets: Sent ="):
		st.transmitted = pingLabelInt(t, "Sent =")
		st.received = pingLabelInt(t, "Received =")
		lost := pingLabelInt(t, "Lost =")
		if st.transmitted > 0 {
			st.loss = float64(lost) / float64(st.transmitted) * 100
			st.hasLoss = true
		}
		st.any = true
		return true
	case strings.Contains(t, "Minimum = ") && strings.Contains(t, "Maximum = "):
		nums := parseFloats(t)
		if len(nums) >= 3 {
			st.min, st.max, st.avg = nums[0], nums[1], nums[2]
			st.hasStats = true
			st.any = true
		}
		return true
	}
	return false
}

// pingLabelInt reads the integer after a "Sent = " style label.
func pingLabelInt(t, label string) int {
	i := strings.Index(t, label)
	if i < 0 {
		return 0
	}
	rest := strings.TrimSpace(t[i+len(label):])
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		return 0
	}
	return n
}
