// Package translate translates a screenplay from French into English,
// element by element, with the conventions of each element type: a French
// slugline becomes "INT. KITCHEN - NIGHT", "(V.O.)" and "(SUITE)" become
// "(V.O.)" and "(CONT'D)", "FONDU AU NOIR" becomes "FADE TO BLACK.", and so
// on. Free text (action, dialogue, parentheticals, locations) goes through
// a machine translation model, one sentence at a time. Text that does not
// read as French is left untouched, so a script can be translated piece by
// piece, and translating it again changes nothing.
package translate

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"plotzilla/internal/screenplay"
)

// Translator translates one French sentence into English. It must be
// safe for concurrent use.
type Translator interface {
	Translate(ctx context.Context, text string) (string, error)
}

// Options says what to translate.
type Options struct {
	// Elements are the indexes of the elements to translate (flat order,
	// as everywhere else); nil translates every element.
	Elements []int
	// TitlePage also translates the title page credit, source, draft date
	// and notes. The title itself is never translated.
	TitlePage bool
	// Workers is how many sentences are translated at once (default 1).
	Workers int
	// Progress is called after each sentence, from any goroutine.
	Progress func(done, total int)
}

// Change is a translated element, to put in place of element Index.
type Change struct {
	Index   int                `json:"index"`
	Element screenplay.Element `json:"element"`
}

// Result lists what changed. TitlePage is nil when it did not change.
type Result struct {
	Changes   []Change              `json:"changes"`
	TitlePage *screenplay.TitlePage `json:"titlePage,omitempty"`
}

// Document translates the French text of doc. It does not modify doc.
func Document(ctx context.Context, doc *screenplay.Document, tr Translator, lex Lexicon, opts Options) (Result, error) {
	s := newSession(doc, lex)
	indexes := opts.Elements
	if indexes == nil {
		indexes = make([]int, len(doc.Elements))
		for i := range indexes {
			indexes[i] = i
		}
	}
	// First pass: collect the sentences to translate. Second pass, once
	// they are translated: build the elements from them.
	s.collect = true
	s.run(doc, indexes, opts.TitlePage)
	done, err := s.translateAll(ctx, tr, max(opts.Workers, 1), opts.Progress)
	if err != nil {
		return Result{}, err
	}
	s.collect, s.done = false, done
	return s.run(doc, indexes, opts.TitlePage), nil
}

type session struct {
	lex Lexicon
	// names are the script's characters, lower case, longest first: they
	// are kept as written instead of being translated ("Marie" would
	// become "Mary").
	names     [][]rune
	nameWords map[string]bool
	// frenchAt says, for each element, whether it or (when it is
	// ambiguous) its nearest unambiguous neighbour is French;
	// frenchContext is that value for the element being translated.
	frenchAt      []bool
	frenchContext bool
	collect       bool
	wanted        []string
	seen          map[string]bool
	done          map[string]string
}

// determiners start a character cue that is a description to translate
// ("LE POLICIER") rather than a name.
var determiners = set("le la les l un une des mon ma mes ton ta tes son sa ses notre votre leur")

func newSession(doc *screenplay.Document, lex Lexicon) *session {
	s := &session{lex: lex, nameWords: map[string]bool{}, seen: map[string]bool{}}
	seen := map[string]bool{}
	for _, el := range doc.Elements {
		if el.Type != screenplay.Character {
			continue
		}
		name := strings.ToLower(screenplay.CharacterName(el.PrintedText()))
		ws := words(name)
		if name == "" || seen[name] || len(ws) == 0 || determiners[strings.TrimSuffix(firstElision(ws[0]), "'")] {
			continue
		}
		seen[name] = true
		s.names = append(s.names, []rune(name))
		for _, w := range ws {
			s.nameWords[w] = true
		}
	}
	s.frenchAt = s.languages(doc)
	// Longest first: "Marie-Claire" before "Marie".
	for i := 1; i < len(s.names); i++ {
		for j := i; j > 0 && len(s.names[j]) > len(s.names[j-1]); j-- {
			s.names[j], s.names[j-1] = s.names[j-1], s.names[j]
		}
	}
	return s
}

