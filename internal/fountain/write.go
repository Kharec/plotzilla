package fountain

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"plotzilla/internal/screenplay"
)

// Write serialises a Document to Fountain. Elements that Fountain would
// otherwise misread are written with the explicit "force" prefixes.
func Write(doc *screenplay.Document) string {
	var b strings.Builder
	writeTitlePage(&b, doc.TitlePage)

	var prev screenplay.ElementType
	var carried []screenplay.PageLock // locks of skipped empty elements
	carriedPage := false              // and their forced page break
	first := true
	for _, el := range doc.Elements {
		if el.IsEmpty() {
			for _, l := range el.Locked {
				carried = append(carried, screenplay.PageLock{Page: l.Page})
			}
			carriedPage = carriedPage || el.NewPage
			continue
		}
		locks := append(carried, el.Locked...)
		newPage := carriedPage || el.NewPage
		carried, carriedPage = nil, false
		inBlock := el.Type.InDialogue() && (prev == screenplay.Character || prev.InDialogue())
		if !first && !inBlock {
			b.WriteString("\n")
		}
		// "===" cannot sit inside a dialogue block: layout breaks before the
		// block anyway.
		if newPage && !inBlock {
			b.WriteString("===\n\n")
		}
		first = false
		prev = el.Type

		if el.Type == screenplay.Intertitle || el.Type == screenplay.Shot {
			el = upper(el)
		}
		// Notes keep their line breaks where Fountain keeps the element's.
		keepLines := el.Type == screenplay.Action || el.Type == screenplay.Dialogue
		text := markup(el, locks, keepLines)
		plainText := el.PrintedText()
		switch el.Type {
		// Case is kept as typed ("McCLANE"); layout upper-cases when printing.
		case screenplay.Scene:
			if !sceneRe.MatchString(plainText) {
				text = "." + text
			}
			if el.SceneNumber != "" {
				text += " #" + el.SceneNumber + "#"
			}
		case screenplay.Character:
			if sceneRe.MatchString(plainText) || !isCharacterCue(plainText) {
				text = "@" + text
			}
			if el.Dual {
				text += " ^"
			}
		case screenplay.Parenthetical:
			text = strings.ReplaceAll(text, "\n", " ")
			if !strings.HasPrefix(plainText, "(") {
				text = "(" + text
			}
			if !strings.HasSuffix(plainText, ")") {
				text += ")"
			}
		case screenplay.Dialogue:
			text = strings.ReplaceAll(text, "\n\n", "\n  \n")
		case screenplay.Transition:
			if !transitionRe.MatchString(plainText) {
				text = "> " + text
			}
		case screenplay.Centered, screenplay.Intertitle:
			text = "> " + strings.ReplaceAll(text, "\n", " <\n> ") + " <"
		case screenplay.Shot:
			// Written as an all-caps line; read back as a shot when it
			// starts like one (INSERT, CLOSE ON, POV, ...).
			text = strings.ReplaceAll(text, "\n", " ")
			if check := strings.ReplaceAll(plainText, "\n", " "); needsForcedAction(check) && !shotRe.MatchString(check) {
				text = "!" + text
			}
		case screenplay.Section:
			text = strings.Repeat("#", max(el.Level, 1)) + " " + strings.ReplaceAll(text, "\n", " ")
		case screenplay.Synopsis:
			text = "= " + strings.ReplaceAll(text, "\n", " ")
		default:
			if needsForcedAction(plainText) {
				text = "!" + text
			}
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	writeSettings(&b, doc.Settings)
	return b.String()
}

// writeSettings stores the production settings in a boneyard block that
// other Fountain apps ignore.
func writeSettings(b *strings.Builder, s screenplay.Settings) {
	if s.IsZero() {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	// "*/" can only occur inside a JSON string, where "\/" means "/".
	b.WriteString("\n/* Plotzilla " + strings.ReplaceAll(string(data), "*/", `*\/`) + " */\n")
}

// upper capitalises the text of an element that is always written in
// capitals, leaving its notes alone.
func upper(el screenplay.Element) screenplay.Element {
	runs := make([]screenplay.Run, len(el.Runs))
	for i, r := range el.Runs {
		if !r.Note {
			r.Text = screenplay.UpperRunes(r.Text)
		}
		runs[i] = r
	}
	el.Runs = runs
	return el
}

func needsForcedAction(s string) bool {
	first := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		first = s[:i]
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return false
	}
	if strings.ContainsRune(".>@#=~!", rune(first[0])) {
		return true
	}
	if sceneRe.MatchString(first) || transitionRe.MatchString(first) {
		return true
	}
	// An all-caps first line followed by more lines would read as a character.
	return strings.Contains(s, "\n") && isCharacterCue(first)
}

func writeTitlePage(b *strings.Builder, tp screenplay.TitlePage) {
	if tp.IsEmpty() {
		return
	}
	field := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		lines := strings.Split(value, "\n")
		if len(lines) == 1 {
			b.WriteString(key + ": " + value + "\n")
			return
		}
		b.WriteString(key + ":\n")
		for _, l := range lines {
			b.WriteString("    " + strings.TrimSpace(l) + "\n")
		}
	}
	field("Title", tp.Title)
	field("Credit", tp.Credit)
	field("Author", tp.Author)
	field("Source", tp.Source)
	field("Draft date", tp.DraftDate)
	field("Contact", tp.Contact)
	field("Notes", tp.Notes)
	b.WriteString("\n")
}

// markup renders runs with Fountain emphasis markers, notes ("[[…]]"),
// revision markers ("/*rev 2*/…/*/rev*/") and locked page starts
// ("/*page 12*/"). Emphasis cannot span lines in Fountain, so markers are
// closed and reopened around "\n".
func markup(el screenplay.Element, locks []screenplay.PageLock, keepNoteLines bool) string {
	var b strings.Builder
	pos, next, rev := 0, 0, 0
	emitLocks := func() {
		for ; next < len(locks) && locks[next].Offset <= pos; next++ {
			b.WriteString("/*page " + locks[next].Page + "*/")
		}
	}
	for _, r := range splitRuns(el.Runs, locks) {
		emitLocks()
		if r.Rev != rev {
			if rev > 0 {
				b.WriteString("/*/rev*/")
			}
			if r.Rev > 0 {
				b.WriteString("/*rev " + strconv.Itoa(r.Rev) + "*/")
			}
			rev = r.Rev
		}
		pos += len([]rune(r.Text))
		if r.Note {
			b.WriteString("[[" + noteMarkup(r.Text, keepNoteLines) + "]]")
			continue
		}
		open, close := "", ""
		if r.Underline {
			open += "_"
		}
		if r.Bold {
			open += "**"
		}
		if r.Italic {
			open += "*"
		}
		for i := len(open) - 1; i >= 0; i-- {
			close += string(open[i])
		}
		for i, line := range strings.Split(r.Text, "\n") {
			if i > 0 {
				b.WriteString("\n")
			}
			if strings.TrimSpace(line) == "" || open == "" {
				b.WriteString(escape(line))
				continue
			}
			// Keep surrounding spaces outside the markers.
			lead := len(line) - len(strings.TrimLeftFunc(line, unicode.IsSpace))
			trail := len(strings.TrimRightFunc(line, unicode.IsSpace))
			b.WriteString(line[:lead] + open + escape(line[lead:trail]) + close + line[trail:])
		}
	}
	if rev > 0 {
		b.WriteString("/*/rev*/")
	}
	emitLocks()
	return b.String()
}

// splitRuns cuts runs where a page lock falls, so that its marker can be
// written between two runs.
func splitRuns(runs []screenplay.Run, locks []screenplay.PageLock) []screenplay.Run {
	if len(locks) == 0 {
		return runs
	}
	var out []screenplay.Run
	pos := 0
	for _, r := range runs {
		rs := []rune(r.Text)
		start := 0
		for _, l := range locks {
			if at := l.Offset - pos; at > start && at < len(rs) {
				part := r
				part.Text = string(rs[start:at])
				out = append(out, part)
				start = at
			}
		}
		r.Text = string(rs[start:])
		out = append(out, r)
		pos += len(rs)
	}
	return out
}

func noteMarkup(s string, keepLines bool) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "[[", "[ ["), "]]", "] ]")
	if !keepLines {
		return strings.ReplaceAll(s, "\n", " ")
	}
	// An empty line inside a note holds two spaces, as in dialogue.
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines)-1; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			lines[i] = "  "
		}
	}
	return strings.Join(lines, "\n")
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "*", `\*`)
	return strings.ReplaceAll(s, "_", `\_`)
}
