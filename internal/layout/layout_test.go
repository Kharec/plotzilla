package layout

import (
	"slices"
	"strings"
	"testing"

	"plotzilla/internal/screenplay"
)

func TestWrap(t *testing.T) {
	r := []rune("the quick brown fox jumps")
	got := Wrap(r, 10, 10)
	var lines []string
	for _, rg := range got {
		lines = append(lines, string(r[rg[0]:rg[1]]))
	}
	want := []string{"the quick", "brown fox", "jumps"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", lines)
	}
}

func el(t screenplay.ElementType, s string) screenplay.Element {
	return screenplay.NewElement(t, s)
}

func TestSceneHeadingNotOrphaned(t *testing.T) {
	doc := &screenplay.Document{}
	// 51 one-line actions (each with a blank line before) fill most of page 1.
	for i := 0; i < 26; i++ {
		doc.Elements = append(doc.Elements, el(screenplay.Action, "Something happens."))
	}
	doc.Elements = append(doc.Elements, el(screenplay.Scene, "INT. HOUSE - DAY"), el(screenplay.Action, "A\nB\nC"))
	res := Paginate(doc, Options{})
	if len(res.Pages) != 2 {
		t.Fatalf("pages = %d", len(res.Pages))
	}
	if b := res.Breaks[0]; b.Element != 26 || b.Offset != 0 {
		t.Errorf("scene heading should start page 2, break = %+v", b)
	}
}

func TestDialogueMoreContd(t *testing.T) {
	doc := &screenplay.Document{}
	for i := 0; i < 24; i++ {
		doc.Elements = append(doc.Elements, el(screenplay.Action, "Something happens."))
	}
	long := strings.Repeat("This is a sentence that goes on. ", 12)
	doc.Elements = append(doc.Elements, el(screenplay.Character, "Margot"), el(screenplay.Dialogue, long))
	res := Paginate(doc, Options{})
	if len(res.Pages) != 2 {
		t.Fatalf("pages = %d", len(res.Pages))
	}
	text := func(p Page, i int) string {
		var b strings.Builder
		for _, s := range p.Lines[i].Segments {
			b.WriteString(s.Text)
		}
		return b.String()
	}
	p1, p2 := res.Pages[0], res.Pages[1]
	if got := text(p1, len(p1.Lines)-1); got != "(MORE)" {
		t.Errorf("last line of page 1 = %q", got)
	}
	if got := text(p2, 0); got != "MARGOT (CONT'D)" {
		t.Errorf("first line of page 2 = %q", got)
	}
	if res.Breaks[0].Element != 25 || res.Breaks[0].Offset == 0 {
		t.Errorf("break should be inside the dialogue: %+v", res.Breaks[0])
	}
}

func TestDualDialogue(t *testing.T) {
	right := el(screenplay.Character, "Bob")
	right.Dual = true
	doc := &screenplay.Document{Elements: []screenplay.Element{
		el(screenplay.Character, "Alice"), el(screenplay.Dialogue, "Hello there, how are you doing today?"),
		right, el(screenplay.Dialogue, "Fine."),
		el(screenplay.Action, "They stop."),
	}}
	lines := Paginate(doc, Options{}).Pages[0].Lines
	byRow := map[int][]float64{}
	for _, l := range lines {
		byRow[l.Row] = append(byRow[l.Row], l.X)
	}
	if len(byRow[0]) != 2 || byRow[0][0] != 2.5 || byRow[0][1] != 5.5 {
		t.Errorf("cues should share row 0 at 2.5in and 5.5in: %v", byRow[0])
	}
	// Left dialogue wraps to 2 lines at 27 chars; action follows after a blank line.
	if last := lines[len(lines)-1]; last.Row != 4 || last.X != 1.5 {
		t.Errorf("action line = %+v", last)
	}
}

func TestShotKeptWithNext(t *testing.T) {
	doc := &screenplay.Document{}
	for i := 0; i < 26; i++ {
		doc.Elements = append(doc.Elements, el(screenplay.Action, "Something happens."))
	}
	doc.Elements = append(doc.Elements, el(screenplay.Shot, "insert - the letter"), el(screenplay.Action, "A\nB\nC"))
	res := Paginate(doc, Options{})
	if b := res.Breaks[0]; b.Element != 26 || b.Offset != 0 {
		t.Errorf("insert should move to page 2 with its action, break = %+v", b)
	}
	if len(res.Scenes) != 0 {
		t.Errorf("an insert is not a scene: %+v", res.Scenes)
	}
	line := res.Pages[1].Lines[0]
	if line.X != 1.5 || line.Segments[0].Text != "INSERT - THE LETTER" {
		t.Errorf("insert line = %+v", line)
	}
}

