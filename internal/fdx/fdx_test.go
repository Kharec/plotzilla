package fdx

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"plotzilla/internal/screenplay"
)

func TestRoundTrip(t *testing.T) {
	doc := &screenplay.Document{
		TitlePage: screenplay.TitlePage{Title: "Le Dernier Rouleau", Credit: "Written by", Author: "Jane Doe"},
		Elements: []screenplay.Element{
			{Type: screenplay.Scene, Runs: []screenplay.Run{{Text: "INT. CABINE - NUIT"}}, SceneNumber: "1"},
			{Type: screenplay.Action, Runs: []screenplay.Run{{Text: "Elle "}, {Text: "entre", Bold: true, Underline: true}, {Text: "."}}},
			{Type: screenplay.Character, Runs: []screenplay.Run{{Text: "ÉLODIE"}}},
			{Type: screenplay.Parenthetical, Runs: []screenplay.Run{{Text: "(bas)"}}},
			{Type: screenplay.Dialogue, Runs: []screenplay.Run{{Text: "Ça finit ici."}}},
			{Type: screenplay.Character, Runs: []screenplay.Run{{Text: "BOB"}}, Dual: true},
			{Type: screenplay.Dialogue, Runs: []screenplay.Run{{Text: "Non."}}},
			{Type: screenplay.Shot, Runs: []screenplay.Run{{Text: "INSERT - LA LETTRE"}}},
			{Type: screenplay.Centered, Runs: []screenplay.Run{{Text: "Fin"}}},
			{Type: screenplay.Intertitle, Runs: []screenplay.Run{{Text: "PARIS, 1942"}}},
		},
	}
	var buf bytes.Buffer
	if err := Write(&buf, doc); err != nil {
		t.Fatal(err)
	}
	got, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Elements, doc.Elements) {
		t.Errorf("elements:\n got %+v\nwant %+v", got.Elements, doc.Elements)
	}
	if got.TitlePage.Title != doc.TitlePage.Title || got.TitlePage.Author != doc.TitlePage.Author {
		t.Errorf("title page: %+v", got.TitlePage)
	}
}

func TestOutlineNotesRevisions(t *testing.T) {
	bob := screenplay.Element{Type: screenplay.Character, Runs: []screenplay.Run{{Text: "BOB"}}, Dual: true}
	doc := &screenplay.Document{
		Settings: screenplay.Settings{
			Revisions: []screenplay.Revision{{Name: "Blue", Color: "blue", Date: "09/14/26"}, {Name: "Pink", Color: "pink", Date: "09/28/26"}},
			Revising:  true,
		},
		Elements: []screenplay.Element{
			{Type: screenplay.Section, Level: 2, Runs: []screenplay.Run{{Text: "Act One"}}},
			{Type: screenplay.Synopsis, Runs: []screenplay.Run{{Text: "It starts."}}},
			{Type: screenplay.Scene, Runs: []screenplay.Run{{Text: "INT. BOOTH - NIGHT"}}, NewPage: true},
			{Type: screenplay.Action, Runs: []screenplay.Run{
				{Text: "He "}, {Text: "waits", Rev: 2, Bold: true}, {Text: ".", Rev: 1},
				{Text: "# not a section", Note: true}, {Text: " Then", Rev: 1}, {Text: "fix", Note: true},
			}},
			{Type: screenplay.Action, Runs: []screenplay.Run{{Text: "only a note", Note: true}}},
			{Type: screenplay.Character, Runs: []screenplay.Run{{Text: "ALICE"}}, NewPage: true},
			{Type: screenplay.Dialogue, Runs: []screenplay.Run{{Text: "Hi."}}},
			bob,
			{Type: screenplay.Dialogue, Runs: []screenplay.Run{{Text: "Hey."}}},
			{Type: screenplay.Section, Level: 1, Runs: []screenplay.Run{{Text: "The End"}}},
		},
	}
	var buf bytes.Buffer
	if err := Write(&buf, doc); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`StartsNewPage="Yes"`, `RevisionID="2"`, `Name="Pink Revision - 09/28/26"`, `RevisionMode="Yes"`, `\# not a section`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	got, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Settings, doc.Settings) {
		t.Errorf("settings: %+v", got.Settings)
	}
	if !reflect.DeepEqual(got.Elements, doc.Elements) {
		for i := range got.Elements {
			if i >= len(doc.Elements) || !reflect.DeepEqual(got.Elements[i], doc.Elements[i]) {
				t.Errorf("element %d: %+v", i, got.Elements[i])
			}
		}
		t.Fatalf("round trip:\n%s", out)
	}
}

func TestFinalDraftRevisionNames(t *testing.T) {
	// A set named by Final Draft keeps its colour; an unknown name falls
	// back to the colour of its position.
	if r := readRevision(revision{Name: "Goldenrod Rev."}, 5); r.Name != "Goldenrod" || r.Color != "goldenrod" {
		t.Errorf("got %+v", r)
	}
	if r := readRevision(revision{Name: "Second draft"}, 2); r.Name != "Second draft" || r.Color != "pink" {
		t.Errorf("got %+v", r)
	}
}