// languages decides the language of each element: French, English, or,
// for an element without clear evidence ("Attends !", "Oui ?" are short;
// a character cue is a name), the language of its nearest decided
// neighbour.
func (s *session) languages(doc *screenplay.Document) []bool {
	n := len(doc.Elements)
	lang := make([]int, n) // 1 French, -1 English, 0 undecided
	for i, el := range doc.Elements {
		fr, en := scores(el.Text(), s.lex, s.nameWords)
		switch {
		case fr > en:
			lang[i] = 1
		case en > fr:
			lang[i] = -1
		}
	}
	out := make([]bool, n)
	for i := range n {
		if lang[i] != 0 {
			out[i] = lang[i] > 0
			continue
		}
		for d := 1; d < n; d++ {
			if i-d >= 0 && lang[i-d] != 0 {
				out[i] = lang[i-d] > 0
				break
			}
			if i+d < n && lang[i+d] != 0 {
				out[i] = lang[i+d] > 0
				break
			}
		}
	}
	return out
}

// firstElision returns "l'" for "l'inspecteur", the word itself otherwise.
func firstElision(w string) string {
	if i := strings.IndexByte(w, '\''); i > 0 {
		return w[:i+1]
	}
	return w
}

func (s *session) run(doc *screenplay.Document, indexes []int, titlePage bool) Result {
	res := Result{Changes: []Change{}}
	for _, i := range indexes {
		if i < 0 || i >= len(doc.Elements) {
			continue
		}
		s.frenchContext = s.frenchAt[i]
		if el, changed := s.element(doc.Elements[i]); changed {
			res.Changes = append(res.Changes, Change{Index: i, Element: el})
		}
	}
	if titlePage {
		s.frenchContext = false
		tp := doc.TitlePage
		tp.Credit = s.lines(tp.Credit, s.credit)
		tp.Source = s.lines(tp.Source, s.prose)
		tp.DraftDate = s.lines(tp.DraftDate, s.prose)
		tp.Notes = s.lines(tp.Notes, s.prose)
		if tp != doc.TitlePage {
			res.TitlePage = &tp
		}
	}
	return res
}

// ------------------------------------------------------------- elements

// element translates el. Notes are translated on their own; the rest of
// the text as a whole, so sentences keep their meaning. Emphasis that
// covers only part of a translated stretch cannot follow the words: the
// translation keeps the styles common to the whole stretch.
func (s *session) element(el screenplay.Element) (screenplay.Element, bool) {
	var runs []screenplay.Run
	changed := false
	if t := s.retype(el); t != el.Type {
		el.Type, changed = t, true
	}
	rule := s.rule(el.Type)
	for _, chunk := range chunks(el.Runs) {
		var b strings.Builder
		for _, r := range chunk {
			b.WriteString(r.Text)
		}
		text := b.String()
		var out string
		if chunk[0].Note {
			out = s.lines(text, s.prose)
		} else {
			out = s.lines(text, rule)
		}
		if out == text {
			runs = append(runs, chunk...)
			continue
		}
		changed = true
		style := commonStyle(chunk)
		style.Text = out
		runs = append(runs, style)
	}
	if !changed {
		return el, false
	}
	oldLen := len([]rune(el.Text()))
	el.Runs = screenplay.NormalizeRuns(runs)
	el.Locked = remapLocks(el.Locked, oldLen, len([]rune(el.Text())))
	return el, true
}

// retype recognises a French transition or shot typed as action, as the
// Fountain format reads "FONDU AU NOIR." or "GROS PLAN SUR la lettre" (it
// only knows the English forms): the element becomes a transition or a
// shot, as the English text will be read back.
func (s *session) retype(el screenplay.Element) screenplay.ElementType {
	if el.Type != screenplay.Action || len(el.Runs) == 0 || slices.ContainsFunc(el.Runs, func(r screenplay.Run) bool { return r.Note }) {
		return el.Type
	}
	line := strings.TrimSpace(el.Text())
	if strings.Contains(line, "\n") {
		return el.Type
	}
	if key := fold(strings.TrimRight(line, ":.  \u202f")); transitions[key] != "" && key != "FIN" && isCaps(line) {
		return screenplay.Transition
	}
	fields := strings.Fields(line)
	for _, sh := range shots {
		n := len(strings.Fields(sh.french))
		if len(fields) >= n && isCaps(strings.Join(fields[:n], " ")) && fold(strings.Join(fields[:n], " ")) == sh.french {
			return screenplay.Shot
		}
	}
	return el.Type
}

