package translate

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"plotzilla/internal/screenplay"
)

// fakeMT translates from a fixed table; a sentence it does not know comes
// back in angle brackets, so the test shows what was asked.
type fakeMT struct {
	mu    sync.Mutex
	dict  map[string]string
	calls []string
	err   error
}

func (f *fakeMT) Translate(ctx context.Context, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, text)
	if f.err != nil {
		return "", f.err
	}
	if out, ok := f.dict[text]; ok {
		return out, nil
	}
	return "<" + text + ">", nil
}

type fakeLexicon map[string][2]bool

func (l fakeLexicon) Knows(w string) (bool, bool) { return l[w][0], l[w][1] }

var lexicon = fakeLexicon{
	"attends": {true, true}, "cuisine": {true, true}, "laughing": {false, true}, "phone": {false, true},
	"rings": {false, true}, "nobody": {false, true}, "moves": {false, true}, "kitchen": {false, true},
	"street": {false, true}, "pleut": {true, false}, "entre": {true, false}, "lettre": {true, false}, "octobre": {true, false},
}

var dict = map[string]string{
	"Le cuisine de Zorbin":      "Zorbin's kitchen",
	"Le rue":                    "The street",
	"Zorbin ?":                  "Zorbin?",
	"Tu es là ?":                "Are you there?",
	"Zorbin décroche.":          "Zorbin picks up.",
	"Le policier":               "The policeman",
	"à Zorbin":                  "To Zorbin",
	"Attends !":                 "Wait!",
	"Elle entre.":               "She comes in.",
	"Il pleut.":                 "It's raining.",
	"Vérifier la date":          "Check the date.",
	"la lettre.":                "the letter.",
	"Je l'ai lue.":              "I read it.",
	"1er octobre 2026":          "1 October 2026",
	"Il pleut. Elle entre.":     "unused",
	"Bonjour, Pierre.":          "Hello, Pierre.",
	"Bonjour, « Zorbin » !":     "Hello, « Zorbin » !",
	"Ça marche, Zorbin.":        "That works, Zorbin.",
	"Zorbin et Dravok sortent.": "Zorbin and Dravok go out.",
}

func el(t screenplay.ElementType, text string) screenplay.Element {
	return screenplay.NewElement(t, text)
}