func TestIntertitleCentered(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{el(screenplay.Intertitle, "Paris, 1942")}}
	line := Paginate(doc, Options{}).Pages[0].Lines[0]
	if line.Segments[0].Text != "PARIS, 1942" || line.X != 1.5+(60-11)*CharWidth/2 {
		t.Errorf("intertitle line = %+v", line)
	}
}

func lineText(l Line) string {
	var b strings.Builder
	for _, s := range l.Segments {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestNotPrinted(t *testing.T) {
	section := el(screenplay.Section, "Act One")
	section.Level = 1
	action := screenplay.Element{Type: screenplay.Action, Runs: []screenplay.Run{
		{Text: "He waits."}, {Text: " (fix this)", Note: true}, {Text: "\n"}, {Text: "a whole line of notes", Note: true}, {Text: "\nThen he goes."},
	}}
	doc := &screenplay.Document{Elements: []screenplay.Element{
		section, el(screenplay.Synopsis, "Where it starts."), el(screenplay.Scene, "INT. HOUSE - DAY"), action,
		{Type: screenplay.Action, Runs: []screenplay.Run{{Text: "only a note", Note: true}}},
		el(screenplay.Scene, "EXT. ROAD - NIGHT"),
	}}
	res := Paginate(doc, Options{})
	var got []string
	for _, l := range res.Pages[0].Lines {
		got = append(got, lineText(l))
	}
	want := []string{"INT. HOUSE - DAY", "He waits.", "Then he goes.", "EXT. ROAD - NIGHT"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("printed %q", got)
	}
	if res.Pages[0].Lines[3].Row != 6 {
		t.Errorf("the note-only action should leave no gap: %+v", res.Pages[0].Lines)
	}
	if res.Scenes[0].Number != "1" || res.Scenes[1].Number != "2" {
		t.Errorf("scenes: %+v", res.Scenes)
	}
}

func TestBreakOffsetSkipsNotes(t *testing.T) {
	doc := &screenplay.Document{}
	for i := 0; i < 26; i++ {
		doc.Elements = append(doc.Elements, el(screenplay.Action, "Line."))
	}
	long := screenplay.Element{Type: screenplay.Action, Runs: []screenplay.Run{
		{Text: "N", Note: true}, {Text: strings.Repeat("One sentence here. ", 20)},
	}}
	doc.Elements = append(doc.Elements, long)
	res := Paginate(doc, Options{})
	b := res.Breaks[0]
	text := []rune(doc.Elements[26].Text())
	if b.Element != 26 || b.Offset != 1 || text[b.Offset] != 'O' {
		t.Errorf("the page should start after the note: %+v", b)
	}
}

func TestA4(t *testing.T) {
	doc := &screenplay.Document{Settings: screenplay.Settings{Paper: screenplay.A4}}
	for i := 0; i < 120; i++ {
		doc.Elements = append(doc.Elements, el(screenplay.Action, "Line."))
	}
	// 29 actions (57 rows) fit on an A4 page, 27 (53 rows) on US Letter.
	a4 := Paginate(doc, Options{})
	doc.Settings.Paper = screenplay.Letter
	letter := Paginate(doc, Options{})
	if a4.Breaks[0].Element != 29 || letter.Breaks[0].Element != 27 {
		t.Errorf("first break: A4 %+v, Letter %+v", a4.Breaks[0], letter.Breaks[0])
	}
	if g := PaperGeometry(screenplay.A4); g.Width != 8.27 || g.Lines != 58 {
		t.Errorf("A4 geometry %+v", g)
	}
}

func labels(res *Result) []string {
	var out []string
	for _, p := range res.Pages {
		out = append(out, p.Label)
	}
	return out
}

func TestLockedPages(t *testing.T) {
	doc := &screenplay.Document{}
	for i := 0; i < 81; i++ {
		doc.Elements = append(doc.Elements, el(screenplay.Action, "Line."))
	}
	// Lock the current pagination: 27 actions per page.
	for _, b := range Paginate(doc, Options{}).Breaks {
		doc.Elements[b.Element].Locked = []screenplay.PageLock{{Offset: b.Offset, Page: b.Label}}
	}
	doc.Settings.Locked = true
	if got := labels(Paginate(doc, Options{})); strings.Join(got, " ") != "1 2 3" {
		t.Fatalf("locked, unchanged: %v", got)
	}

	// Material added to page 1 goes to page 1A; page 2 does not move.
	grown := *doc
	grown.Elements = append(append(append([]screenplay.Element{}, doc.Elements[:5]...),
		el(screenplay.Action, "New."), el(screenplay.Action, "New.")), doc.Elements[5:]...)
	res := Paginate(&grown, Options{})
	if got := labels(res); strings.Join(got, " ") != "1 1A 2 3" {
		t.Fatalf("added material: %v", got)
	}
	if b := res.Breaks[1]; b.Element != 29 || b.Label != "2" {
		t.Errorf("page 2 should still start with its first line: %+v", b)
	}

	// Page 2 emptied: merged into page 1.
	shrunk := *doc
	shrunk.Elements = append([]screenplay.Element{}, doc.Elements...)
	for i := 27; i < 54; i++ {
		shrunk.Elements[i] = screenplay.Element{Type: screenplay.Action, Locked: doc.Elements[i].Locked}
	}
	res = Paginate(&shrunk, Options{})
	if got := labels(res); strings.Join(got, " ") != "1-2 3" {
		t.Errorf("removed page: %v", got)
	}
	if b := res.Breaks[0]; b.Label != "3" || b.Page != 2 {
		t.Errorf("break after the merged page: %+v", b)
	}

	// Page 3 emptied: page 2 becomes 2-3, and its break says so.
	shrunk.Elements = append([]screenplay.Element{}, doc.Elements...)
	for i := 54; i < 81; i++ {
		shrunk.Elements[i] = screenplay.Element{Type: screenplay.Action, Locked: doc.Elements[i].Locked}
	}
	res = Paginate(&shrunk, Options{})
	if got := labels(res); strings.Join(got, " ") != "1 2-3" || res.Breaks[0].Label != "2-3" {
		t.Errorf("removed last page: %v %+v", got, res.Breaks)
	}
}

func TestLockedPageInsideDialogue(t *testing.T) {
	doc := &screenplay.Document{Settings: screenplay.Settings{Locked: true}}
	doc.Elements = append(doc.Elements, el(screenplay.Character, "Margot"),
		screenplay.Element{Type: screenplay.Dialogue, Runs: []screenplay.Run{{Text: "First part. Second part."}},
			Locked: []screenplay.PageLock{{Offset: 12, Page: "2"}}})
	res := Paginate(doc, Options{})
	if len(res.Pages) != 2 {
		t.Fatalf("pages = %d", len(res.Pages))
	}
	p1, p2 := res.Pages[0].Lines, res.Pages[1].Lines
	if lineText(p1[1]) != "First part." || lineText(p1[2]) != "(MORE)" {
		t.Errorf("page 1: %+v", p1)
	}
	if lineText(p2[0]) != "MARGOT (CONT'D)" || lineText(p2[1]) != "Second part." {
		t.Errorf("page 2: %+v", p2)
	}
	if b := res.Breaks[0]; b.Element != 1 || b.Offset != 12 || b.Label != "2" {
		t.Errorf("break %+v", b)
	}
}

func TestRevisionLevels(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{
		{Type: screenplay.Action, Runs: []screenplay.Run{{Text: "Old text. "}, {Text: "New text.", Rev: 2}}},
		{Type: screenplay.Action, Runs: []screenplay.Run{{Text: "Blue text.", Rev: 1}}},
	}}
	page := Paginate(doc, Options{}).Pages[0]
	if page.Lines[0].Rev != 2 || page.Lines[1].Rev != 1 || page.Rev() != 2 {
		t.Errorf("revisions: %+v", page.Lines)
	}
}

func TestForcedPageBreak(t *testing.T) {
	scene := el(screenplay.Scene, "EXT. ROAD - NIGHT")
	scene.NewPage = true
	right := el(screenplay.Character, "Bob")
	right.Dual, right.NewPage = true, true
	cue := el(screenplay.Character, "Alice")
	doc := &screenplay.Document{Elements: []screenplay.Element{
		el(screenplay.Action, "He waits."), scene, el(screenplay.Action, "A car."),
		cue, el(screenplay.Dialogue, "Hi."), right, el(screenplay.Dialogue, "Hey."),
		{Type: screenplay.Section, Level: 1, Runs: []screenplay.Run{{Text: "Act Two"}}, NewPage: true},
		el(screenplay.Action, "Later."),
	}}
	doc.Elements[0].NewPage = true // at the top of page 1: no effect
	res := Paginate(doc, Options{})
	var starts []int
	for _, b := range res.Breaks {
		starts = append(starts, b.Element)
	}
	// The right column's page break starts the page above the dual dialogue;
	// the section's goes to the next printed element.
	if len(res.Pages) != 4 || !slices.Equal(starts, []int{1, 3, 8}) {
		t.Errorf("pages %d, breaks at %v", len(res.Pages), starts)
	}
}
