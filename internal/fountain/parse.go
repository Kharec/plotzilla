// Package fountain reads and writes the Fountain plain-text screenplay
// format (https://fountain.io/syntax).
package fountain

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"plotzilla/internal/screenplay"
)

var (
	sceneRe       = regexp.MustCompile(`(?i)^(INT|EXT|EST|INT\.?/EXT|I/E)[. ]`)
	sceneNumberRe = regexp.MustCompile(`\s*#([\w.\-]+)#\s*$`)
	transitionRe  = regexp.MustCompile(`^[^a-z]*TO:$`)
	titleKeyRe    = regexp.MustCompile(`^([A-Za-z][A-Za-z ]*):\s*(.*)$`)
	boneyardRe    = regexp.MustCompile(`(?s)/\*.*?\*/`)
	pageBreakRe   = regexp.MustCompile(`^={3,}$`)
	// Plotzilla's own data lives in boneyard comments, which other Fountain
	// apps ignore: the document settings in one block at the end, and
	// revision and locked-page markers inline, where they travel with the
	// text they mark.
	settingsRe = regexp.MustCompile(`(?s)/\*\s*Plotzilla (\{.*?\})\s*\*/`)
	markerRe   = regexp.MustCompile(`/\*(rev \d+|/rev|page [^*\s]+)\*/`)
	noteRe     = regexp.MustCompile(`(?s)\[\[.*?\]\]`)
	tokenRe    = regexp.MustCompile("\uE000[^\uE001]*\uE001")
	// Fountain has no shot element: shots are all-caps action lines.
	shotRe = regexp.MustCompile(`^(INSERT|CLOSE ON|CLOSE UP|EXTREME CLOSE|ECU|CU|ANGLE ON|NEW ANGLE|REVERSE ANGLE|POV|WIDE ON|WIDE SHOT|OVERHEAD|BACK TO SCENE|TRACKING|AERIAL)\b`)
)

// Parse converts Fountain source into a Document. Boneyard that is not
// Plotzilla's own data is dropped.
func Parse(src string) *screenplay.Document {
	src = strings.TrimPrefix(src, "\ufeff")
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")

	doc := &screenplay.Document{}
	if m := settingsRe.FindAllStringSubmatchIndex(src, -1); len(m) > 0 {
		last := m[len(m)-1]
		doc.Settings = parseSettings(src[last[2]:last[3]])
		src = src[:last[0]] + src[last[1]:]
	}
	p := tokenize(src)
	lines := strings.Split(p.src, "\n")
	start := parseTitlePage(lines, &doc.TitlePage)
	doc.Elements = p.elements(lines[start:])
	if len(doc.Elements) == 0 {
		doc.Elements = []screenplay.Element{screenplay.NewElement(screenplay.Scene, "")}
	}
	return doc
}

func parseSettings(data string) screenplay.Settings {
	var s screenplay.Settings
	if json.Unmarshal([]byte(data), &s) != nil {
		return screenplay.Settings{}
	}
	if s.Paper != screenplay.A4 {
		s.Paper = screenplay.Letter
	}
	return s
}

// ParseText parses body text (no title page), such as pasted plain text.
func ParseText(text string) []screenplay.Element {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	p := tokenize(text)
	return p.elements(strings.Split(p.src, "\n"))
}

// ParseElements parses body lines (no title page).
func ParseElements(lines []string) []screenplay.Element {
	return ParseText(strings.Join(lines, "\n"))
}

// parser holds the source once notes and markers have been replaced by
// tokens: U+E000, a payload, U+E001. Tokens contain no line break, so a
// note spanning lines no longer splits its paragraph, and line rules look
// at the text without them (bare).
type parser struct {
	src   string
	notes []string
}

func token(payload string) string { return "\uE000" + payload + "\uE001" }

// bare removes the tokens: what line classification looks at.
func bare(s string) string { return strings.TrimSpace(tokenRe.ReplaceAllString(s, "")) }

func tokenize(src string) *parser {
	src = markerRe.ReplaceAllStringFunc(src, func(m string) string {
		body := m[2 : len(m)-2]
		switch {
		case body == "/rev":
			return token("r")
		case strings.HasPrefix(body, "rev "):
			return token("r" + body[4:])
		}
		return token("p" + strings.TrimPrefix(body, "page "))
	})
	src = boneyardRe.ReplaceAllString(src, "")
	p := &parser{}
	src = noteRe.ReplaceAllStringFunc(src, func(m string) string {
		note, ok := noteText(m[2 : len(m)-2])
		if !ok {
			return m
		}
		p.notes = append(p.notes, note)
		return token("n" + strconv.Itoa(len(p.notes)-1))
	})
	p.src = src
	return p
}