func translateDoc(t *testing.T, doc *screenplay.Document, opts Options) (Result, *fakeMT) {
	t.Helper()
	mt := &fakeMT{dict: dict}
	res, err := Document(context.Background(), doc, mt, lexicon, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res, mt
}

// texts applies the changes and returns every element's text.
func texts(doc *screenplay.Document, res Result) []string {
	out := make([]string, len(doc.Elements))
	for i, e := range doc.Elements {
		out[i] = e.Text()
	}
	for _, c := range res.Changes {
		out[c.Index] = c.Element.Text()
	}
	return out
}

func TestScreenplayElements(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{
		el(screenplay.Scene, "INT. CUISINE DE MARIE - NUIT"),
		el(screenplay.Character, "PIERRE (VOIX OFF)"),
		el(screenplay.Dialogue, "Marie ? Tu es là ?"),
		el(screenplay.Character, "MARIE"),
		el(screenplay.Parenthetical, "(un temps)"),
		el(screenplay.Dialogue, "Attends !"),
		el(screenplay.Parenthetical, "(à Marie)"),
		el(screenplay.Character, "LE POLICIER"),
		el(screenplay.Character, "MARIE (SUITE)"),
		el(screenplay.Transition, "FONDU ENCHAÎNÉ :"),
		el(screenplay.Scene, "EXTÉRIEUR RUE - AUBE (FLASH-BACK)"),
		el(screenplay.Action, "The phone rings. Marie décroche."),
		el(screenplay.Centered, "FIN"),
	}}
	res, _ := translateDoc(t, doc, Options{})
	want := []string{
		"INT. MARIE'S KITCHEN - NIGHT",
		"PIERRE (V.O.)",
		"Marie? Are you there?",
		"MARIE",
		"(beat)",
		"Wait!",
		"(to Marie)",
		"THE POLICEMAN",
		"MARIE (CONT'D)",
		"DISSOLVE TO:",
		"EXT. STREET - DAWN (FLASHBACK)",
		"The phone rings. Marie picks up.",
		"THE END",
	}
	if got := texts(doc, res); !reflect.DeepEqual(got, want) {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("element %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
	if slices.ContainsFunc(res.Changes, func(c Change) bool { return c.Index == 3 }) {
		t.Error("a character name was changed")
	}
}

func TestEnglishLeftAlone(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{
		el(screenplay.Scene, "INT. KITCHEN - DAY"),
		el(screenplay.Action, "Nobody moves. The phone rings."),
		el(screenplay.Character, "PIERRE (O.S.)"),
		el(screenplay.Parenthetical, "(laughing)"),
		el(screenplay.Dialogue, "I told you, I'm not leaving."),
		el(screenplay.Transition, "CUT TO:"),
		el(screenplay.Shot, "CLOSE ON THE LETTER"),
		el(screenplay.Action, "OK."),
	}}
	res, mt := translateDoc(t, doc, Options{TitlePage: true})
	if len(res.Changes) != 0 || res.TitlePage != nil || len(mt.calls) != 0 {
		t.Errorf("English script changed: %+v, model asked %q", res, mt.calls)
	}
}

func TestActionRetyped(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{
		el(screenplay.Action, "FONDU EN OUVERTURE :"),
		el(screenplay.Action, "GROS PLAN SUR la lettre."),
		el(screenplay.Action, "COUPE SUR :"),
		el(screenplay.Action, "Il pleut."),
	}}
	res, _ := translateDoc(t, doc, Options{})
	want := map[int]screenplay.Element{
		0: el(screenplay.Transition, "FADE IN:"),
		1: el(screenplay.Shot, "CLOSE ON the letter."),
		2: el(screenplay.Transition, "CUT TO:"),
		3: el(screenplay.Action, "It's raining."),
	}
	for _, c := range res.Changes {
		if w := want[c.Index]; c.Element.Type != w.Type || c.Element.Text() != w.Text() {
			t.Errorf("element %d = %s %q, want %s %q", c.Index, c.Element.Type, c.Element.Text(), w.Type, w.Text())
		}
	}
	if len(res.Changes) != len(want) {
		t.Errorf("%d changes, want %d", len(res.Changes), len(want))
	}
}

func TestNotesStylesAndLocks(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{{
		Type: screenplay.Action,
		Runs: []screenplay.Run{
			{Text: "Elle entre. ", Bold: true},
			{Text: "Vérifier la date", Note: true},
			{Text: " Il pleut.", Rev: 1},
		},
		Locked: []screenplay.PageLock{{Offset: 0, Page: "2"}, {Offset: 28, Page: "3"}},
	}}}
	res, _ := translateDoc(t, doc, Options{})
	if len(res.Changes) != 1 {
		t.Fatalf("changes: %+v", res.Changes)
	}
	got := res.Changes[0].Element
	want := []screenplay.Run{
		{Text: "She comes in. ", Bold: true},
		// No full stop: the note had none.
		{Text: "Check the date", Note: true},
		{Text: " It's raining.", Rev: 1},
	}
	if !reflect.DeepEqual(got.Runs, want) {
		t.Errorf("runs = %+v, want %+v", got.Runs, want)
	}
	// 28 of 38 runes → the same proportion of the 42 translated ones.
	if want := []screenplay.PageLock{{Offset: 0, Page: "2"}, {Offset: 31, Page: "3"}}; !reflect.DeepEqual(got.Locked, want) {
		t.Errorf("locks = %+v, want %+v", got.Locked, want)
	}
}

