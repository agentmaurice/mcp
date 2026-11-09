package interchange

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the golden file (run go test -update):\n%s", name, got)
	}
}

// line is a parsed content line of the independent test parser.
type line struct {
	name   string
	params string
	value  string // still escaped
}

// parseLines is a minimal independent parser: it checks CRLF endings and the
// 75-octet limit, unfolds, and splits NAME;PARAMS:VALUE.
func parseLines(t *testing.T, out []byte) []line {
	t.Helper()
	if !utf8.Valid(out) {
		t.Fatal("output is not valid UTF-8")
	}
	if !bytes.HasSuffix(out, []byte("\r\n")) || bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("lines must all end with CRLF")
	}
	var logical []string
	for _, p := range strings.Split(strings.TrimSuffix(string(out), "\r\n"), "\r\n") {
		if len(p) > 75 {
			t.Fatalf("physical line of %d octets: %q", len(p), p)
		}
		if !utf8.ValidString(p) {
			t.Fatalf("a UTF-8 sequence is split: %q", p)
		}
		if strings.HasPrefix(p, " ") && len(logical) > 0 {
			logical[len(logical)-1] += p[1:]
			continue
		}
		logical = append(logical, p)
	}
	var lines []line
	for _, l := range logical {
		colon := -1
		quoted := false
		for i, c := range l {
			if c == '"' {
				quoted = !quoted
			}
			if c == ':' && !quoted {
				colon = i
				break
			}
		}
		if colon < 0 {
			t.Fatalf("no colon in %q", l)
		}
		head, value := l[:colon], l[colon+1:]
		name, params, _ := strings.Cut(head, ";")
		lines = append(lines, line{name, params, value})
	}
	return lines
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == 'n' || s[i] == 'N' {
				b.WriteByte('\n')
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func find(lines []line, name string) []line {
	var out []line
	for _, l := range lines {
		if l.name == name {
			out = append(out, l)
		}
	}
	return out
}

func TestFoldBoundsAndMultibyte(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("a", 200),
		"X:" + strings.Repeat("é", 100),
		"X:" + strings.Repeat("😀", 40),
		"X:" + strings.Repeat("a", 72) + "é", // the 2-byte rune straddles octet 75
		"short",
		"",
	} {
		folded := Fold(in)
		if !strings.HasSuffix(folded, "\r\n") {
			t.Fatalf("no CRLF: %q", folded)
		}
		var rebuilt strings.Builder
		for i, p := range strings.Split(strings.TrimSuffix(folded, "\r\n"), "\r\n") {
			if len(p) > 75 || !utf8.ValidString(p) {
				t.Fatalf("bad physical line %q (%d octets)", p, len(p))
			}
			if i > 0 {
				if p[0] != ' ' {
					t.Fatalf("continuation without space: %q", p)
				}
				p = p[1:]
			}
			rebuilt.WriteString(p)
		}
		if rebuilt.String() != in {
			t.Fatalf("unfold(fold(x)) != x for %q", in)
		}
	}
	// exactly 75 octets: not folded; 76: folded
	if got := Fold(strings.Repeat("a", 75)); strings.Count(got, "\r\n") != 1 {
		t.Fatal("a 75 octet line must not be folded")
	}
	if got := Fold(strings.Repeat("a", 76)); strings.Count(got, "\r\n") != 2 {
		t.Fatal("a 76 octet line must be folded")
	}
}

func TestEscapeText(t *testing.T) {
	cases := map[string]string{
		`a,b;c\d`:    `a\,b\;c\\d`,
		"l1\nl2":     `l1\nl2`,
		"l1\r\nl2":   `l1\nl2`,
		"l1\rl2":     `l1\nl2`,
		"é,😀":        "é\\,😀",
		"":           "",
		`\n literal`: `\\n literal`,
	}
	for in, want := range cases {
		if got := EscapeText(in); got != want {
			t.Errorf("EscapeText(%q) = %q, want %q", in, got, want)
		}
		if got := unescape(EscapeText(in)); got != strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(in) {
			t.Errorf("round trip of %q gave %q", in, got)
		}
	}
}

