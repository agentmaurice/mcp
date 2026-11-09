package interchange

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // IANA database embedded: TZID rendering does not depend on the host
)

// ICSInput is the typed data of the ics output.
type ICSInput struct {
	Name   string     `json:"name,omitempty"`
	Method string     `json:"method,omitempty"` // PUBLISH (default), REQUEST, CANCEL
	Events []ICSEvent `json:"events"`
}

// ICSEvent is one VEVENT.
type ICSEvent struct {
	UID             string     `json:"uid,omitempty"`
	DTStamp         string     `json:"dtstamp,omitempty"` // RFC 3339 with zone; default: the start, in UTC
	Start           string     `json:"start"`
	End             string     `json:"end,omitempty"`
	DurationMinutes *int       `json:"duration_minutes,omitempty"` // timed events only
	DurationDays    *int       `json:"duration_days,omitempty"`    // all-day events only, rendered P<n>D
	AllDay          bool       `json:"all_day,omitempty"`
	Timezone        string     `json:"timezone,omitempty"` // IANA name, or UTC
	Summary         string     `json:"summary"`
	Description     string     `json:"description,omitempty"`
	Location        string     `json:"location,omitempty"`
	URL             string     `json:"url,omitempty"`
	Status          string     `json:"status,omitempty"`
	Organizer       *Person    `json:"organizer,omitempty"`
	Attendees       []Attendee `json:"attendees,omitempty"`
	RRule           *RRule     `json:"rrule,omitempty"`
}

// Person is an organizer.
type Person struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// Attendee is one ATTENDEE.
type Attendee struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
	Role  string `json:"role,omitempty"` // REQ-PARTICIPANT (default), OPT-PARTICIPANT, CHAIR
	RSVP  bool   `json:"rsvp,omitempty"`
}

// RRule is a simple recurrence rule.
type RRule struct {
	Freq     string   `json:"freq"`
	Interval int      `json:"interval,omitempty"`
	Count    int      `json:"count,omitempty"`
	Until    string   `json:"until,omitempty"`
	ByDay    []string `json:"by_day,omitempty"`
}

var (
	tzName    = regexp.MustCompile(`^[A-Za-z0-9_+\-]+(/[A-Za-z0-9_+\-]+)*$`)
	byDayRe   = regexp.MustCompile(`^(MO|TU|WE|TH|FR|SA|SU)$`)
	icsUTC    = "20060102T150405Z"
	icsLocal  = "20060102T150405"
	icsDate   = "20060102"
	localForm = []string{"2006-01-02T15:04:05", "2006-01-02T15:04"}
)

// resolved is a parsed point in time.
type resolved struct {
	t     time.Time
	date  bool
	zoned bool // rendered as TZID=<zone>:local
	local bool // given as a local time (its meaning is the first occurrence)
	zone  string
	loc   *time.Location
}

func (r resolved) render() (params []string, value string) {
	switch {
	case r.date:
		return []string{"VALUE=DATE"}, r.t.Format(icsDate)
	case r.zoned && r.ambiguous():
		// The local time of this instant is repeated by a clock change: RFC 5545 would read
		// the first occurrence, so the instant is written in UTC to keep it exact.
		return nil, r.t.UTC().Format(icsUTC)
	case r.zoned:
		return []string{"TZID=" + paramValue(r.zone)}, r.t.In(r.loc).Format(icsLocal)
	default:
		return nil, r.t.UTC().Format(icsUTC)
	}
}

