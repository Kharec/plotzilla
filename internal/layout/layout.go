// Package layout turns a Document into printed pages following the
// Hollywood screenplay standard: US Letter (or A4), Courier 12pt (10
// characters per inch, 6 lines per inch), 1.5" left margin, ~55 lines per
// page so that one page reads as roughly one minute of screen time.
//
// It applies the usual pagination rules: a scene heading is never the last
// thing on a page, action and dialogue only break at the end of a sentence
// with at least two lines on each side, a page never ends on a
// parenthetical, and split dialogue gets "(MORE)" / "NAME (CONT'D)".
// Sections, synopses and notes are not printed. An element marked NewPage
// (Fountain "===") starts a page.
//
// Once pages are locked, every locked page starts where it did when it was
// locked: material added to a page flows onto A pages (12A, 12B), and a
// page whose material was all removed is merged with the previous one
// (12-13).
package layout

import (
	"strconv"
	"strings"
	"unicode"

	"plotzilla/internal/screenplay"
)

const (
	TopMargin  = 1.0 // inches
	CharWidth  = 0.1 // Courier 12pt
	LineHeight = 1.0 / 6
	RightEdge  = 7.5
)

// Geometry is what changes with the paper size. The text keeps the same
// margins and widths on A4; the page is longer, so it holds more lines.
type Geometry struct {
	Width, Height float64 // inches
	Lines         int     // body lines per page, between 1" margins
	// SceneNumber is where the right-hand scene number starts, and
	// RevisionMark where the revision asterisk goes.
	SceneNumber  float64
	RevisionMark float64
}

// PaperGeometry returns the geometry of a paper size.
func PaperGeometry(paper screenplay.Paper) Geometry {
	if paper == screenplay.A4 {
		return Geometry{Width: 8.27, Height: 11.69, Lines: 58, SceneNumber: 7.6, RevisionMark: 7.95}
	}
	return Geometry{Width: 8.5, Height: 11, Lines: 54, SceneNumber: 7.75, RevisionMark: 8.1}
}

type spec struct {
	left   float64 // inches from the left edge of the page
	width  int     // characters per line
	before int     // blank lines before the element
}

var specs = map[screenplay.ElementType]spec{
	screenplay.Scene:         {1.5, 60, 2},
	screenplay.Action:        {1.5, 60, 1},
	screenplay.Character:     {3.7, 38, 1},
	screenplay.Parenthetical: {3.1, 25, 0},
	screenplay.Dialogue:      {2.5, 35, 0},
	screenplay.Transition:    {1.5, 60, 1},
	screenplay.Centered:      {1.5, 60, 1},
	screenplay.Shot:          {1.5, 60, 1},
	screenplay.Intertitle:    {1.5, 60, 1},
}

type Segment struct {
	Text      string
	Bold      bool
	Italic    bool
	Underline bool
}

type Line struct {
	Row         int     // 0-based line within the page body
	X           float64 // inches from the left edge of the page
	Segments    []Segment
	SceneNumber string // printed in both margins when set
	Rev         int    // latest revision set of the text on the line
}

type Page struct {
	Number int    // 1, 2, 3… in print order (0 for a title page)
	Label  string // the printed page number: "12", "12A", "12-13"
	Lines  []Line
}

// Rev is the latest revision set of the text on the page: the page is
// issued on that colour.
func (p Page) Rev() int {
	rev := 0
	for _, l := range p.Lines {
		rev = max(rev, l.Rev)
	}
	return rev
}

// Break marks where a page starts in the source: the element index and the
// rune offset inside that element's text.
type Break struct {
	Element int    `json:"element"`
	Offset  int    `json:"offset"`
	Page    int    `json:"page"`
	Label   string `json:"label"`
}

type ScenePage struct {
	Element int    `json:"element"`
	Number  string `json:"number"`
	Page    int    `json:"page"`
	Label   string `json:"label"`
}

type Result struct {
	Pages  []Page
	Breaks []Break
	Scenes []ScenePage
}