func (s *session) rule(t screenplay.ElementType) func(string) string {
	switch t {
	case screenplay.Scene:
		return s.slugline
	case screenplay.Character:
		return s.cue
	case screenplay.Parenthetical:
		return s.parenthetical
	case screenplay.Transition:
		return s.transition
	case screenplay.Shot:
		return s.shot
	case screenplay.Centered:
		return s.centered
	}
	return s.prose
}

// chunks groups runs into stretches of text: each note alone, the text
// between notes together.
func chunks(runs []screenplay.Run) [][]screenplay.Run {
	var out [][]screenplay.Run
	for i, r := range runs {
		if i == 0 || r.Note || runs[i-1].Note {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], r)
	}
	return out
}

func commonStyle(runs []screenplay.Run) screenplay.Run {
	c := runs[0]
	c.Text = ""
	for _, r := range runs[1:] {
		c.Bold = c.Bold && r.Bold
		c.Italic = c.Italic && r.Italic
		c.Underline = c.Underline && r.Underline
		if r.Rev != c.Rev {
			c.Rev = 0
		}
	}
	return c
}

// remapLocks keeps the locked pages of a translated element in proportion
// to the text: the page that started halfway through starts halfway
// through the translation.
func remapLocks(locks []screenplay.PageLock, oldLen, newLen int) []screenplay.PageLock {
	if len(locks) == 0 {
		return locks
	}
	out := make([]screenplay.PageLock, len(locks))
	prev := 0
	for i, l := range locks {
		off := 0
		if oldLen > 0 {
			off = (l.Offset*newLen + oldLen/2) / oldLen
		}
		off = min(max(off, prev), newLen)
		out[i] = screenplay.PageLock{Offset: off, Page: l.Page}
		prev = off
	}
	return out
}

