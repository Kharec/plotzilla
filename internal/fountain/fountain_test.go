package fountain

import (
	"reflect"
	"strings"
	"testing"

	"plotzilla/internal/screenplay"
)

const sample = `Title: The Last Reel
Credit: Written by
Author: Jane Doe
Draft date: 1 Sept 2026

INT. PROJECTION BOOTH - NIGHT #1#

Dust hangs in the **beam** of the projector.

INSERT - THE TICKET STUB

> Paris, 1942 <

MARGOT (V.O.)
(quietly)
It always ends *here*.
Right here.

CUT TO:

.FLASHBACK

@McCLANE
Yippee.

HANS ^
Nein.

!SHOUTING IS ACTION
and continues.

> THE END <
`

func types(doc *screenplay.Document) []screenplay.ElementType {
	var out []screenplay.ElementType
	for _, e := range doc.Elements {
		out = append(out, e.Type)
	}
	return out
}

func TestParse(t *testing.T) {
	doc := Parse(sample)
	if doc.TitlePage.Title != "The Last Reel" || doc.TitlePage.Author != "Jane Doe" {
		t.Fatalf("title page: %+v", doc.TitlePage)
	}
	want := []screenplay.ElementType{
		screenplay.Scene, screenplay.Action, screenplay.Shot, screenplay.Centered,
		screenplay.Character, screenplay.Parenthetical, screenplay.Dialogue,
		screenplay.Transition, screenplay.Scene,
		screenplay.Character, screenplay.Dialogue,
		screenplay.Character, screenplay.Dialogue,
		screenplay.Action, screenplay.Intertitle,
	}
	if got := types(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("types:\n got %v\nwant %v", got, want)
	}
	if doc.Elements[0].SceneNumber != "1" || doc.Elements[0].Text() != "INT. PROJECTION BOOTH - NIGHT" {
		t.Errorf("scene: %+v", doc.Elements[0])
	}
	if r := doc.Elements[1].Runs; len(r) != 3 || !r[1].Bold || r[1].Text != "beam" {
		t.Errorf("bold runs: %+v", r)
	}
	if got := doc.Elements[2].Text(); got != "INSERT - THE TICKET STUB" {
		t.Errorf("shot: %q", got)
	}
	if got := doc.Elements[6].Text(); got != "It always ends here.\nRight here." {
		t.Errorf("dialogue: %q", got)
	}
	if got := doc.Elements[9].Text(); got != "McCLANE" {
		t.Errorf("forced character: %q", got)
	}
	if hans := doc.Elements[11]; hans.Text() != "HANS" || !hans.Dual || doc.Elements[9].Dual {
		t.Errorf("dual cue: %+v", hans)
	}
}

func TestInlineLiteralAsterisk(t *testing.T) {
	runs := parseInline("2 * 3 = 6")
	if len(runs) != 1 || runs[0].Text != "2 * 3 = 6" || runs[0].Italic {
		t.Errorf("got %+v", runs)
	}
}

func TestRoundTrip(t *testing.T) {
	first := Parse(sample)
	second := Parse(Write(first))
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("round trip changed the document:\n%s", Write(first))
	}
}

const outline = `# Act One

## The Booth

= Margot finds the reel.

INT. PROJECTION BOOTH - NIGHT

Dust hangs in the beam. [[Too dark? Try dawn.]]

[[Cut the cat
  
or keep it for the ending.]]

MARGOT [[she whispers]]
(quietly)
It always ends here.
`

func TestOutlineAndNotes(t *testing.T) {
	doc := Parse(outline)
	want := []screenplay.ElementType{
		screenplay.Section, screenplay.Section, screenplay.Synopsis, screenplay.Scene,
		screenplay.Action, screenplay.Action, screenplay.Character, screenplay.Parenthetical, screenplay.Dialogue,
	}
	if got := types(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("types:\n got %v\nwant %v", got, want)
	}
	els := doc.Elements
	if els[0].Level != 1 || els[0].Text() != "Act One" || els[1].Level != 2 || els[1].Text() != "The Booth" {
		t.Errorf("sections: %+v %+v", els[0], els[1])
	}
	if els[2].Text() != "Margot finds the reel." {
		t.Errorf("synopsis: %q", els[2].Text())
	}
	if r := els[4].Runs; len(r) != 2 || r[1].Text != "Too dark? Try dawn." || !r[1].Note || r[0].Note {
		t.Errorf("inline note: %+v", r)
	}
	if r := els[5].Runs; len(r) != 1 || !r[0].Note || r[0].Text != "Cut the cat\n\nor keep it for the ending." {
		t.Errorf("note paragraph: %+v", r)
	}
	if els[6].PrintedText() != "MARGOT " || !els[6].Runs[1].Note {
		t.Errorf("note after a cue: %+v", els[6].Runs)
	}
	if second := Parse(Write(doc)); !reflect.DeepEqual(doc, second) {
		t.Fatalf("round trip changed the document:\n%s", Write(doc))
	}
}