type Options struct {
	SceneNumbers bool
}

// dualSpecs place the two columns of a dual dialogue side by side:
// left column from 1.5", right column from 4.5".
var dualSpecs = [2]map[screenplay.ElementType]spec{
	{
		screenplay.Character:     {2.5, 20, 1},
		screenplay.Parenthetical: {1.8, 22, 0},
		screenplay.Dialogue:      {1.5, 27, 0},
	},
	{
		screenplay.Character:     {5.5, 20, 1},
		screenplay.Parenthetical: {4.8, 22, 0},
		screenplay.Dialogue:      {4.5, 28, 0},
	},
}

type style uint8

const (
	sBold style = 1 << iota
	sItalic
	sUnderline
)

type item struct {
	elem        int
	typ         screenplay.ElementType
	runes       []rune
	styles      []style
	revs        []int // revision set of each rune
	src         []int // offset of each rune in the element's text
	ranges      [][2]int
	before      int
	sceneNumber string
	dual        bool
	sp          spec
	pages       []string // locked pages starting with this item
	newPage     bool     // forced page break before the item
}

// newItem prepares the printed text of an element: notes are removed, and
// so are the lines that held nothing but a note.
func newItem(idx int, el screenplay.Element) *item {
	it := &item{elem: idx, typ: el.Type, dual: el.Dual, newPage: el.NewPage}
	type char struct {
		r    rune
		st   style
		rev  int
		note bool
	}
	var all []char
	for _, r := range el.Runs {
		var st style
		if r.Bold {
			st |= sBold
		}
		if r.Italic {
			st |= sItalic
		}
		if r.Underline {
			st |= sUnderline
		}
		for _, c := range r.Text {
			all = append(all, char{c, st, r.Rev, r.Note})
		}
	}
	started := false
	for start, i := 0, 0; i <= len(all); i++ {
		if i < len(all) && all[i].r != '\n' {
			continue
		}
		line := all[start:i]
		note, text := false, false
		for _, c := range line {
			note = note || c.note
			text = text || (!c.note && !unicode.IsSpace(c.r))
		}
		if !note || text {
			if started {
				it.add('\n', 0, 0, start-1)
			}
			started = true
			for k, c := range line {
				if !c.note {
					it.add(c.r, c.st, c.rev, start+k)
				}
			}
		}
		start = i + 1
	}
	it.relayout(specs[el.Type])
	return it
}

func (it *item) add(c rune, st style, rev, src int) {
	if c == '\t' {
		c = ' '
	}
	if it.typ.Uppercase() {
		c = unicode.ToUpper(c)
	}
	it.runes = append(it.runes, c)
	it.styles = append(it.styles, st)
	it.revs = append(it.revs, rev)
	it.src = append(it.src, src)
}

func (it *item) blank() bool { return strings.TrimSpace(string(it.runes)) == "" }

// offset converts a printed rune index into an offset in the element text.
func (it *item) offset(i int) int {
	switch {
	case i < len(it.src):
		return it.src[i]
	case len(it.src) > 0:
		return it.src[len(it.src)-1] + 1
	}
	return 0
}

// index is the first printed rune at or after an element text offset.
func (it *item) index(offset int) int {
	i := 0
	for i < len(it.src) && it.src[i] < offset {
		i++
	}
	return i
}

// cut splits the item before printed rune k, for a locked page starting
// there. Spaces and line breaks around the cut go.
func (it *item) cut(k int) (*item, *item) {
	tail := *it
	head := *it
	end := k
	for end > 0 && unicode.IsSpace(it.runes[end-1]) {
		end--
	}
	for k < len(it.runes) && unicode.IsSpace(it.runes[k]) {
		k++
	}
	head.runes, head.styles, head.revs, head.src = it.runes[:end], it.styles[:end], it.revs[:end], it.src[:end]
	tail.runes, tail.styles, tail.revs, tail.src = it.runes[k:], it.styles[k:], it.revs[k:], it.src[k:]
	tail.pages = nil
	tail.newPage = false
	head.relayout(it.sp)
	tail.relayout(it.sp)
	return &head, &tail
}