const icsSample = `{"name":"Equipe, projet","method":"REQUEST","events":[
{"summary":"Revue; trimestrielle, avec \\ et accents éàü 😀","description":"Ligne 1\nLigne 2, avec virgule; et point-virgule. ` + `Un texte assez long pour obliger le pliage à soixante-quinze octets, avec des caractères multioctets éééé.","location":"Paris, salle \"A\"",
 "start":"2026-11-10T09:30:00","duration_minutes":90,"timezone":"Europe/Paris",
 "organizer":{"name":"Ada, Lovelace","email":"ada@example.com"},
 "attendees":[{"name":"Bob","email":"bob@example.com","rsvp":true},{"email":"carol@example.com","role":"OPT-PARTICIPANT"}],
 "rrule":{"freq":"WEEKLY","interval":2,"count":6,"by_day":["TU","TH"]}},
{"summary":"Journée entière","start":"2026-12-24","end":"2026-12-26","all_day":true},
{"summary":"Appel UTC","start":"2026-11-12T14:00:00Z","end":"2026-11-12T14:45:00Z","dtstamp":"2026-11-01T08:00:00Z","status":"CONFIRMED","url":"https://example.com/a?b=1,2"}
]}`

func TestICSGoldenAndParse(t *testing.T) {
	out, err := RenderICS(decode(t, icsSample))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "events.ics", out)
	lines := parseLines(t, out)
	if lines[0].name != "BEGIN" || lines[0].value != "VCALENDAR" || lines[len(lines)-1].value != "VCALENDAR" {
		t.Fatalf("not wrapped in VCALENDAR: %v", lines)
	}
	if n := len(find(lines, "VEVENT")); n != 0 {
		t.Fatalf("VEVENT is a BEGIN value, got %d lines named VEVENT", n)
	}
	sums := find(lines, "SUMMARY")
	if len(sums) != 3 || unescape(sums[0].value) != "Revue; trimestrielle, avec \\ et accents éàü 😀" {
		t.Fatalf("summary does not round trip: %v", sums)
	}
	if d := find(lines, "DESCRIPTION"); !strings.HasPrefix(unescape(d[0].value), "Ligne 1\nLigne 2, avec virgule; et point-virgule.") {
		t.Fatalf("description does not round trip: %q", d[0].value)
	}
	s := find(lines, "DTSTART")
	s = s[len(s)-3:] // the last three are the events (the others belong to the VTIMEZONE)
	if s[0].params != "TZID=Europe/Paris" || s[0].value != "20261110T093000" ||
		s[1].params != "VALUE=DATE" || s[1].value != "20261224" || s[2].params != "" || s[2].value != "20261112T140000Z" {
		t.Fatalf("DTSTART lines: %v", s)
	}
	if r := find(lines, "RRULE"); r[0].value != "FREQ=WEEKLY;INTERVAL=2;COUNT=6;BYDAY=TU,TH" {
		t.Fatalf("RRULE: %v", r)
	}
	if d := find(lines, "DURATION"); d[0].value != "PT1H30M" {
		t.Fatalf("DURATION: %v", d)
	}
	if o := find(lines, "ORGANIZER"); o[0].params != `CN="Ada, Lovelace"` || o[0].value != "mailto:ada@example.com" {
		t.Fatalf("ORGANIZER: %v", o)
	}
	if tz := find(lines, "TZID"); len(tz) != 1 || tz[0].value != "Europe/Paris" {
		t.Fatalf("VTIMEZONE: %v", tz)
	}
	// the Paris offsets at the event: CET (+0100)
	if off := find(lines, "TZOFFSETTO"); len(off) < 2 {
		t.Fatalf("VTIMEZONE observances: %v", off)
	}
	for _, d := range find(lines, "DTSTAMP") {
		if !strings.HasSuffix(d.value, "Z") {
			t.Fatalf("DTSTAMP must be UTC: %v", d)
		}
	}
}

func TestICSDeterministicAndDerivedUID(t *testing.T) {
	var first [32]byte
	for i := 0; i < 5; i++ {
		out, err := RenderICS(decode(t, icsSample))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(out)
		if i == 0 {
			first = sum
		} else if sum != first {
			t.Fatalf("render %d differs", i)
		}
	}
	a, _ := RenderICS(decode(t, `{"events":[{"summary":"x","start":"2026-01-01","all_day":true}]}`))
	b, _ := RenderICS(decode(t, `{"events":[{"summary":"y","start":"2026-01-01","all_day":true}]}`))
	ua, ub := find(parseLines(t, a), "UID")[0].value, find(parseLines(t, b), "UID")[0].value
	if ua == ub || !strings.HasSuffix(ua, "@artifact.agentmaurice") {
		t.Fatalf("derived UIDs: %s %s", ua, ub)
	}
	c, _ := RenderICS(decode(t, `{"events":[{"uid":"fixed-1","summary":"x","start":"2026-01-01","all_day":true}]}`))
	if find(parseLines(t, c), "UID")[0].value != "fixed-1" {
		t.Fatal("an explicit uid must be kept")
	}
}