func TestSelectionAndTitlePage(t *testing.T) {
	doc := &screenplay.Document{
		TitlePage: screenplay.TitlePage{Title: "La Dernière Bobine", Credit: "Écrit par", DraftDate: "1er octobre 2026"},
		Elements: []screenplay.Element{
			el(screenplay.Action, "Il pleut."),
			el(screenplay.Action, "Elle entre."),
		},
	}
	res, _ := translateDoc(t, doc, Options{Elements: []int{1}})
	if len(res.Changes) != 1 || res.Changes[0].Index != 1 || res.TitlePage != nil {
		t.Errorf("selection: %+v", res)
	}
	res, _ = translateDoc(t, doc, Options{TitlePage: true})
	want := screenplay.TitlePage{Title: "La Dernière Bobine", Credit: "Written by", DraftDate: "1 October 2026"}
	if res.TitlePage == nil || *res.TitlePage != want {
		t.Errorf("title page = %+v, want %+v", res.TitlePage, want)
	}
}

func TestNamesAndTypography(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{
		el(screenplay.Character, "MARIE"),
		el(screenplay.Character, "PIERRE"),
		el(screenplay.Dialogue, "Bonjour, « Marie » !"),
		el(screenplay.Action, "Ça marche, PIERRE."),
		el(screenplay.Action, "Marie et Pierre sortent."),
	}}
	res, mt := translateDoc(t, doc, Options{})
	got := texts(doc, res)
	want := []string{"MARIE", "PIERRE", `Hello, "Marie"!`, "That works, PIERRE.", "Marie and Pierre go out."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q (model asked %q)", got, want, mt.calls)
	}
}

func TestIdempotent(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{
		el(screenplay.Scene, "INT. CUISINE DE MARIE - NUIT"),
		el(screenplay.Character, "MARIE (SUITE)"),
		el(screenplay.Dialogue, "Je l'ai lue."),
	}}
	res, _ := translateDoc(t, doc, Options{})
	for _, c := range res.Changes {
		doc.Elements[c.Index] = c.Element
	}
	res, mt := translateDoc(t, doc, Options{})
	if len(res.Changes) != 0 || len(mt.calls) != 0 {
		t.Errorf("second translation changed %+v, asked %q", res.Changes, mt.calls)
	}
}

func TestModelError(t *testing.T) {
	doc := &screenplay.Document{Elements: []screenplay.Element{el(screenplay.Action, "Il pleut.")}}
	boom := errors.New("boom")
	if _, err := Document(context.Background(), doc, &fakeMT{err: boom}, nil, Options{Workers: 4}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Document(ctx, doc, &fakeMT{dict: dict}, nil, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSentences(t *testing.T) {
	tests := map[string][]string{
		"Il pleut. Elle entre.":          {"Il pleut. ", "Elle entre."},
		"Quoi ? Non !":                   {"Quoi ? ", "Non !"},
		"M. Dupont entre. J. Doe aussi.": {"M. Dupont entre. ", "J. Doe aussi."},
		"Attends... il revient.":         {"Attends... il revient."},
		"« Non. » Il sort.":              {"« Non. » ", "Il sort."},
	}
	for in, want := range tests {
		if got := sentences(in); !reflect.DeepEqual(got, want) {
			t.Errorf("sentences(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetection(t *testing.T) {
	tests := []struct {
		text   string
		french bool
	}{
		{"Il pleut sur la ville.", true},
		{"It's raining on the city.", false},
		{"Qu'est-ce que tu fais ?", true},
		{"Allô ?", true},
		{"OK.", false},
		{"Don't move.", false},
		{"Marie", false},
	}
	s := &session{lex: lexicon, nameWords: map[string]bool{"marie": true}}
	for _, tt := range tests {
		if got := s.french(tt.text); got != tt.french {
			t.Errorf("french(%q) = %v, want %v", tt.text, got, tt.french)
		}
	}
}