// splittable reports whether a locked page may start inside the element.
func (it *item) splittable() bool {
	switch it.typ {
	case screenplay.Action, screenplay.Centered, screenplay.Dialogue, screenplay.Parenthetical:
		return true
	}
	return false
}

func (it *item) relayout(sp spec) {
	it.sp = sp
	it.before = sp.before
	cont := sp.width
	if it.typ == screenplay.Parenthetical {
		cont-- // hanging indent under the opening parenthesis
	}
	it.ranges = Wrap(it.runes, sp.width, cont)
}

func (it *item) x(li int) float64 {
	sp := it.sp
	n := float64(it.ranges[li][1] - it.ranges[li][0])
	switch it.typ {
	case screenplay.Transition:
		return RightEdge - n*CharWidth
	case screenplay.Centered, screenplay.Intertitle:
		return sp.left + (float64(sp.width)-n)*CharWidth/2
	case screenplay.Parenthetical:
		if li > 0 {
			return sp.left + CharWidth
		}
	}
	return sp.left
}

func (it *item) rev(li int) int {
	rev := 0
	for _, r := range it.revs[it.ranges[li][0]:it.ranges[li][1]] {
		rev = max(rev, r)
	}
	return rev
}

func (it *item) segments(li int) []Segment {
	var segs []Segment
	var cur style
	rg := it.ranges[li]
	for i := rg[0]; i < rg[1]; i++ {
		st := it.styles[i]
		if len(segs) == 0 || st != cur {
			segs = append(segs, Segment{Bold: st&sBold != 0, Italic: st&sItalic != 0, Underline: st&sUnderline != 0})
			cur = st
		}
		segs[len(segs)-1].Text += string(it.runes[i])
	}
	return segs
}

// Wrap splits text into lines of at most width characters (contWidth for the
// following lines), breaking on spaces and on "\n". It returns rune ranges.
func Wrap(r []rune, width, contWidth int) [][2]int {
	width, contWidth = max(width, 1), max(contWidth, 1)
	n := len(r)
	var out [][2]int
	start := 0
	for {
		w := width
		if len(out) > 0 {
			w = contWidth
		}
		end, lastSpace := start, -1
		for end < n && r[end] != '\n' && end-start < w {
			if r[end] == ' ' {
				lastSpace = end
			}
			end++
		}
		switch {
		case end >= n:
			return append(out, trimRange(r, start, n))
		case r[end] == '\n':
			out = append(out, trimRange(r, start, end))
			start = end + 1
		case r[end] == ' ':
			out = append(out, trimRange(r, start, end))
			if start = skipSpaces(r, end); start >= n {
				return out
			}
		case lastSpace > start:
			out = append(out, trimRange(r, start, lastSpace))
			start = skipSpaces(r, lastSpace)
		default: // a single word longer than the line
			out = append(out, [2]int{start, end})
			start = end
		}
	}
}

func trimRange(r []rune, s, e int) [2]int {
	for e > s && r[e-1] == ' ' {
		e--
	}
	return [2]int{s, e}
}

func skipSpaces(r []rune, i int) int {
	for i < len(r) && r[i] == ' ' {
		i++
	}
	return i
}

type unit struct {
	head  *item   // the element, or the character cue of a dialogue block
	parts []*item // parentheticals and dialogue following a cue
	block bool
	right *unit // second column of a dual dialogue
	// A dialogue block cut by a locked page: the first part ends with
	// (MORE), the next one starts with NAME (CONT'D).
	more, contd bool
	pages       []string // locked pages starting with this unit
	newPage     bool     // forced page break before the unit
}

func (u *unit) items() []*item {
	return append([]*item{u.head}, u.parts...)
}

func (u *unit) height() int {
	n := 0
	for _, it := range u.items() {
		n += len(it.ranges)
	}
	return n
}