// localInstants returns the distinct instants, in order, whose wall clock in loc
// reads like wall (fields in UTC; anchor is a nearby instant): none (skipped time), one, or two (repeated time).
func localInstants(wall, anchor time.Time, loc *time.Location) []time.Time {
	y, mo, d := wall.Date()
	h, mi, sec := wall.Clock()
	want := wall.Format(icsLocal)
	var out []time.Time
	for _, probe := range []time.Time{anchor.Add(-26 * time.Hour), anchor, anchor.Add(26 * time.Hour)} {
		_, off := probe.In(loc).Zone()
		c := time.Date(y, mo, d, h, mi, sec, 0, time.FixedZone("", off))
		if c.In(loc).Format(icsLocal) != want {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o.Equal(c)
		}
		if !dup {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// ambiguous reports whether the local time of an instant given with a zone
// designator is repeated, so that TZID rendering would not denote this instant.
func (r resolved) ambiguous() bool {
	if r.local {
		return false // the first occurrence is what the local form means
	}
	l := r.t.In(r.loc)
	inst := localInstants(time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC), r.t, r.loc)
	return len(inst) > 1 && !inst[0].Equal(r.t)
}

func fractionErr(field, s string) error {
	return dataErr("%s %q has a fraction of second, which iCalendar cannot represent", field, s)
}

func loadZone(name string) (*time.Location, error) {
	if !tzName.MatchString(name) || len(name) > 64 || name == "Local" {
		return nil, dataErr("timezone %q is not an IANA zone name", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, dataErr("timezone %q is unknown", name)
	}
	return loc, nil
}

// parseMoment parses a start or end. A value with a zone designator is an
// instant; a local value needs the event timezone.
func parseMoment(field, s string, allDay bool, zone string, loc *time.Location) (resolved, error) {
	if allDay {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return resolved{}, dataErr("%s %q is not a date (YYYY-MM-DD) for an all-day event", field, s)
		}
		return resolved{t: t, date: true}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		if t.Nanosecond() != 0 {
			return resolved{}, fractionErr(field, s)
		}
		if zone != "" && zone != "UTC" {
			return resolved{t: t, zoned: true, zone: zone, loc: loc}, nil
		}
		return resolved{t: t.UTC()}, nil
	}
	for _, layout := range localForm {
		if zone == "" {
			break
		}
		if zone == "UTC" {
			if t, err := time.Parse(layout, s); err == nil {
				if t.Nanosecond() != 0 {
					return resolved{}, fractionErr(field, s)
				}
				return resolved{t: t}, nil
			}
			continue
		}
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			// A local time is the first occurrence when it is repeated (RFC 5545), and must exist.
			wall, _ := time.Parse(layout, s)
			if wall.Nanosecond() != 0 {
				return resolved{}, fractionErr(field, s)
			}
			inst := localInstants(wall, t, loc)
			if len(inst) == 0 {
				return resolved{}, dataErr("%s %q does not exist in %s (skipped by a clock change)", field, s, zone)
			}
			return resolved{t: inst[0], zoned: true, zone: zone, loc: loc, local: true}, nil
		}
	}
	return resolved{}, dataErr("%s %q is not RFC 3339, and a local date-time (YYYY-MM-DDTHH:MM[:SS]) needs a timezone", field, s)
}

func formatDuration(minutes int) string {
	var b strings.Builder
	b.WriteString("PT")
	if h := minutes / 60; h > 0 {
		fmt.Fprintf(&b, "%dH", h)
	}
	if m := minutes % 60; m > 0 {
		fmt.Fprintf(&b, "%dM", m)
	}
	return b.String()
}

// RenderICS renders data (already decoded JSON) to an iCalendar file.
func RenderICS(data any) ([]byte, error) {
	var in ICSInput
	if err := validateAndDecode(OutputICS, data, &in); err != nil {
		return nil, err
	}
	if len(in.Events) == 0 {
		return nil, dataErr("events must hold at least one event")
	}
	if len(in.Events) > MaxItems {
		return nil, limitErr("%d events, above the limit of %d", len(in.Events), MaxItems)
	}
	switch in.Method {
	case "", "PUBLISH", "REQUEST", "CANCEL":
	default:
		return nil, dataErr("method %q is not PUBLISH, REQUEST or CANCEL", in.Method)
	}
	if err := checkLine("name", in.Name); err != nil {
		return nil, err
	}
	type zoneSpan struct {
		loc      *time.Location
		from, to int
	}
	zones := map[string]*zoneSpan{}
	var d document
	d.line("BEGIN:VCALENDAR")
	d.line("VERSION:2.0")
	d.line("PRODID:-//AgentMaurice//Artifact//EN")
	d.line("CALSCALE:GREGORIAN")
	if in.Method != "" {
		d.line("METHOD:" + in.Method)
	}
	if in.Name != "" {
		d.prop("X-WR-CALNAME", nil, EscapeText(in.Name))
	}
	// Events are rendered into a separate document: VTIMEZONE components precede them.
	var ev document
	for i := range in.Events {
		e := &in.Events[i]
		label := fmt.Sprintf("events[%d]", i)
		if err := renderEvent(&ev, e, label, func(zone string, loc *time.Location, from, to int) {
			z := zones[zone]
			if z == nil {
				z = &zoneSpan{loc: loc, from: from, to: to}
				zones[zone] = z
			}
			if from < z.from {
				z.from = from
			}
			if to > z.to {
				z.to = to
			}
		}); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(zones))
	for n := range zones {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		z := zones[n]
		writeVTimezone(&d, n, z.loc, z.from, z.to)
	}
	d.b.WriteString(ev.b.String())
	d.line("END:VCALENDAR")
	return d.bytes()
}

func renderEvent(d *document, e *ICSEvent, label string, useZone func(string, *time.Location, int, int)) error {
	if e.Summary == "" {
		return dataErr("%s.summary is required", label)
	}
	for _, f := range []struct {
		name, v string
		multi   bool
	}{{"summary", e.Summary, false}, {"description", e.Description, true}, {"location", e.Location, false}} {
		max := MaxLineSource
		if f.multi {
			max = MaxTextBytes
		}
		if err := checkText(label+"."+f.name, f.v, f.multi, max); err != nil {
			return err
		}
	}
	if e.URL != "" {
		if err := checkURI(label+".url", e.URL); err != nil {
			return err
		}
	}
	switch e.Status {
	case "", "TENTATIVE", "CONFIRMED", "CANCELLED":
	default:
		return dataErr("%s.status %q is not TENTATIVE, CONFIRMED or CANCELLED", label, e.Status)
	}
	var loc *time.Location
	if e.Timezone != "" {
		if e.AllDay {
			return dataErr("%s: an all-day event has no timezone", label)
		}
		if e.Timezone == "UTC" {
			loc = time.UTC
		} else {
			var err error
			if loc, err = loadZone(e.Timezone); err != nil {
				return err
			}
		}
	}
	start, err := parseMoment(label+".start", e.Start, e.AllDay, e.Timezone, loc)
	if err != nil {
		return err
	}
	if e.AllDay && e.DurationMinutes != nil {
		return dataErr("%s: an all-day event takes an end date or duration_days, not duration_minutes (RFC 5545: DTSTART of type DATE needs a duration in days)", label)
	}
	if !e.AllDay && e.DurationDays != nil {
		return dataErr("%s: duration_days applies to all-day events only", label)
	}
	if e.End != "" && (e.DurationMinutes != nil || e.DurationDays != nil) {
		return dataErr("%s: end and a duration are exclusive", label)
	}
	if e.DurationMinutes != nil && e.DurationDays != nil {
		return dataErr("%s: duration_minutes and duration_days are exclusive", label)
	}
	if e.DurationDays != nil && (*e.DurationDays < 1 || *e.DurationDays > 366) {
		return dataErr("%s.duration_days must be between 1 and 366", label)
	}
	var end *resolved
	if e.End != "" {
		r, err := parseMoment(label+".end", e.End, e.AllDay, e.Timezone, loc)
		if err != nil {
			return err
		}
		if !r.t.After(start.t) {
			return dataErr("%s.end must be after start", label)
		}
		end = &r
	}
	if e.DurationMinutes != nil && (*e.DurationMinutes < 1 || *e.DurationMinutes > 60*24*366) {
		return dataErr("%s.duration_minutes must be between 1 and %d", label, 60*24*366)
	}
	if e.DTStamp != "" {
		if st, err := time.Parse(time.RFC3339, e.DTStamp); err == nil && st.Nanosecond() != 0 {
			return fractionErr(label+".dtstamp", e.DTStamp)
		}
		if _, err := time.Parse(time.RFC3339, e.DTStamp); err != nil {
			return dataErr("%s.dtstamp %q is not RFC 3339 with a zone", label, e.DTStamp)
		}
	}
	if len(e.Attendees) > MaxAttendees {
		return limitErr("%s has %d attendees, above the limit of %d", label, len(e.Attendees), MaxAttendees)
	}

	uid := e.UID
	if uid == "" {
		c := *e
		c.UID = ""
		canonical, _ := json.Marshal(c)
		uid = derivedID("ics.uid", canonical)[:32] + "@artifact.agentmaurice"
	} else if err := checkLine(label+".uid", uid); err != nil {
		return err
	}
	stamp := start.t.UTC()
	if e.DTStamp != "" {
		t, _ := time.Parse(time.RFC3339, e.DTStamp)
		stamp = t.UTC()
	}

	d.line("BEGIN:VEVENT")
	d.prop("UID", nil, EscapeText(uid))
	d.prop("DTSTAMP", nil, stamp.Format(icsUTC))
	p, v := start.render()
	d.prop("DTSTART", p, v)
	if end != nil {
		p, v := end.render()
		d.prop("DTEND", p, v)
	}
	if e.DurationMinutes != nil {
		d.prop("DURATION", nil, formatDuration(*e.DurationMinutes))
	}
	if e.DurationDays != nil {
		d.prop("DURATION", nil, fmt.Sprintf("P%dD", *e.DurationDays))
	}
	d.prop("SUMMARY", nil, EscapeText(e.Summary))
	if e.Description != "" {
		d.prop("DESCRIPTION", nil, EscapeText(e.Description))
	}
	if e.Location != "" {
		d.prop("LOCATION", nil, EscapeText(e.Location))
	}
	if e.URL != "" {
		d.prop("URL", nil, e.URL)
	}
	if e.Status != "" {
		d.prop("STATUS", nil, e.Status)
	}
	if e.Organizer != nil {
		params, value, err := mailtoProp(label+".organizer", e.Organizer.Name, e.Organizer.Email, nil)
		if err != nil {
			return err
		}
		d.prop("ORGANIZER", params, value)
	}
	for i, a := range e.Attendees {
		params := []string{}
		switch a.Role {
		case "":
			params = append(params, "ROLE=REQ-PARTICIPANT")
		case "REQ-PARTICIPANT", "OPT-PARTICIPANT", "CHAIR":
			params = append(params, "ROLE="+a.Role)
		default:
			return dataErr("%s.attendees[%d].role %q is not REQ-PARTICIPANT, OPT-PARTICIPANT or CHAIR", label, i, a.Role)
		}
		params = append(params, "PARTSTAT=NEEDS-ACTION")
		if a.RSVP {
			params = append(params, "RSVP=TRUE")
		}
		ps, value, err := mailtoProp(fmt.Sprintf("%s.attendees[%d]", label, i), a.Name, a.Email, params)
		if err != nil {
			return err
		}
		d.prop("ATTENDEE", ps, value)
	}
	// The VTIMEZONE covers the effective end of every occurrence: start plus the
	// end, duration_minutes or duration_days of the event, up to the last recurrence.
	var span time.Duration
	days := 0
	switch {
	case end != nil:
		span = end.t.Sub(start.t)
	case e.DurationMinutes != nil:
		span = time.Duration(*e.DurationMinutes) * time.Minute
	case e.DurationDays != nil:
		days = *e.DurationDays
	}
	yearOf := func(from time.Time) int {
		t := from.Add(span).AddDate(0, 0, days)
		if start.zoned {
			t = t.In(start.loc)
		}
		return t.Year()
	}
	lastYear := yearOf(start.t)
	if e.RRule != nil && start.zoned && start.ambiguous() {
		return dataErr("%s: the start falls in the repeated hour of a clock change and is written in UTC, which a recurrence in %s cannot follow; use another start", label, start.zone)
	}
	if e.RRule != nil {
		line, until, err := renderRRule(e.RRule, start, label+".rrule")
		if err != nil {
			return err
		}
		d.prop("RRULE", nil, line)
		switch {
		case until > 0:
			lastYear = yearOf(time.Date(until, 12, 31, 23, 59, 59, 0, time.UTC))
		case e.RRule.Count > 0:
			wall, zone := start.t, (*time.Location)(nil)
			if start.zoned {
				wall, zone = start.t.In(start.loc), start.loc
			}
			last, err := lastOccurrence(e.RRule, wall, zone, label+".rrule")
			if err != nil {
				return err
			}
			h, mi, sec := wall.Clock()
			lastStart := time.Date(last.Year(), last.Month(), last.Day(), h, mi, sec, 0, time.UTC)
			if zone != nil {
				lastStart = time.Date(last.Year(), last.Month(), last.Day(), h, mi, sec, 0, zone)
			}
			lastYear = yearOf(lastStart)
		case start.zoned:
			return dataErr("%s.rrule: a recurrence in the timezone %s must be bounded (count or until); an unbounded rule is allowed in UTC only", label, start.zone)
		}
		if start.zoned && lastYear-start.t.Year() > MaxRecurrenceYears {
			return limitErr("%s.rrule: the occurrences reach year %d, more than %d years after the start", label, lastYear, MaxRecurrenceYears)
		}
	}
	d.line("END:VEVENT")
	if start.zoned {
		useZone(start.zone, start.loc, start.t.In(start.loc).Year(), lastYear)
	}
	return nil
}

func mailtoProp(label, name, email string, params []string) ([]string, string, error) {
	if err := checkEmail(label+".email", email); err != nil {
		return nil, "", err
	}
	if name != "" {
		if err := checkParam(label+".name", name); err != nil {
			return nil, "", err
		}
		params = append([]string{"CN=" + paramValue(name)}, params...)
	}
	return params, "mailto:" + email, nil
}

// renderRRule returns the RRULE value and the UNTIL year (0 when the rule has none).
func renderRRule(r *RRule, start resolved, label string) (string, int, error) {
	switch r.Freq {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	default:
		return "", 0, dataErr("%s.freq %q is not DAILY, WEEKLY, MONTHLY or YEARLY", label, r.Freq)
	}
	if r.Interval < 0 || r.Interval > 999 {
		return "", 0, dataErr("%s.interval must be between 1 and 999", label)
	}
	if r.Count != 0 && r.Until != "" {
		return "", 0, dataErr("%s: count and until are exclusive", label)
	}
	if r.Count < 0 || r.Count > 10000 {
		return "", 0, dataErr("%s.count must be between 1 and 10000", label)
	}
	parts := []string{"FREQ=" + r.Freq}
	if r.Interval > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", r.Interval))
	}
	if r.Count > 0 {
		parts = append(parts, fmt.Sprintf("COUNT=%d", r.Count))
	}
	untilYear := 0
	if r.Until != "" {
		var u time.Time
		var err error
		if start.date {
			if u, err = time.Parse("2006-01-02", r.Until); err != nil {
				return "", 0, dataErr("%s.until %q is not a date (YYYY-MM-DD) for an all-day event", label, r.Until)
			}
			parts = append(parts, "UNTIL="+u.Format(icsDate))
		} else {
			if u, err = time.Parse(time.RFC3339, r.Until); err == nil && u.Nanosecond() != 0 {
				return "", 0, fractionErr(label+".until", r.Until)
			}
			if err != nil {
				return "", 0, dataErr("%s.until %q is not RFC 3339 with a zone", label, r.Until)
			}
			u = u.UTC()
			parts = append(parts, "UNTIL="+u.Format(icsUTC))
		}
		if u.Before(start.t.UTC()) && !start.date {
			return "", 0, dataErr("%s.until is before start", label)
		}
		untilYear = u.Year()
	}
	if len(r.ByDay) > 0 {
		if len(r.ByDay) > 7 {
			return "", 0, dataErr("%s.by_day holds more than 7 days", label)
		}
		seen := map[string]bool{}
		for _, day := range r.ByDay {
			if !byDayRe.MatchString(day) || seen[day] {
				return "", 0, dataErr("%s.by_day %q is not a unique two-letter day (MO..SU)", label, day)
			}
			seen[day] = true
		}
		parts = append(parts, "BYDAY="+strings.Join(r.ByDay, ","))
	}
	return strings.Join(parts, ";"), untilYear, nil
}