func TestRevisionsAndLocks(t *testing.T) {
	doc := &screenplay.Document{
		TitlePage: screenplay.TitlePage{Title: "Reel"},
		Settings: screenplay.Settings{
			Paper:     screenplay.A4,
			Revisions: []screenplay.Revision{{Name: "Blue", Color: "blue", Date: "09/28/26"}, {Name: "Pink", Color: "pink", Date: "*/"}},
			Revising:  true,
			Locked:    true,
		},
		Elements: []screenplay.Element{
			{Type: screenplay.Scene, Runs: []screenplay.Run{{Text: "INT. BOOTH - NIGHT"}}, Locked: []screenplay.PageLock{{Offset: 0, Page: "2"}}},
			{Type: screenplay.Action, Runs: []screenplay.Run{
				{Text: "Dust hangs in the "}, {Text: "golden", Rev: 2, Bold: true}, {Text: " beam.", Rev: 1},
				{Text: " She waits.", Rev: 1, Note: true}, {Text: " Then leaves."},
			}, Locked: []screenplay.PageLock{{Offset: 25, Page: "3"}, {Offset: 25, Page: "4"}}},
			{Type: screenplay.Character, Runs: []screenplay.Run{{Text: "MARGOT"}}},
			{Type: screenplay.Parenthetical, Runs: []screenplay.Run{{Text: "(quietly)", Rev: 1}}, Locked: []screenplay.PageLock{{Offset: 0, Page: "4A"}}},
			{Type: screenplay.Dialogue, Runs: []screenplay.Run{{Text: "Here."}}},
			{Type: screenplay.Shot, Runs: []screenplay.Run{{Text: "INSERT - ", Rev: 1}, {Text: "THE TICKET"}}},
		},
	}
	out := Write(doc)
	if !strings.Contains(out, "/*page 2*/INT. BOOTH") || !strings.Contains(out, "/*rev 2*/**golden**/*/rev*//*rev 1*/ /*page 3*//*page 4*/beam.") {
		t.Errorf("markers:\n%s", out)
	}
	if got := Parse(out); !reflect.DeepEqual(doc, got) {
		for i := range got.Elements {
			if !reflect.DeepEqual(got.Elements[i], doc.Elements[i]) {
				t.Errorf("element %d:\n got %#v\nwant %#v", i, got.Elements[i], doc.Elements[i])
			}
		}
		t.Fatalf("round trip changed the document:\n%s", out)
	}
}

func TestPlainScriptUnchanged(t *testing.T) {
	// No Plotzilla data in a script that uses none of it.
	if out := Write(Parse(sample)); strings.Contains(out, "/*") || strings.Contains(out, "[[") {
		t.Errorf("unexpected markers:\n%s", out)
	}
}

func TestPastedText(t *testing.T) {
	els := ParseText("# Act Two\r\n\r\nJOHN\r\nHi. [[louder]]\r\n")
	if len(els) != 3 || els[0].Type != screenplay.Section || els[2].PrintedText() != "Hi. " {
		t.Errorf("got %+v", els)
	}
}

func TestInvalidNoteStaysText(t *testing.T) {
	// An empty line ends a note: without the two spaces this is not one.
	doc := Parse("Wait [[for it\n\nnow]] here.\n")
	if len(doc.Elements) != 2 || doc.Elements[0].Text() != "Wait [[for it" || doc.Elements[0].Runs[0].Note {
		t.Errorf("got %+v", doc.Elements)
	}
}

func TestForcedPageBreak(t *testing.T) {
	doc := Parse("INT. HOUSE - DAY\n\nHe waits.\n\n===\n\nEXT. ROAD - NIGHT\n\nJOHN\nHi.\n\n===\nLater.\n")
	want := []bool{false, false, true, false, false, true}
	for i, el := range doc.Elements {
		if el.NewPage != want[i] {
			t.Errorf("element %d %q: NewPage = %v", i, el.Text(), el.NewPage)
		}
	}
	out := Write(doc)
	if !strings.Contains(out, "He waits.\n\n===\n\nEXT. ROAD") {
		t.Errorf("page break not written:\n%s", out)
	}
	if second := Parse(out); !reflect.DeepEqual(doc, second) {
		t.Fatalf("round trip changed the document:\n%s", out)
	}
	// The page break of an empty element moves to the next one written.
	doc.Elements[1] = screenplay.Element{Type: screenplay.Action, NewPage: true}
	if again := Parse(Write(doc)); !again.Elements[1].NewPage || again.Elements[1].Type != screenplay.Scene {
		t.Errorf("carried page break: %+v", again.Elements)
	}
}