// minLines is how much of the unit must fit under a scene heading.
func (u *unit) minLines() int {
	if !u.block {
		return min(2, len(u.head.ranges))
	}
	n := 0
	for _, p := range u.parts {
		n += len(p.ranges)
	}
	return len(u.head.ranges) + min(2, n)
}

// Paginate lays the document out on pages.
func Paginate(doc *screenplay.Document, opt Options) *Result {
	locked := doc.Settings.Locked
	var items []*item
	var carried []string // locked pages of elements that print nothing
	carriedPage := false // and their forced page break
	scene := 0
	for i, el := range doc.Elements {
		var locks []screenplay.PageLock
		if locked {
			for _, l := range carried {
				locks = append(locks, screenplay.PageLock{Page: l})
			}
			locks = append(locks, el.Locked...)
		}
		var it *item
		if !el.IsEmpty() && el.Type.Printed() {
			it = newItem(i, el)
		}
		if it == nil || it.blank() {
			carried = nil // now in locks
			for _, l := range locks {
				carried = append(carried, l.Page)
			}
			carriedPage = carriedPage || el.NewPage
			continue
		}
		carried = nil
		it.newPage = it.newPage || carriedPage
		carriedPage = false
		if el.Type == screenplay.Scene {
			scene++
			it.sceneNumber = el.SceneNumber
			if it.sceneNumber == "" {
				it.sceneNumber = strconv.Itoa(scene)
			}
		}
		items = append(items, lockItem(it, locks, &carried)...)
	}

	var units []unit
	for i := 0; i < len(items); i++ {
		it := items[i]
		if it.typ == screenplay.Character {
			u := unit{head: it, block: true, pages: it.pages, newPage: it.newPage}
			for i+1 < len(items) && items[i+1].typ.InDialogue() {
				i++
				part := items[i]
				if len(part.pages) > 0 && !it.dual && !nextIsDual(items, i) {
					u.more = true
					units = append(units, u)
					u = unit{head: it, block: true, contd: true, pages: part.pages}
				} else {
					u.pages = append(u.pages, part.pages...)
				}
				// A page cannot be forced inside a dialogue block: it breaks
				// before the block.
				u.newPage = u.newPage || part.newPage
				u.parts = append(u.parts, part)
			}
			if prev := len(units) - 1; it.dual && prev >= 0 && units[prev].block && units[prev].right == nil {
				for side, col := range []*unit{&units[prev], &u} {
					for _, ci := range col.items() {
						ci.relayout(dualSpecs[side][ci.typ])
					}
				}
				// A dual dialogue is never split: its locked pages start above it.
				units[prev].pages = append(units[prev].pages, u.pages...)
				units[prev].newPage = units[prev].newPage || u.newPage
				units[prev].right = &u
				continue
			}
			units = append(units, u)
			continue
		}
		if it.typ.InDialogue() {
			it.before = 1 // dialogue without a cue: keep it off the previous element
		}
		units = append(units, unit{head: it, pages: it.pages, newPage: it.newPage})
	}

	p := &paginator{res: &Result{}, opt: opt, geo: PaperGeometry(doc.Settings.Paper), locked: locked}
	p.newPage()
	for i, u := range units {
		var next *unit
		if i+1 < len(units) {
			next = &units[i+1]
			if (locked && len(next.pages) > 0) || next.newPage {
				next = nil // the next unit starts its own page anyway
			}
		}
		if locked {
			p.startLocked(u.pages)
		}
		if u.newPage && len(p.page.Lines) > 0 {
			p.newPage()
		}
		switch {
		case u.right != nil:
			p.placeDual(u)
		case u.block:
			p.placeBlock(u)
		default:
			p.placeItem(u.head, next)
		}
	}
	if locked {
		// Locked pages whose material is all gone, at the end.
		p.startLocked(carried)
		if len(p.res.Pages) > 1 && len(p.page.Lines) == 0 {
			p.dropEmptyPage()
		}
	}
	// A page merged with an emptied one after it was renamed ("6-7").
	for i, b := range p.res.Breaks {
		p.res.Breaks[i].Label = p.res.Pages[b.Page-1].Label
	}
	for i, s := range p.res.Scenes {
		p.res.Scenes[i].Label = p.res.Pages[s.Page-1].Label
	}
	return p.res
}