func offsetString(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	if sec%60 != 0 {
		return fmt.Sprintf("%s%02d%02d%02d", sign, sec/3600, sec%3600/60, sec%60)
	}
	return fmt.Sprintf("%s%02d%02d", sign, sec/3600, sec%3600/60)
}

// writeVTimezone emits a VTIMEZONE covering the years from..to with one
// observance per offset change (plus the offset in force at the start).
func writeVTimezone(d *document, name string, loc *time.Location, from, to int) {
	d.line("BEGIN:VTIMEZONE")
	d.prop("TZID", nil, paramValue(name))
	cur := time.Date(from, 1, 1, 0, 0, 0, 0, loc)
	_, off := cur.Zone()
	observance := func(t time.Time, fromOff int) {
		kind := "STANDARD"
		if t.IsDST() {
			kind = "DAYLIGHT"
		}
		abbr, toOff := t.Zone()
		d.line("BEGIN:" + kind)
		d.prop("DTSTART", nil, t.In(time.FixedZone("", fromOff)).Format(icsLocal))
		d.prop("TZOFFSETFROM", nil, offsetString(fromOff))
		d.prop("TZOFFSETTO", nil, offsetString(toOff))
		d.prop("TZNAME", nil, EscapeText(abbr))
		d.line("END:" + kind)
	}
	observance(cur, off)
	// Offset changes are found by scanning day by day, then bisecting to the
	// second: time.ZoneBounds is not reliable past the explicit transitions of
	// the zone data (before 2038 in most zones).
	limit := time.Date(to+1, 1, 1, 0, 0, 0, 0, loc)
	for day := cur; day.Before(limit); {
		next := day.AddDate(0, 0, 1)
		if _, o := next.Zone(); o != off {
			lo, hi := day, next // offset(lo) == off, offset(hi) != off
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				if _, mo := mid.Zone(); mo == off {
					lo = mid
				} else {
					hi = mid
				}
			}
			_, newOff := hi.Zone()
			observance(hi, off)
			off = newOff
		}
		day = next
	}
	d.line("END:VTIMEZONE")
}