// noteText validates a note's content: it may span lines, but an empty
// line inside it must hold two spaces, as in dialogue.
func noteText(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines)-1; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			if len(lines[i]) < 2 {
				return "", false
			}
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n"), true
}

func (p *parser) elements(lines []string) []screenplay.Element {
	var out []screenplay.Element
	add := func(t screenplay.ElementType, text string) {
		runs, locks := p.inline(text)
		out = append(out, screenplay.Element{Type: t, Runs: runs, Locked: locks})
	}
	blank := func(i int) bool { return i < 0 || i >= len(lines) || strings.TrimSpace(lines[i]) == "" }

	prevBlank := true
	newPage := false // a "===" line: the next element starts a page
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			prevBlank = true
			continue
		}
		nextBlank := blank(i + 1)
		b := bare(trimmed)
		if pageBreakRe.MatchString(b) {
			newPage = true
			prevBlank = true
			continue
		}
		first := len(out)

		switch {
		case strings.HasPrefix(b, "#"):
			text := strings.TrimLeft(trimmed, "#")
			add(screenplay.Section, strings.TrimSpace(text))
			out[len(out)-1].Level = max(len(b)-len(strings.TrimLeft(b, "#")), 1)

		case strings.HasPrefix(b, "="):
			add(screenplay.Synopsis, strings.TrimSpace(strings.TrimPrefix(trimmed, "=")))

		case strings.HasPrefix(b, ">") && strings.HasSuffix(b, "<") && len(b) > 1:
			// Fountain only knows centred text; an all-caps one is read as
			// an intertitle (title card), which is how Plotzilla writes them.
			text := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, ">"), "<"))
			if isCharacterCue(bare(text)) {
				add(screenplay.Intertitle, text)
			} else {
				add(screenplay.Centered, text)
			}

		case prevBlank && nextBlank && shotRe.MatchString(b) && isCharacterCue(b):
			add(screenplay.Shot, trimmed)

		case strings.HasPrefix(b, ">"):
			add(screenplay.Transition, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))

		case strings.HasPrefix(b, ".") && !strings.HasPrefix(b, ".."):
			p.addScene(&out, strings.TrimPrefix(trimmed, "."))

		case prevBlank && sceneRe.MatchString(b):
			p.addScene(&out, trimmed)

		case prevBlank && nextBlank && transitionRe.MatchString(b):
			add(screenplay.Transition, trimmed)

		case prevBlank && !nextBlank && !strings.HasPrefix(b, "!") &&
			(strings.HasPrefix(b, "@") || isCharacterCue(b)):
			cue := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "@"), "^"))
			add(screenplay.Character, cue)
			out[len(out)-1].Dual = strings.HasSuffix(b, "^")
			i = p.dialogue(lines, i+1, &out)

		default:
			// Action: consecutive non-blank lines form one paragraph.
			para := []string{strings.TrimPrefix(strings.TrimPrefix(trimmed, "!"), "~")}
			for !blank(i + 1) {
				i++
				para = append(para, strings.TrimSpace(strings.TrimPrefix(lines[i], "~")))
			}
			add(screenplay.Action, strings.Join(para, "\n"))
		}
		if newPage && len(out) > first {
			out[first].NewPage = true
			newPage = false
		}
		prevBlank = false
	}
	return out
}

func (p *parser) addScene(out *[]screenplay.Element, text string) {
	el := screenplay.Element{Type: screenplay.Scene}
	if m := sceneNumberRe.FindStringSubmatch(text); m != nil {
		el.SceneNumber = m[1]
		text = text[:len(text)-len(m[0])]
	}
	el.Runs, el.Locked = p.inline(strings.TrimSpace(text))
	*out = append(*out, el)
}