// nextIsDual reports whether the block holding items[i] is the left column
// of a dual dialogue.
func nextIsDual(items []*item, i int) bool {
	for j := i + 1; j < len(items); j++ {
		if !items[j].typ.InDialogue() {
			return items[j].typ == screenplay.Character && items[j].dual
		}
	}
	return false
}

// lockItem attaches the locked pages that start in an item, cutting it
// where a page starts inside it. A page starting at its very end starts
// with the next item.
func lockItem(it *item, locks []screenplay.PageLock, carried *[]string) []*item {
	out := []*item{it}
	cur := it
	base := 0 // printed runes of it before cur
	for _, l := range locks {
		k := it.index(l.Offset) - base
		switch {
		case k >= len(cur.runes) || strings.TrimSpace(string(cur.runes[k:])) == "":
			*carried = append(*carried, l.Page)
		case k <= 0 || !cur.splittable() || strings.TrimSpace(string(cur.runes[:k])) == "":
			cur.pages = append(cur.pages, l.Page)
		default:
			head, tail := cur.cut(k)
			base += len(cur.runes) - len(tail.runes)
			tail.pages = []string{l.Page}
			out[len(out)-1] = head
			out = append(out, tail)
			cur = tail
		}
	}
	return out
}

type paginator struct {
	res     *Result
	page    *Page
	row     int
	pending bool // the next emitted source line starts a new page
	opt     Options
	geo     Geometry
	locked  bool
}

// newPage starts the next page. Once pages are locked, a page that does
// not start a locked page is an A page of the previous one.
func (p *paginator) newPage() {
	label := strconv.Itoa(len(p.res.Pages) + 1)
	if p.locked && p.page != nil {
		label = nextLabel(p.page.Label)
	}
	p.addPage(label)
}

func (p *paginator) addPage(label string) {
	p.res.Pages = append(p.res.Pages, Page{Number: len(p.res.Pages) + 1, Label: label})
	p.page = &p.res.Pages[len(p.res.Pages)-1]
	p.row = 0
	p.pending = len(p.res.Pages) > 1
}

// startLocked starts the locked pages that begin with the next unit. A
// page left empty in between lost all its material: it is merged into the
// page before it ("12-13").
func (p *paginator) startLocked(labels []string) {
	for _, l := range labels {
		switch {
		case len(p.page.Lines) > 0:
			p.addPage(l)
		case len(p.res.Pages) > 1:
			p.dropEmptyPage()
			p.addPage(l)
		default: // nothing before the first locked page
			p.page.Label = firstLabel(p.page.Label) + "-" + l
		}
	}
}

func (p *paginator) dropEmptyPage() {
	n := len(p.res.Pages)
	empty := p.res.Pages[n-1].Label
	p.res.Pages = p.res.Pages[:n-1]
	p.page = &p.res.Pages[n-2]
	p.page.Label = firstLabel(p.page.Label) + "-" + lastLabel(empty)
	p.row = p.geo.Lines
}

func firstLabel(l string) string { return strings.Split(l, "-")[0] }

func lastLabel(l string) string {
	parts := strings.Split(l, "-")
	return parts[len(parts)-1]
}

// nextLabel names the page added after a locked page: 12 → 12A → 12B.
func nextLabel(label string) string {
	l := lastLabel(label)
	if n := len(l); n > 0 && l[n-1] >= 'A' && l[n-1] < 'Z' && strings.ContainsAny(l, "0123456789") {
		return l[:n-1] + string(l[n-1]+1)
	}
	return l + "A"
}