// MaxRecurrenceYears is the safety bound on the span of a bounded recurrence in
// a timezone: the VTIMEZONE covers the whole span, so the span is limited, never truncated.
const MaxRecurrenceYears = 100

var weekdayCodes = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

// lastOccurrenceYear iterates the occurrences of a counted rule as RFC 5545
// defines them (DTSTART is the first one; a date that does not exist, such as
// February 30, is skipped and does not consume COUNT; weeks start on Monday) and
// returns the year of the last one. With a zone (loc not nil), an occurrence whose
// local time does not exist (skipped by a clock change) is not an occurrence and
// does not consume COUNT. It stops with ErrLimits when the occurrences
// go beyond MaxRecurrenceYears after the start.
func lastOccurrenceYear(r *RRule, startWall time.Time, loc *time.Location, label string) (int, error) {
	d, err := lastOccurrence(r, startWall, loc, label)
	return d.Year(), err
}

// lastOccurrence returns the date of the last occurrence (see lastOccurrenceYear).
func lastOccurrence(r *RRule, startWall time.Time, loc *time.Location, label string) (time.Time, error) {
	startDay := time.Date(startWall.Year(), startWall.Month(), startWall.Day(), 0, 0, 0, 0, time.UTC)
	interval := r.Interval
	if interval < 1 {
		interval = 1
	}
	byDay := map[time.Weekday]bool{}
	for _, d := range r.ByDay {
		byDay[weekdayCodes[d]] = true
	}
	daysIn := func(from, to time.Time) []time.Time { // [from, to)
		var out []time.Time
		for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
			if byDay[d.Weekday()] {
				out = append(out, d)
			}
		}
		return out
	}
	period := func(p int) []time.Time {
		switch r.Freq {
		case "DAILY":
			d := startDay.AddDate(0, 0, p*interval)
			if len(byDay) > 0 && !byDay[d.Weekday()] {
				return nil
			}
			return []time.Time{d}
		case "WEEKLY":
			monday := startDay.AddDate(0, 0, -((int(startDay.Weekday())+6)%7)+7*interval*p)
			if len(byDay) == 0 {
				return []time.Time{monday.AddDate(0, 0, (int(startDay.Weekday())+6)%7)}
			}
			return daysIn(monday, monday.AddDate(0, 0, 7))
		case "MONTHLY":
			first := time.Date(startDay.Year(), startDay.Month()+time.Month(p*interval), 1, 0, 0, 0, 0, time.UTC)
			next := first.AddDate(0, 1, 0)
			if len(byDay) > 0 {
				return daysIn(first, next)
			}
			if startDay.Day() > int(next.Sub(first).Hours()/24) {
				return nil // the month has no such day: skipped, COUNT is not consumed
			}
			return []time.Time{first.AddDate(0, 0, startDay.Day()-1)}
		default: // YEARLY
			y := startDay.Year() + p*interval
			if len(byDay) > 0 {
				return daysIn(time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC))
			}
			d := time.Date(y, startDay.Month(), startDay.Day(), 0, 0, 0, 0, time.UTC)
			if d.Month() != startDay.Month() {
				return nil // February 29 of a common year
			}
			return []time.Time{d}
		}
	}
	count, last := 1, startDay // DTSTART is the first occurrence
	if r.Count == 1 {
		return last, nil
	}
	for p := 0; ; p++ {
		if p > 366*(MaxRecurrenceYears+1) {
			return time.Time{}, limitErr("%s: the occurrences go beyond %d years after the start", label, MaxRecurrenceYears)
		}
		for _, d := range period(p) {
			if !d.After(startDay) {
				continue
			}
			if loc != nil {
				h, mi, sec := startWall.Clock()
				wall := time.Date(d.Year(), d.Month(), d.Day(), h, mi, sec, 0, time.UTC)
				if len(localInstants(wall, d.Add(12*time.Hour), loc)) == 0 {
					continue
				}
			}
			if d.Year()-startDay.Year() > MaxRecurrenceYears {
				return time.Time{}, limitErr("%s: the occurrences reach year %d, more than %d years after the start", label, d.Year(), MaxRecurrenceYears)
			}
			count, last = count+1, d
			if count == r.Count {
				return last, nil
			}
		}
	}
}