// lines applies fn to each line of text, keeping the line breaks and the
// spaces around each line.
func (s *session) lines(text string, fn func(string) string) string {
	parts := strings.Split(text, "\n")
	for i, line := range parts {
		core := strings.TrimSpace(line)
		if core == "" {
			continue
		}
		lead := line[:strings.Index(line, core)]
		trail := line[len(lead)+len(core):]
		parts[i] = lead + fn(core) + trail
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------- rules

// prose translates free text sentence by sentence, leaving the sentences
// that are not French.
//
// A sentence with as much French as English evidence ("Attends !": to
// attend is English too, "Marie ?") is translated when the text around it
// is French: other sentences of the text, or the neighbouring elements.
func (s *session) prose(text string) string {
	list := sentences(text)
	type verdict struct{ fr, en int }
	verdicts := make([]verdict, len(list))
	frenchAround := s.frenchContext
	for i, sentence := range list {
		fr, en := scores(sentence, s.lex, s.nameWords)
		verdicts[i] = verdict{fr, en}
		frenchAround = frenchAround || fr > en
	}
	var b strings.Builder
	for i, sentence := range list {
		core := strings.TrimRightFunc(sentence, unicode.IsSpace)
		v := verdicts[i]
		if v.fr > v.en || (v.fr == v.en && frenchAround && hasLetter(core)) {
			b.WriteString(s.mt(core, modeOf(core)))
		} else {
			b.WriteString(core)
		}
		b.WriteString(sentence[len(core):])
	}
	return b.String()
}

// french reports whether a line reads as French, or could be French and
// sits among French elements.
func (s *session) french(text string) bool {
	fr, en := scores(text, s.lex, s.nameWords)
	return fr > en || (fr == en && s.frenchContext && hasLetter(text))
}

func hasLetter(text string) bool { return strings.IndexFunc(text, unicode.IsLetter) >= 0 }

var (
	slugPrefix = regexp.MustCompile(`(?i)^((?:INT\.?\s*/\s*EXT|EXT\.?\s*/\s*INT|I\s*/\s*E|INT|EXT|EST|INT[ÉE]RIEUR|EXT[ÉE]RIEUR)\.?)\s+(.*)$`)
	slugDash   = regexp.MustCompile(`\s+[-–—]+\s+`)
	trailNote  = regexp.MustCompile(`^(.*?)\s*\(([^()]*)\)$`)
)

// slugline translates a French scene heading: "INT. CUISINE DE MARIE -
// NUIT" → "INT. MARIE'S KITCHEN - NIGHT". Times of day and the usual notes
// come from a table, locations from the model.
func (s *session) slugline(line string) string {
	prefix, rest := "", line
	if m := slugPrefix.FindStringSubmatch(line); m != nil {
		prefix, rest = m[1], m[2]
	}
	note := ""
	if m := trailNote.FindStringSubmatch(rest); m != nil && m[1] != "" {
		rest, note = m[1], m[2]
	}
	parts := slugDash.Split(rest, -1)
	locations, time := parts, ""
	if len(parts) > 1 {
		locations, time = parts[:len(parts)-1], parts[len(parts)-1]
	}
	frTime, isFrTime := frenchTimes[fold(time)]
	_, isEnTime := englishTimes[fold(time)]
	_, isFrNote := sceneNotes[fold(note)]
	fp := fold(prefix)
	french := isFrTime || strings.HasPrefix(fp, "INTERIEUR") || strings.HasPrefix(fp, "EXTERIEUR") ||
		(!isEnTime && (s.french(strings.Join(locations, " ")) || isFrNote && time == ""))
	if !french {
		return line
	}

	switch {
	case strings.HasPrefix(fp, "INTERIEUR"):
		prefix = "INT."
	case strings.HasPrefix(fp, "EXTERIEUR"):
		prefix = "EXT."
	}
	out := make([]string, 0, len(parts))
	for _, loc := range locations {
		if s.notEnglish(loc) {
			loc = s.mt(loc, location)
		}
		out = append(out, loc)
	}
	switch {
	case isFrTime:
		out = append(out, frTime)
	case time != "" && !isEnTime && s.notEnglish(time):
		out = append(out, s.mt(time, caps))
	case time != "":
		out = append(out, time)
	}
	heading := strings.Join(out, " - ")
	if prefix != "" {
		heading = prefix + " " + heading
	}
	if note != "" {
		if en, ok := sceneNotes[fold(note)]; ok {
			note = en
		} else if s.french(note) {
			note = s.mt(note, caps)
		}
		heading += " (" + note + ")"
	}
	return heading
}

var (
	parens   = regexp.MustCompile(`\(([^()]*)\)`)
	shotHead = regexp.MustCompile(`^([A-Z' .]+?)(\s+[-–—:]\s+)(.+)$`)
)

// cue translates a character cue's extensions, "MARIE (VOIX OFF)" →
// "MARIE (V.O.)". The name is kept, unless it is a description starting
// with an article ("LE POLICIER" → "THE POLICEMAN").
func (s *session) cue(line string) string {
	name, rest := line, ""
	if i := strings.IndexByte(line, '('); i >= 0 {
		name, rest = line[:i], line[i:]
	}
	core := strings.TrimRightFunc(name, unicode.IsSpace)
	gap := name[len(core):]
	if ws := words(core); len(ws) > 0 && determiners[strings.TrimSuffix(firstElision(ws[0]), "'")] && s.notEnglish(core) {
		core = s.mt(core, caps)
	}
	rest = parens.ReplaceAllStringFunc(rest, func(p string) string {
		inner := strings.TrimSpace(p[1 : len(p)-1])
		if en, ok := extensions[foldTight(inner)]; ok {
			return "(" + en + ")"
		}
		if s.french(inner) {
			return "(" + s.mt(inner, caps) + ")"
		}
		return p
	})
	return core + gap + rest
}

// parenthetical translates a wryly, "(un temps)" → "(beat)", "(à Marie)"
// → "(to Marie)", in lower case as is the custom.
func (s *session) parenthetical(line string) string {
	open, close := strings.HasPrefix(line, "("), strings.HasSuffix(line, ")")
	inner := line
	if open {
		inner = inner[1:]
	}
	if close {
		inner = inner[:len(inner)-1]
	}
	inner = strings.TrimSpace(inner)
	var out string
	if en, ok := parentheticals[foldLower(inner)]; ok {
		out = en
	} else if s.french(inner) {
		out = s.lowerFirst(s.mt(inner, plain))
	} else {
		return line
	}
	if open {
		out = "(" + out
	}
	if close {
		out += ")"
	}
	return out
}

// lowerFirst lower-cases the first letter unless it starts a name, an
// acronym or "I".
func (s *session) lowerFirst(text string) string {
	r := []rune(text)
	if len(r) < 2 || !unicode.IsUpper(r[0]) || !unicode.IsLower(r[1]) {
		return text
	}
	if ws := words(text); len(ws) > 0 && s.nameWords[ws[0]] {
		return text
	}
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// transition translates "FONDU ENCHAÎNÉ :" → "DISSOLVE TO:".
func (s *session) transition(line string) string {
	key := fold(strings.TrimRight(line, ":.   "))
	if en, ok := transitions[key]; ok {
		return en
	}
	if s.french(line) {
		return s.mt(line, caps)
	}
	return line
}

// shot translates a shot or insert: "GROS PLAN SUR LA LETTRE" → "CLOSE ON
// THE LETTER", "INSERT - UNE PHOTO" → "INSERT - A PHOTO".
func (s *session) shot(line string) string {
	fields := strings.Fields(line)
	for _, sh := range shots {
		n := len(strings.Fields(sh.french))
		if len(fields) < n || fold(strings.Join(fields[:n], " ")) != sh.french {
			continue
		}
		rest := afterWords(line, n)
		trimmed := strings.TrimLeft(rest, " \t-–—:")
		if strings.TrimSpace(trimmed) == "" {
			return sh.english
		}
		sep := " "
		if strings.ContainsAny(rest[:len(rest)-len(trimmed)], "-–—:") {
			sep = " - "
		}
		if s.notEnglish(trimmed) {
			trimmed = s.mt(trimmed, modeOf(trimmed))
		}
		return sh.english + sep + trimmed
	}
	// "INSERT - UNE PHOTO": an English shot with French text after it.
	if s.french(line) {
		if m := shotHead.FindStringSubmatch(line); m != nil && !s.french(m[1]) {
			return m[1] + " - " + s.mt(m[3], caps)
		}
		return s.mt(line, caps)
	}
	return line
}

// afterWords returns line after its first n words.
func afterWords(line string, n int) string {
	i := 0
	for range n {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		for i < len(line) && line[i] != ' ' {
			i++
		}
	}
	return line[i:]
}

func (s *session) centered(line string) string {
	if fold(strings.Trim(line, ". ")) == "FIN" {
		return "THE END"
	}
	return s.prose(line)
}

func (s *session) credit(line string) string {
	if en, ok := credits[foldLower(strings.TrimRight(line, ": "))]; ok {
		return en
	}
	return s.prose(line)
}

// isCaps reports whether text is written in capitals.
func isCaps(text string) bool {
	letters := 0
	for _, r := range text {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters > 1
}

// ------------------------------------------------------------ sentences

var abbreviations = set("m mm mme mmes mlle mlles dr pr me mgr st ste etc cf p vol ch av bd no mr mrs ms jr sr vs env")

// sentences splits text into sentences, each with the spaces that follow
// it. A sentence ends with . ! ? or … (and closing quotes or brackets)
// followed by a space and a capital, a digit or an opening quote or dash;
// "M. Dupont" and initials do not end one.
func sentences(text string) []string {
	r := []rune(text)
	var out []string
	start := 0
	for i := 0; i < len(r); i++ {
		if !strings.ContainsRune(".!?…", r[i]) {
			continue
		}
		j := closers(r, i+1)
		if j >= len(r) || !unicode.IsSpace(r[j]) {
			i = j - 1
			continue
		}
		k := j
		for k < len(r) && unicode.IsSpace(r[k]) {
			k++
		}
		if k == len(r) {
			break
		}
		next := r[k]
		if !unicode.IsUpper(next) && !unicode.IsDigit(next) && !strings.ContainsRune("«\"“—–-¿¡([", next) {
			i = j - 1
			continue
		}
		if r[i] == '.' && j == i+1 && abbreviated(r[:i]) {
			i = j - 1
			continue
		}
		out = append(out, string(r[start:k]))
		start, i = k, k-1
	}
	if start < len(r) {
		out = append(out, string(r[start:]))
	}
	return out
}

// closers skips what may close a sentence after its final punctuation:
// more punctuation, quotes, brackets, and a French closing guillemet after
// its space ("Non. »").
func closers(r []rune, j int) int {
	for j < len(r) {
		switch {
		case strings.ContainsRune(".!?…»\"')]’”", r[j]):
			j++
		case unicode.IsSpace(r[j]):
			k := j
			for k < len(r) && unicode.IsSpace(r[k]) && r[k] != '\n' {
				k++
			}
			if k < len(r) && r[k] == '»' {
				j = k + 1
				continue
			}
			return j
		default:
			return j
		}
	}
	return j
}

// abbreviated reports whether text ends with an abbreviation or an
// initial (the word before a full stop).
func abbreviated(text []rune) bool {
	i := len(text)
	for i > 0 && unicode.IsLetter(text[i-1]) {
		i--
	}
	w := text[i:]
	if len(w) == 0 {
		return false
	}
	return (len(w) == 1 && unicode.IsUpper(w[0])) || abbreviations[strings.ToLower(string(w))]
}

// ---------------------------------------------------------- translation

// mode says how a text is put to the model.
type mode byte

const (
	plain mode = iota
	// caps: text in capitals, translated in lower case (the model reads
	// capitals badly) and put back in capitals.
	caps
	// location: a slugline's location, in capitals. It is given an
	// article, which the model needs to read it as a noun phrase
	// ("CUISINE DE MARIE" alone gives "MARIE CUISINE", "La cuisine de
	// Marie" gives "Marie's kitchen"), removed from the translation.
	location
)

func modeOf(text string) mode {
	if isCaps(text) {
		return caps
	}
	return plain
}

// mt returns the translation of a French sentence (during the first pass:
// records it and returns it as is).
func (s *session) mt(text string, m mode) string {
	key := string(rune('0'+m)) + "\x00" + text
	if s.collect {
		if !s.seen[key] {
			s.seen[key] = true
			s.wanted = append(s.wanted, key)
		}
		return text
	}
	if out, ok := s.done[key]; ok && out != "" {
		return out
	}
	return text
}

func (s *session) translateAll(ctx context.Context, tr Translator, workers int, progress func(done, total int)) (map[string]string, error) {
	done := make(map[string]string, len(s.wanted))
	if len(s.wanted) == 0 {
		return done, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	jobs := make(chan string)
	for range min(workers, len(s.wanted)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				out, err := s.translateOne(ctx, tr, key)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				done[key] = out
				n := len(done)
				mu.Unlock()
				if err == nil && progress != nil {
					progress(n, len(s.wanted))
				}
			}
		}()
	}
feed:
	for _, key := range s.wanted {
		select {
		case jobs <- key:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return done, nil
}

// placeholders stand for the characters' names while a sentence is
// translated: invented names the model copies as they are (checked with
// OPUS-MT fr-en), so it cannot turn "Marie" into "Mary" or "Pierre" into
// "stone".
var placeholders = []string{
	"Zorbin", "Dravok", "Yelmir", "Brontiq", "Quorvel", "Velkor", "Strivan", "Xavek",
	"Kwilam", "Tazmik", "Moxtel", "Gorlex", "Pilvam", "Wexol", "Jandrix",
}

func (s *session) translateOne(ctx context.Context, tr Translator, key string) (string, error) {
	m, text := mode(key[0]-'0'), key[2:]
	upper := m != plain
	src, names := s.protect(text, upper)
	article := m == location && !startsWithDeterminer(src)
	if article {
		src = articleFor(src) + src
	}
	if upper {
		src = capitalize(src)
	}
	out, err := tr.Translate(ctx, src)
	if err != nil {
		return "", err
	}
	if article {
		out = leadingArticle.ReplaceAllString(out, "")
	}
	for i, name := range names {
		out = placeholderRes[i].ReplaceAllLiteralString(out, name)
	}
	return polish(text, out, upper), nil
}

var (
	leadingArticle = regexp.MustCompile(`(?i)^\s*(the|an?)\s+`)
	placeholderRes = func() []*regexp.Regexp {
		out := make([]*regexp.Regexp, len(placeholders))
		for i, p := range placeholders {
			out[i] = regexp.MustCompile(`(?i)\b` + p + `\b`)
		}
		return out
	}()
)

func startsWithDeterminer(text string) bool {
	ws := words(text)
	return len(ws) > 0 && (determiners[strings.TrimSuffix(firstElision(ws[0]), "'")] || ws[0] == "du" || ws[0] == "au" || ws[0] == "aux")
}

// articleFor picks a French article for a noun phrase: its gender is
// unknown, but any article will do for the model.
func articleFor(text string) string {
	first := strings.ToLower(fold(strings.Fields(text)[0]))
	switch {
	case strings.ContainsAny(first[:1], "aeiouyh"):
		return "l'"
	case strings.HasSuffix(first, "s") || strings.HasSuffix(first, "x"):
		return "les "
	}
	return "le "
}

// capitalize upper-cases the first letter: a sentence starts with a
// capital, or the model takes it for a fragment.
func capitalize(text string) string {
	r := []rune(text)
	for i, c := range r {
		if unicode.IsLetter(c) {
			r[i] = unicode.ToUpper(c)
			break
		}
	}
	return string(r)
}

// protect replaces the characters' names written with a capital by
// placeholders and, if lowerCase, lower-cases the rest. It returns the
// names in placeholder order.
func (s *session) protect(text string, lowerCase bool) (string, []string) {
	r := []rune(text)
	lower := make([]rune, len(r))
	for i, c := range r {
		lower[i] = unicode.ToLower(c)
	}
	isWord := func(i int) bool { return i >= 0 && i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i])) }
	var b strings.Builder
	var names []string
	index := map[string]int{}
	for i := 0; i < len(r); {
		matched := false
		if !isWord(i - 1) {
			for _, name := range s.names {
				end := i + len(name)
				if end > len(r) || isWord(end) || string(lower[i:end]) != string(name) || !hasUpper(r[i:end]) {
					continue
				}
				key := string(name)
				n, ok := index[key]
				if !ok {
					if len(names) == len(placeholders) {
						break
					}
					n = len(names)
					index[key] = n
					names = append(names, string(r[i:end]))
				}
				b.WriteString(placeholders[n])
				i, matched = end, true
				break
			}
		}
		if matched {
			continue
		}
		if lowerCase {
			b.WriteRune(lower[i])
		} else {
			b.WriteRune(r[i])
		}
		i++
	}
	return b.String(), names
}

func hasUpper(r []rune) bool {
	for _, c := range r {
		if unicode.IsUpper(c) {
			return true
		}
	}
	return false
}

var (
	openQuote  = regexp.MustCompile(`«[\s\x{a0}\x{202f}]*`)
	closeQuote = regexp.MustCompile(`[\s\x{a0}\x{202f}]*»`)
	spaceMark  = regexp.MustCompile(`[\s\x{a0}\x{202f}]+([!?;:])`)
)

// polish applies English typography to a translation: no space before
// ! ? ; :, straight quotes instead of guillemets, no full stop the source
// did not have (a location, a wryly), capitals if the source had them.
func polish(src, out string, caps bool) string {
	out = openQuote.ReplaceAllString(out, `"`)
	out = closeQuote.ReplaceAllString(out, `"`)
	out = spaceMark.ReplaceAllString(out, "$1")
	out = strings.TrimSpace(out)
	if !strings.ContainsAny(lastRune(strings.TrimSpace(src)), ".!?…") &&
		strings.HasSuffix(out, ".") && !strings.HasSuffix(out, "..") {
		out = strings.TrimSuffix(out, ".")
	}
	if caps {
		out = screenplay.UpperRunes(out)
	}
	return out
}

func lastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[len(r)-1])
}