func (p *paginator) emit(it *item, li int) {
	if p.pending {
		p.res.Breaks = append(p.res.Breaks, Break{Element: it.elem, Offset: it.offset(it.ranges[li][0]), Page: p.page.Number, Label: p.page.Label})
		p.pending = false
	}
	line := Line{Row: p.row, X: it.x(li), Segments: it.segments(li), Rev: it.rev(li)}
	if it.typ == screenplay.Scene && li == 0 {
		p.res.Scenes = append(p.res.Scenes, ScenePage{Element: it.elem, Number: it.sceneNumber, Page: p.page.Number, Label: p.page.Label})
		if p.opt.SceneNumbers {
			line.SceneNumber = it.sceneNumber
		}
	}
	p.page.Lines = append(p.page.Lines, line)
	p.row++
}

// emitText adds a line that does not come from the source ((MORE), CONT'D).
func (p *paginator) emitText(x float64, s string) {
	p.page.Lines = append(p.page.Lines, Line{Row: p.row, X: x, Segments: []Segment{{Text: s}}})
	p.row++
}

func (p *paginator) spaceBefore(it *item) int {
	if p.row == 0 {
		return 0
	}
	return it.before
}

func (p *paginator) placeItem(it *item, next *unit) {
	n := len(it.ranges)
	// Scene headings and inserts never end a page: keep them with what follows.
	if (it.typ == screenplay.Scene || it.typ == screenplay.Shot) && p.row > 0 && next != nil {
		need := it.before + n + next.head.before + next.minLines()
		if p.row+need > p.geo.Lines {
			p.newPage()
		}
	}
	splittable := it.typ == screenplay.Action || it.typ == screenplay.Centered
	start := 0
	for {
		before := p.spaceBefore(it)
		if p.row+before+n-start <= p.geo.Lines {
			p.row += before
			for li := start; li < n; li++ {
				p.emit(it, li)
			}
			return
		}
		avail := p.geo.Lines - p.row - before
		k := 0
		if splittable {
			k = sentenceSplit(it, start, avail)
		}
		if k == 0 && p.row == 0 {
			k = avail // longer than a page with no clean break
		}
		if k > 0 {
			p.row += before
			for li := start; li < start+k; li++ {
				p.emit(it, li)
			}
			start += k
		}
		p.newPage()
	}
}

type dline struct {
	it *item
	li int
}

func (p *paginator) placeBlock(u unit) {
	var flat []dline
	for _, part := range u.parts {
		for li := range part.ranges {
			flat = append(flat, dline{part, li})
		}
	}
	cue := u.head
	contd := u.contd
	more := 0
	if u.more {
		more = 1
	}
	pos := 0
	for {
		before := p.spaceBefore(cue)
		head := len(cue.ranges)
		if contd {
			head = 1
		}
		rem := len(flat) - pos
		if p.row+before+head+rem+more <= p.geo.Lines {
			p.row += before
			p.emitCue(cue, contd)
			for _, d := range flat[pos:] {
				p.emit(d.it, d.li)
			}
			if u.more {
				p.emitText(specs[screenplay.Character].left, "(MORE)")
			}
			return
		}
		avail := p.geo.Lines - p.row - before - head - 1 // one line for (MORE)
		k := dialogueSplit(flat, pos, avail)
		if k == 0 && p.row == 0 {
			k = min(max(avail, 1), rem-1)
		}
		if k > 0 {
			p.row += before
			p.emitCue(cue, contd)
			for _, d := range flat[pos : pos+k] {
				p.emit(d.it, d.li)
			}
			p.emitText(specs[screenplay.Character].left, "(MORE)")
			pos += k
			contd = true
		}
		p.newPage()
	}
}