func TestICSVTimezoneTracksDST(t *testing.T) {
	out, err := RenderICS(decode(t, `{"events":[{"summary":"x","start":"2026-07-01T10:00","duration_minutes":30,"timezone":"Europe/Paris"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"BEGIN:DAYLIGHT", "BEGIN:STANDARD", "TZOFFSETTO:+0200", "TZOFFSETTO:+0100", "DTSTART;TZID=Europe/Paris:20260701T100000"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
}

func TestICSErrors(t *testing.T) {
	cases := map[string]struct {
		json string
		kind error
	}{
		"unknown field":      {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"bogus":1}]}`, ErrData},
		"no events":          {`{"events":[]}`, ErrData},
		"local no zone":      {`{"events":[{"summary":"x","start":"2026-01-01T10:00:00"}]}`, ErrData},
		"unknown zone":       {`{"events":[{"summary":"x","start":"2026-01-01T10:00:00","timezone":"Mars/Base"}]}`, ErrData},
		"end before start":   {`{"events":[{"summary":"x","start":"2026-01-02T10:00:00Z","end":"2026-01-01T10:00:00Z"}]}`, ErrData},
		"end and duration":   {`{"events":[{"summary":"x","start":"2026-01-01T10:00:00Z","end":"2026-01-02T10:00:00Z","duration_minutes":5}]}`, ErrData},
		"bad email":          {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"organizer":{"email":"not an email"}}]}`, ErrData},
		"control char":       {`{"events":[{"summary":"x\u0000y","start":"2026-01-01","all_day":true}]}`, ErrData},
		"bad rrule freq":     {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"rrule":{"freq":"HOURLY"}}]}`, ErrData},
		"count and until":    {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"rrule":{"freq":"DAILY","count":2,"until":"2026-02-01"}}]}`, ErrData},
		"all-day with zone":  {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"timezone":"UTC"}]}`, ErrData},
		"all-day minutes":    {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"duration_minutes":60}]}`, ErrData},
		"days on timed":      {`{"events":[{"summary":"x","start":"2026-01-01T10:00:00Z","duration_days":2}]}`, ErrData},
		"days and end":       {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"end":"2026-01-03","duration_days":2}]}`, ErrData},
		"unbounded in zone":  {`{"events":[{"summary":"x","start":"2026-07-01T10:00","timezone":"Europe/Paris","rrule":{"freq":"YEARLY"}}]}`, ErrData},
		"too long in zone":   {`{"events":[{"summary":"x","start":"2026-07-01T10:00","timezone":"Europe/Paris","rrule":{"freq":"YEARLY","count":150}}]}`, ErrLimits},
		"quote in CN":        {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"organizer":{"name":"Ada \"A\" Lovelace","email":"a@b.co"}}]}`, ErrData},
		"quote in attendee":  {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"attendees":[{"name":"Bob \"B\"","email":"b@b.co"}]}]}`, ErrData},
		"missing summary":    {`{"events":[{"start":"2026-01-01","all_day":true}]}`, ErrData},
		"injection in zone":  {`{"events":[{"summary":"x","start":"2026-01-01T10:00:00","timezone":"UTC\r\nX-EVIL:1"}]}`, ErrData},
		"injection in email": {`{"events":[{"summary":"x","start":"2026-01-01","all_day":true,"organizer":{"email":"a@b.c\r\nX-EVIL:1"}}]}`, ErrData},
	}
	for name, c := range cases {
		_, err := RenderICS(decode(t, c.json))
		if !errors.Is(err, c.kind) {
			t.Errorf("%s: err = %v, want %v", name, err, c.kind)
		}
	}
	// bounds
	events := make([]string, MaxItems+1)
	for i := range events {
		events[i] = `{"summary":"x","start":"2026-01-01","all_day":true}`
	}
	_, err := RenderICS(decode(t, `{"events":[`+strings.Join(events, ",")+`]}`))
	if err == nil {
		t.Error("more than MaxItems events must be refused")
	}
	_, err = RenderICS(decode(t, `{"events":[{"summary":"x","description":"`+strings.Repeat("a", MaxTextBytes+1)+`","start":"2026-01-01","all_day":true}]}`))
	if !errors.Is(err, ErrLimits) {
		t.Errorf("a long description: %v", err)
	}
}