// dialogue consumes the lines of a dialogue block starting at i and
// returns the index of the last consumed line.
func (p *parser) dialogue(lines []string, i int, out *[]screenplay.Element) int {
	var cur []string
	add := func(t screenplay.ElementType, text string) {
		runs, locks := p.inline(text)
		*out = append(*out, screenplay.Element{Type: t, Runs: runs, Locked: locks})
	}
	flush := func() {
		if len(cur) > 0 {
			add(screenplay.Dialogue, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for ; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// Two spaces keep an intentional empty line inside dialogue.
			if len(line) >= 2 && len(cur) > 0 {
				cur = append(cur, "")
				continue
			}
			break
		}
		if b := bare(trimmed); strings.HasPrefix(b, "(") && strings.HasSuffix(b, ")") {
			flush()
			add(screenplay.Parenthetical, trimmed)
			continue
		}
		cur = append(cur, trimmed)
	}
	flush()
	return i - 1
}

func isCharacterCue(s string) bool {
	name := strings.TrimSuffix(s, "^")
	if j := strings.Index(name, "("); j >= 0 {
		name = name[:j]
	}
	hasLetter := false
	for _, r := range name {
		if unicode.IsLetter(r) {
			hasLetter = true
			if unicode.IsLower(r) {
				return false
			}
		}
	}
	return hasLetter
}

// parseTitlePage reads "Key: value" pairs at the top of the file and returns
// the index of the first body line.
func parseTitlePage(lines []string, tp *screenplay.TitlePage) int {
	if len(lines) == 0 || !titleKeyRe.MatchString(lines[0]) {
		return 0
	}
	var key string
	values := map[string][]string{}
	i := 0
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		if m := titleKeyRe.FindStringSubmatch(line); m != nil && !startsIndented(line) {
			key = strings.ToLower(strings.TrimSpace(m[1]))
			if v := strings.TrimSpace(m[2]); v != "" {
				values[key] = append(values[key], v)
			}
			continue
		}
		if key != "" {
			values[key] = append(values[key], strings.TrimSpace(line))
		}
	}
	get := func(keys ...string) string {
		var parts []string
		for _, k := range keys {
			for _, v := range values[k] {
				parts = append(parts, plain(v))
			}
		}
		return strings.Join(parts, "\n")
	}
	tp.Title = get("title")
	tp.Credit = get("credit")
	tp.Author = get("author", "authors")
	tp.Source = get("source")
	tp.DraftDate = get("draft date", "date")
	tp.Contact = get("contact")
	tp.Notes = get("notes", "copyright")
	return i
}

func startsIndented(line string) bool {
	return strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "   ")
}

// plain drops emphasis, notes and markers from a title page value.
func plain(s string) string {
	var b strings.Builder
	for _, r := range parseInline(s) {
		b.WriteString(r.Text)
	}
	return b.String()
}

// parseInline parses emphasis; notes are dropped.
func parseInline(s string) []screenplay.Run {
	runs, _ := (&parser{}).inline(s)
	return runs
}

// inline turns Fountain emphasis (**bold**, *italic*, _underline_, with
// backslash escapes) into runs, and tokens into notes, revision levels and
// page locks. A marker only opens a span when a matching marker follows on
// the same line, so stray asterisks stay literal.
func (p *parser) inline(s string) ([]screenplay.Run, []screenplay.PageLock) {
	var runs []screenplay.Run
	var locks []screenplay.PageLock
	var cur screenplay.Run
	var buf strings.Builder
	offset := 0 // runes of element text so far
	flush := func() {
		if buf.Len() > 0 {
			cur.Text = buf.String()
			runs = append(runs, cur)
			buf.Reset()
		}
	}
	rs := []rune(s)
	closes := func(from int, marker string) bool {
		rest := string(rs[from:])
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[:nl]
		}
		return strings.Contains(rest, marker)
	}
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\uE000':
			end := i + 1
			for end < len(rs) && rs[end] != '\uE001' {
				end++
			}
			payload := string(rs[i+1 : min(end, len(rs))])
			i = end
			if payload == "" {
				continue
			}
			arg := payload[1:]
			switch payload[0] {
			case 'n':
				if n, err := strconv.Atoi(arg); err == nil && n < len(p.notes) {
					flush()
					runs = append(runs, screenplay.Run{Text: p.notes[n], Note: true, Rev: cur.Rev})
					offset += len([]rune(p.notes[n]))
				}
			case 'r':
				flush()
				cur.Rev, _ = strconv.Atoi(arg)
			case 'p':
				locks = append(locks, screenplay.PageLock{Offset: offset, Page: arg})
			}
			continue
		case c == '\\' && i+1 < len(rs) && strings.ContainsRune(`*_\`, rs[i+1]):
			buf.WriteRune(rs[i+1])
			offset++
			i++
		case c == '*' && i+1 < len(rs) && rs[i+1] == '*' && (cur.Bold || closes(i+2, "**")):
			flush()
			cur.Bold = !cur.Bold
			i++
		case c == '*' && (cur.Italic || closes(i+1, "*")):
			flush()
			cur.Italic = !cur.Italic
		case c == '_' && (cur.Underline || closes(i+1, "_")):
			flush()
			cur.Underline = !cur.Underline
		default:
			buf.WriteRune(c)
			offset++
		}
	}
	flush()
	return screenplay.NormalizeRuns(runs), locks
}