// placeDual lays both columns side by side. A dual dialogue is never split.
func (p *paginator) placeDual(u unit) {
	h := max(u.height(), u.right.height())
	before := p.spaceBefore(u.head)
	if p.row > 0 && p.row+before+h > p.geo.Lines {
		p.newPage()
		before = 0
	}
	p.row += before
	base := p.row
	for _, col := range []*unit{&u, u.right} {
		p.row = base
		for _, it := range col.items() {
			for li := range it.ranges {
				p.emit(it, li)
			}
		}
	}
	p.row = base + h
}

func (p *paginator) emitCue(cue *item, contd bool) {
	if !contd {
		for li := range cue.ranges {
			p.emit(cue, li)
		}
		return
	}
	name := strings.TrimSpace(string(cue.runes))
	if !strings.Contains(name, "(CONT'D)") {
		name += " (CONT'D)"
	}
	p.emitText(specs[screenplay.Character].left, name)
}

// sentenceSplit returns how many lines (from start) to keep on the current
// page, or 0 if the element cannot be split cleanly within avail lines.
func sentenceSplit(it *item, start, avail int) int {
	rem := len(it.ranges) - start
	for k := min(avail, rem-2); k >= 2; k-- {
		if endsSentence(it.runes, it.ranges[start+k-1]) {
			return k
		}
	}
	return 0
}

func dialogueSplit(flat []dline, pos, avail int) int {
	rem := len(flat) - pos
	for k := min(avail, rem-2); k >= 2; k-- {
		last := flat[pos+k-1]
		if last.it.typ == screenplay.Parenthetical {
			continue
		}
		if last.li == len(last.it.ranges)-1 || endsSentence(last.it.runes, last.it.ranges[last.li]) {
			return k
		}
	}
	return 0
}

func endsSentence(r []rune, rg [2]int) bool {
	if rg[1] < len(r) && r[rg[1]] == '\n' {
		return true
	}
	s := strings.TrimRight(string(r[rg[0]:rg[1]]), " \"'”’»)")
	for _, end := range []string{".", "!", "?", "…", ":", "--"} {
		if strings.HasSuffix(s, end) {
			return true
		}
	}
	return false
}

// TitlePage lays out the cover page: title block centred in the upper half,
// contact bottom-left, draft date and notes bottom-right.
func TitlePage(tp screenplay.TitlePage, paper screenplay.Paper) Page {
	lastRow := PaperGeometry(paper).Lines
	var lines []Line
	centered := func(row int, s string) int {
		for _, para := range strings.Split(s, "\n") {
			r := []rune(para)
			for _, rg := range Wrap(r, 60, 60) {
				n := float64(rg[1] - rg[0])
				lines = append(lines, Line{Row: row, X: 1.5 + (60-n)*CharWidth/2, Segments: []Segment{{Text: string(r[rg[0]:rg[1]])}}})
				row++
			}
		}
		return row
	}
	bottom := func(s string, width int, right bool) {
		var texts []string
		for _, para := range strings.Split(strings.TrimSpace(s), "\n") {
			r := []rune(para)
			for _, rg := range Wrap(r, width, width) {
				texts = append(texts, string(r[rg[0]:rg[1]]))
			}
		}
		row := lastRow - len(texts)
		for _, t := range texts {
			x := 1.5
			if right {
				x = RightEdge - float64(len([]rune(t)))*CharWidth
			}
			lines = append(lines, Line{Row: row, X: x, Segments: []Segment{{Text: t}}})
			row++
		}
	}

	row := 18
	if tp.Title != "" {
		row = centered(row, tp.Title) + 3
	}
	if tp.Credit != "" {
		row = centered(row, tp.Credit) + 1
	}
	if tp.Author != "" {
		row = centered(row, tp.Author) + 3
	}
	if tp.Source != "" {
		centered(row, tp.Source)
	}
	if strings.TrimSpace(tp.Contact) != "" {
		bottom(tp.Contact, 35, false)
	}
	right := strings.TrimSpace(strings.TrimSpace(tp.Notes) + "\n" + strings.TrimSpace(tp.DraftDate))
	if right != "" {
		bottom(right, 25, true)
	}
	return Page{Lines: lines}
}