const vcfSample = `{"cards":[
{"fn":"Ada Lovelace, Comtesse","n":{"family":"Lovelace","given":"Ada","prefix":"Dr."},"org":["Analytical Engines; Ltd","R&D"],"title":"Mathématicienne","email":[{"value":"ada@example.com","type":"work"},{"value":"ada@home.example"}],
 "tel":[{"value":"+33 1 23 45 67 89","type":"work"},{"value":"06.12.34.56.78","type":"cell"}],
 "adr":[{"type":"work","street":"10 rue de la Paix","locality":"Paris","postal_code":"75002","country":"France"}],
 "url":["https://example.com/ada"],"note":"Ligne 1\nLigne 2, 😀; fin. Un texte assez long pour obliger le pliage à soixante-quinze octets éééé."},
{"fn":"Bob"}]}`

func TestVCFGoldenAndParse(t *testing.T) {
	out, err := RenderVCF(decode(t, vcfSample))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "cards.vcf", out)
	lines := parseLines(t, out)
	if v := find(lines, "VERSION"); len(v) != 2 || v[0].value != "4.0" {
		t.Fatalf("VERSION: %v", v)
	}
	if f := find(lines, "FN"); unescape(f[0].value) != "Ada Lovelace, Comtesse" {
		t.Fatalf("FN: %v", f)
	}
	if n := find(lines, "N"); n[0].value != "Lovelace;Ada;;Dr.;" {
		t.Fatalf("N: %v", n)
	}
	if o := find(lines, "ORG"); o[0].value != `Analytical Engines\; Ltd;R&D` {
		t.Fatalf("ORG: %v", o)
	}
	if tl := find(lines, "TEL"); tl[0].value != "tel:+33123456789" || tl[1].value != "06.12.34.56.78" || tl[1].params != "TYPE=cell;VALUE=text" || tl[0].params != "TYPE=work;VALUE=uri" {
		t.Fatalf("TEL: %v", tl)
	}
	if a := find(lines, "ADR"); a[0].value != ";;10 rue de la Paix;Paris;;75002;France" {
		t.Fatalf("ADR: %v", a)
	}
	if n := find(lines, "NOTE"); unescape(n[0].value) != "Ligne 1\nLigne 2, 😀; fin. Un texte assez long pour obliger le pliage à soixante-quinze octets éééé." {
		t.Fatalf("NOTE: %v", n)
	}
	if u := find(lines, "UID"); !strings.HasPrefix(u[0].value, "urn:uuid:") || u[0].value == u[1].value {
		t.Fatalf("UID: %v", u)
	}
}

func TestVCFDeterministic(t *testing.T) {
	var first [32]byte
	for i := 0; i < 5; i++ {
		out, err := RenderVCF(decode(t, vcfSample))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(out)
		if i == 0 {
			first = sum
		} else if sum != first {
			t.Fatalf("render %d differs", i)
		}
	}
}

func TestVCFErrors(t *testing.T) {
	for name, j := range map[string]string{
		"no fn":       `{"cards":[{"n":{"family":"x"}}]}`,
		"no cards":    `{"cards":[]}`,
		"unknown key": `{"cards":[{"fn":"x","nickname":"y"}]}`,
		"bad email":   `{"cards":[{"fn":"x","email":[{"value":"nope"}]}]}`,
		"bad tel":     `{"cards":[{"fn":"x","tel":[{"value":"call me"}]}]}`,
		"bad type":    `{"cards":[{"fn":"x","tel":[{"value":"+3312345678","type":"pager"}]}]}`,
		"bad url":     `{"cards":[{"fn":"x","url":["javascript:alert(1)"]}]}`,
		"newline fn":  `{"cards":[{"fn":"x\r\nEND:VCARD"}]}`,
	} {
		if _, err := RenderVCF(decode(t, j)); !errors.Is(err, ErrData) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCSVOptions(t *testing.T) {
	data := `{"headers":["nom","note"],"rows":[["Ada","a,b"],["Bob","x\"y"],["Cé",null]]}`
	cases := []struct {
		opts, want string
	}{
		{``, "nom,note\r\nAda,\"a,b\"\r\nBob,\"x\"\"y\"\r\nCé,\r\n"},
		{`{"separator":";","line_ending":"lf"}`, "nom;note\nAda;a,b\nBob;\"x\"\"y\"\nCé;\n"},
		{`{"separator":"\t"}`, "nom\tnote\r\nAda\ta,b\r\nBob\t\"x\"\"y\"\r\nCé\t\r\n"},
		{`{"bom":true,"quote":"all"}`, "\xEF\xBB\xBF\"nom\",\"note\"\r\n\"Ada\",\"a,b\"\r\n\"Bob\",\"x\"\"y\"\r\n\"Cé\",\"\"\r\n"},
	}
	for _, c := range cases {
		var o any
		if c.opts != "" {
			o = decode(t, c.opts)
		}
		got, err := RenderCSV(decode(t, data), o)
		if err != nil || string(got) != c.want {
			t.Errorf("opts %s: %q, %v; want %q", c.opts, got, err, c.want)
		}
	}
}

func TestCSVChecks(t *testing.T) {
	bad := map[string][2]string{
		"ragged row":        {`{"headers":["a","b"],"rows":[["1"]]}`, ``},
		"ragged no headers": {`{"rows":[["1","2"],["3"]]}`, ``},
		"expected columns":  {`{"rows":[["1","2"]]}`, `{"expected_columns":3}`},
		"expected headers":  {`{"headers":["a","b"],"rows":[]}`, `{"expected_headers":["a","c"]}`},
		"bad separator":     {`{"rows":[]}`, `{"separator":"x"}`},
		"unknown option":    {`{"rows":[]}`, `{"bogus":true}`},
		"nested cell":       {`{"rows":[[["x"]]]}`, ``},
		"unknown key":       {`{"headers":["a"],"rows":[{"b":1}]}`, ``},
		"unknown field":     {`{"rows":[],"extra":1}`, ``},
	}
	for name, c := range bad {
		var o any
		if c[1] != "" {
			o = decode(t, c[1])
		}
		if _, err := RenderCSV(decode(t, c[0]), o); !errors.Is(err, ErrData) {
			t.Errorf("%s: %v", name, err)
		}
	}
	good, err := RenderCSV(decode(t, `{"headers":["a","b"],"rows":[["1","2"]]}`), decode(t, `{"expected_columns":2,"expected_headers":["a","b"]}`))
	if err != nil || string(good) != "a,b\r\n1,2\r\n" {
		t.Errorf("good: %q %v", good, err)
	}
	if _, err := WriteCSV(Table{Rows: make([][]string, MaxCSVRows+1)}, CSVOptions{}); !errors.Is(err, ErrLimits) {
		t.Errorf("rows bound: %v", err)
	}
	if _, err := WriteCSV(Table{Rows: [][]string{{strings.Repeat("a", MaxCSVCellBytes+1)}}}, CSVOptions{}); !errors.Is(err, ErrLimits) {
		t.Errorf("cell bound: %v", err)
	}
}

func TestCSVObjectRowsAndFormulas(t *testing.T) {
	got, err := RenderCSV(decode(t, `{"rows":[{"b":2,"a":"=SUM(A1)"},{"a":true,"b":1.5}]}`), decode(t, `{"line_ending":"lf","neutralize_formulas":true}`))
	if err != nil || string(got) != "a,b\n'=SUM(A1),2\ntrue,1.5\n" {
		t.Errorf("got %q, %v", got, err)
	}
	got, err = RenderCSV(decode(t, `{"headers":["b","a"],"rows":[{"a":1},{"b":2,"a":3}]}`), nil)
	if err != nil || string(got) != "b,a\r\n,1\r\n2,3\r\n" {
		t.Errorf("headers order: %q, %v", got, err)
	}
	// reparsed by encoding/csv, quoting round trips
	out, _ := RenderCSV(decode(t, `{"headers":["x"],"rows":[["a\nb, \"c\""]]}`), nil)
	if string(out) != "x\r\n\"a\nb, \"\"c\"\"\"\r\n" {
		t.Errorf("multiline cell: %q", out)
	}
}

func TestSchemasAreClosed(t *testing.T) {
	for _, name := range []string{"ics", "vcf", "csv", "csv.options"} {
		if Schema(name) == nil {
			t.Errorf("no schema for %s", name)
		}
	}
	if _, err := compiledSchemas(); err != nil {
		t.Fatal(err)
	}
	for name, src := range schemaSources {
		var v map[string]any
		if err := json.Unmarshal([]byte(src), &v); err != nil || v["additionalProperties"] != false {
			t.Errorf("%s: root schema must be closed (%v)", name, err)
		}
	}
}

func TestICSAllDayDurationInDays(t *testing.T) {
	out, err := RenderICS(decode(t, `{"events":[{"summary":"Congé","start":"2026-11-10","all_day":true,"duration_days":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	l := parseLines(t, out)
	if d := find(l, "DURATION"); len(d) != 1 || d[0].value != "P3D" || find(l, "DTSTART")[0].params != "VALUE=DATE" {
		t.Fatalf("%s", out)
	}
}

func TestICSRecurrenceTimezoneCoverage(t *testing.T) {
	// Unbounded stays allowed in UTC.
	if _, err := RenderICS(decode(t, `{"events":[{"summary":"x","start":"2026-07-01T10:00:00Z","rrule":{"freq":"YEARLY"}}]}`)); err != nil {
		t.Fatal(err)
	}
	// A 60 year yearly rule: the VTIMEZONE covers the last occurrence (2085), not a truncated range.
	out, err := RenderICS(decode(t, `{"events":[{"summary":"x","start":"2026-07-01T10:00","timezone":"Europe/Paris","rrule":{"freq":"YEARLY","count":60}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "DTSTART:20851028T030000") && !strings.Contains(string(out), "DTSTART:20851") {
		t.Fatalf("the VTIMEZONE stops before the last occurrence:\n%s", out)
	}
	// until beyond ten years is covered too.
	out, err = RenderICS(decode(t, `{"events":[{"summary":"x","start":"2026-07-01T10:00","timezone":"Europe/Paris","rrule":{"freq":"YEARLY","until":"2050-07-01T00:00:00Z"}}]}`))
	if err != nil || !strings.Contains(string(out), "DTSTART:2050") {
		t.Fatalf("until coverage: %v", err)
	}
}

func TestCSVUnboundedKeepsV01(t *testing.T) {
	big := strings.Repeat("a", MaxCSVCellBytes+1) // 32 769 characters
	table := Table{Headers: []string{"k"}, Rows: [][]string{{big}}}
	out, err := WriteCSVUnbounded(table, CSVOptions{})
	if err != nil || string(out) != "k\n"+big+"\n" {
		t.Fatalf("unbounded write: %v", err)
	}
	if _, err := WriteCSV(table, CSVOptions{}); !errors.Is(err, ErrLimits) {
		t.Fatalf("the render writer must refuse the cell: %v", err)
	}
}

func TestICSRepeatedHourKeepsTheInstant(t *testing.T) {
	start := func(js string) (string, string, error) {
		out, err := RenderICS(decode(t, `{"events":[{"summary":"x","timezone":"Europe/Paris","duration_minutes":30,`+js+`}]}`))
		if err != nil {
			return "", "", err
		}
		s := find(parseLines(t, out), "DTSTART")
		last := s[len(s)-1]
		return last.params, last.value, nil
	}
	// 01:30Z on 2026-10-25 is the second 02:30 of Paris: written in UTC, not as an ambiguous local time.
	if p, v, err := start(`"start":"2026-10-25T01:30:00Z"`); err != nil || p != "" || v != "20261025T013000Z" {
		t.Fatalf("second occurrence: %q %q %v", p, v, err)
	}
	// 00:30Z is the first 02:30: the local form is exact.
	if p, v, err := start(`"start":"2026-10-25T00:30:00Z"`); err != nil || p != "TZID=Europe/Paris" || v != "20261025T023000" {
		t.Fatalf("first occurrence: %q %q %v", p, v, err)
	}
	// a local time of the repeated hour means the first occurrence
	if p, v, err := start(`"start":"2026-10-25T02:30"`); err != nil || p != "TZID=Europe/Paris" || v != "20261025T023000" {
		t.Fatalf("local repeated: %q %q %v", p, v, err)
	}
	// a local time skipped by the spring clock change does not exist
	if _, _, err := start(`"start":"2026-03-29T02:30"`); !errors.Is(err, ErrData) {
		t.Fatalf("skipped local time: %v", err)
	}
	// a recurrence cannot start on the second occurrence
	_, err := RenderICS(decode(t, `{"events":[{"summary":"x","timezone":"Europe/Paris","start":"2026-10-25T01:30:00Z","rrule":{"freq":"DAILY","count":3}}]}`))
	if !errors.Is(err, ErrData) {
		t.Fatalf("recurrence on the second occurrence: %v", err)
	}
}

func TestLastOccurrenceFollowsRFC5545(t *testing.T) {
	wall := func(s string) time.Time {
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name  string
		rule  RRule
		start string
		want  int
	}{
		{"monthly 31st skips short months", RRule{Freq: "MONTHLY", Count: 60}, "2026-01-31", 2034},
		{"yearly Feb 29", RRule{Freq: "YEARLY", Count: 3}, "2028-02-29", 2036},
		{"weekly two days", RRule{Freq: "WEEKLY", Count: 5, ByDay: []string{"MO", "FR"}}, "2026-12-28", 2027},
		{"daily", RRule{Freq: "DAILY", Count: 10}, "2026-12-25", 2027},
		{"one", RRule{Freq: "DAILY", Count: 1}, "2026-12-25", 2026},
		{"interval", RRule{Freq: "MONTHLY", Interval: 12, Count: 3}, "2026-05-10", 2028},
	}
	for _, c := range cases {
		got, err := lastOccurrenceYear(&c.rule, wall(c.start), nil, "rrule")
		if err != nil || got != c.want {
			t.Errorf("%s: %d, %v, want %d", c.name, got, err, c.want)
		}
	}
	if _, err := lastOccurrenceYear(&RRule{Freq: "YEARLY", Count: 200}, wall("2026-01-01"), nil, "rrule"); !errors.Is(err, ErrLimits) {
		t.Errorf("safety bound: %v", err)
	}
}

func TestICSMonthly31VTimezoneCoversTheLastOccurrence(t *testing.T) {
	out, err := RenderICS(decode(t, `{"events":[{"summary":"x","start":"2026-01-31T10:00","timezone":"Europe/Paris","rrule":{"freq":"MONTHLY","count":60}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// the last occurrence is 2034-07-31: the 2034 transitions must be there, and nothing beyond
	if !strings.Contains(s, "DTSTART:20340326T020000") || !strings.Contains(s, "DTSTART:20341029T030000") || strings.Contains(s, "DTSTART:2035") {
		t.Fatalf("VTIMEZONE range is wrong:\n%s", s)
	}
}

func TestVCFExplicitUID(t *testing.T) {
	uid := func(v string) line {
		out, err := RenderVCF(decode(t, `{"cards":[{"fn":"Ada","uid":`+v+`}]}`))
		if err != nil {
			t.Fatal(err)
		}
		return find(parseLines(t, out), "UID")[0]
	}
	// a URI is kept exactly, commas included
	if l := uid(`"https://example.com/a,b"`); l.params != "" || l.value != "https://example.com/a,b" {
		t.Fatalf("URI uid: %+v", l)
	}
	if l := uid(`"urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6"`); l.params != "" || l.value != "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6" {
		t.Fatalf("urn uid: %+v", l)
	}
	// anything else is declared text and escaped
	if l := uid(`"ada, 42; ok"`); l.params != "VALUE=text" || l.value != `ada\, 42\; ok` {
		t.Fatalf("text uid: %+v", l)
	}
	// the derived UID is a URI
	out, _ := RenderVCF(decode(t, `{"cards":[{"fn":"Ada"}]}`))
	if l := find(parseLines(t, out), "UID")[0]; l.params != "" || !strings.HasPrefix(l.value, "urn:uuid:") {
		t.Fatalf("derived uid: %+v", l)
	}
}

func TestICSFractionsOfSecondAreRefused(t *testing.T) {
	for _, ev := range []string{
		`{"summary":"x","start":"2026-11-10T10:00:00.100Z","end":"2026-11-10T10:00:00.200Z"}`,
		`{"summary":"x","start":"2026-11-10T10:00:00.5Z","duration_minutes":5}`,
		`{"summary":"x","start":"2026-11-10T10:00:00,100Z","end":"2026-11-10T10:00:00,200Z"}`,
		`{"summary":"x","start":"2026-11-10T10:00:00,5","timezone":"UTC","duration_minutes":5}`,
		`{"summary":"x","start":"2026-11-10T10:00:00,5","timezone":"Europe/Paris","duration_minutes":5}`,
		`{"summary":"x","start":"2026-11-10T10:00:00Z","dtstamp":"2026-11-01T08:00:00,5Z"}`,
		`{"summary":"x","start":"2026-11-10T10:00:00Z","rrule":{"freq":"DAILY","until":"2026-12-01T00:00:00,5Z"}}`,
		`{"summary":"x","start":"2026-11-10T10:00:00Z","dtstamp":"2026-11-01T08:00:00.250Z"}`,
		`{"summary":"x","start":"2026-11-10T10:00:00Z","rrule":{"freq":"DAILY","until":"2026-12-01T00:00:00.5Z"}}`,
	} {
		if _, err := RenderICS(decode(t, `{"events":[`+ev+`]}`)); !errors.Is(err, ErrData) {
			t.Errorf("%s: %v", ev, err)
		}
	}
}

func TestICSVTimezoneIgnoresOccurrencesInAClockGap(t *testing.T) {
	// 2026-03-29 02:30 does not exist in Paris: the second occurrence is in 2027.
	out, err := RenderICS(decode(t, `{"events":[{"summary":"x","start":"2025-03-29T02:30","timezone":"Europe/Paris","rrule":{"freq":"YEARLY","count":2}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "DTSTART:20270328T020000") {
		t.Fatalf("the VTIMEZONE must reach 2027:\n%s", out)
	}
	rule := &RRule{Freq: "YEARLY", Count: 2}
	loc, _ := loadZone("Europe/Paris")
	start := time.Date(2025, 3, 29, 2, 30, 0, 0, time.UTC)
	if y, err := lastOccurrenceYear(rule, start, loc, "rrule"); err != nil || y != 2027 {
		t.Fatalf("with the zone: %d %v", y, err)
	}
	if y, err := lastOccurrenceYear(rule, start, nil, "rrule"); err != nil || y != 2026 {
		t.Fatalf("without a zone nothing is skipped: %d %v", y, err)
	}
}

// The CSV writer keeps the quoting of encoding/csv in LF mode (so artifact_create_v1 is unchanged) and
// never alters a cell, including a lone CR.
func TestCSVWriterMatchesEncodingCSVAndKeepsEveryCell(t *testing.T) {
	cells := []string{"", "a", "a,b", `x"y`, " lead", "\tlead", "trail ", "l1\nl2", "é😀", `\.`, "a;b", "a|b", "-", "=1+1", "'q"}
	for _, sep := range []string{",", ";", "\t", "|"} {
		for _, c := range cells {
			table := Table{Headers: []string{"h", "k"}, Rows: [][]string{{c, "v"}, {"w", c}}}
			got, err := WriteCSVUnbounded(table, CSVOptions{Separator: sep})
			if err != nil {
				t.Fatal(err)
			}
			var ref bytes.Buffer
			w := csv.NewWriter(&ref)
			w.Comma = rune(sep[0])
			for _, rec := range append([][]string{table.Headers}, table.Rows...) {
				_ = w.Write(rec)
			}
			w.Flush()
			if string(got) != ref.String() {
				t.Errorf("sep %q cell %q: %q, encoding/csv gives %q", sep, c, got, ref.String())
			}
		}
	}
	// A lone CR, a CRLF and an LF survive, in CRLF and LF modes, with either quoting policy.
	for _, q := range []string{"minimal", "all"} {
		for _, eol := range []string{"lf", "crlf"} {
			data := `{"headers":["texte"],"rows":[["a\rb"],["c\r\nd"],["e\nf"]]}`
			out, err := RenderCSV(decode(t, data), decode(t, `{"quote":"`+q+`","line_ending":"`+eol+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			r := csv.NewReader(bytes.NewReader(out))
			recs, err := r.ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			// encoding/csv reads CRLF inside a quoted field as LF; the bytes are checked directly.
			for _, want := range []string{"\"a\rb\"", "\"c\r\nd\"", "\"e\nf\""} {
				if !strings.Contains(string(out), want) {
					t.Errorf("%s/%s: %q lost from %q", q, eol, want, out)
				}
			}
			if len(recs) != 4 {
				t.Errorf("%s/%s: %d records", q, eol, len(recs))
			}
		}
	}
}

func TestICSVTimezoneCoversTheEffectiveEnd(t *testing.T) {
	render := func(ev string) string {
		out, err := RenderICS(decode(t, `{"events":[`+ev+`]}`))
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	has2027 := func(s string) bool { return strings.Contains(s, "DTSTART:20270328T020000") }
	// 366 days from 2026-12-31: the 2027 clock changes are inside the event
	if s := render(`{"summary":"x","start":"2026-12-31T10:00","timezone":"Europe/Paris","duration_minutes":527040}`); !has2027(s) {
		t.Fatalf("duration_minutes: the VTIMEZONE stops in 2026:\n%s", s)
	}
	if s := render(`{"summary":"x","start":"2026-12-31T10:00","timezone":"Europe/Paris","end":"2027-12-31T10:00"}`); !has2027(s) {
		t.Fatal("end: the VTIMEZONE stops in 2026")
	}
	// a short event that stays in 2026 does not reach 2027
	if s := render(`{"summary":"x","start":"2026-12-31T10:00","timezone":"Europe/Paris","duration_minutes":60}`); has2027(s) {
		t.Fatal("a one hour event must not extend the VTIMEZONE")
	}
	// the last occurrence of a recurrence crosses the year with its duration
	if s := render(`{"summary":"x","start":"2026-12-31T23:00","timezone":"Europe/Paris","duration_minutes":527040,"rrule":{"freq":"YEARLY","count":2}}`); !strings.Contains(s, "DTSTART:2028") {
		t.Fatalf("recurrence plus duration:\n%s", s)
	}
}
