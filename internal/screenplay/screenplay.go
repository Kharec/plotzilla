// Package screenplay holds the in-memory model of a screenplay: an ordered
// list of typed elements (scene heading, action, character, ...) plus a
// title page. Every file format and renderer converts to and from this model.
package screenplay

import (
	"strconv"
	"strings"
	"unicode"
)

type ElementType string

const (
	Scene         ElementType = "scene"
	Action        ElementType = "action"
	Character     ElementType = "character"
	Parenthetical ElementType = "parenthetical"
	Dialogue      ElementType = "dialogue"
	Transition    ElementType = "transition"
	Centered      ElementType = "centered"
	// Shot is an insert or camera direction ("INSERT - THE LETTER",
	// "CLOSE ON", "POV"): set like a scene heading but not a scene.
	Shot ElementType = "shot"
	// Intertitle is on-screen text: a title card, centred in capitals.
	Intertitle ElementType = "intertitle"
	// Section is an outline heading (Fountain "#", "##"…) and Synopsis a
	// summary line (Fountain "="). Neither is printed.
	Section  ElementType = "section"
	Synopsis ElementType = "synopsis"
)

// Printed reports whether the element appears on the printed page.
func (t ElementType) Printed() bool {
	return t != Section && t != Synopsis
}

// Uppercase reports whether the element is always printed in capitals.
func (t ElementType) Uppercase() bool {
	return t == Scene || t == Character || t == Transition || t == Shot || t == Intertitle
}

// InDialogue reports whether the element belongs to a dialogue block
// (the lines that follow a character cue).
func (t ElementType) InDialogue() bool {
	return t == Parenthetical || t == Dialogue
}

// Run is a span of text sharing the same emphasis. A "\n" inside Text is a
// hard line break within the element.
type Run struct {
	Text      string `json:"text"`
	Bold      bool   `json:"b,omitempty"`
	Italic    bool   `json:"i,omitempty"`
	Underline bool   `json:"u,omitempty"`
	// Note marks a writer's note (Fountain "[[…]]"): kept and shown in the
	// editor, never printed.
	Note bool `json:"n,omitempty"`
	// Rev is the revision set (1 = the first, see Settings.Revisions) in
	// which the text was added or changed; 0 is original text.
	Rev int `json:"rev,omitempty"`
}

func (r Run) SameStyle(o Run) bool {
	return r.Bold == o.Bold && r.Italic == o.Italic && r.Underline == o.Underline && r.Note == o.Note && r.Rev == o.Rev
}

type Element struct {
	Type ElementType `json:"type"`
	Runs []Run       `json:"runs"`
	// SceneNumber is a locked scene number ("12A"). Empty means automatic.
	SceneNumber string `json:"sceneNumber,omitempty"`
	// Dual, on a character cue, puts its dialogue block beside the
	// previous one (dual dialogue, Fountain's "^").
	Dual bool `json:"dual,omitempty"`
	// Level is the depth of a section, from 1 ("#").
	Level int `json:"level,omitempty"`
	// NewPage forces a page break before the element (Fountain "===").
	// On a dialogue part it applies to the whole dialogue block.
	NewPage bool `json:"newPage,omitempty"`
	// Locked lists the locked pages that start inside this element, in
	// offset order (see Settings.Locked).
	Locked []PageLock `json:"locked,omitempty"`
}

// PageLock fixes where a page starts once pages are locked: the page
// labelled Page starts at rune Offset of the element's text.
type PageLock struct {
	Offset int    `json:"offset"`
	Page   string `json:"page"`
}

func NewElement(t ElementType, text string) Element {
	e := Element{Type: t, Runs: []Run{}}
	if text != "" {
		e.Runs = append(e.Runs, Run{Text: text})
	}
	return e
}

// Text returns the element's plain text, without emphasis.
func (e Element) Text() string {
	var b strings.Builder
	for _, r := range e.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

func (e Element) IsEmpty() bool {
	return strings.TrimSpace(e.Text()) == ""
}

// PrintedText returns the text that is printed: without notes.
func (e Element) PrintedText() string {
	var b strings.Builder
	for _, r := range e.Runs {
		if !r.Note {
			b.WriteString(r.Text)
		}
	}
	return b.String()
}

type TitlePage struct {
	Title     string `json:"title"`
	Credit    string `json:"credit"`
	Author    string `json:"author"`
	Source    string `json:"source"`
	DraftDate string `json:"draftDate"`
	Contact   string `json:"contact"`
	Notes     string `json:"notes"`
}

func (t TitlePage) IsEmpty() bool {
	return strings.TrimSpace(t.Title+t.Author+t.Source+t.DraftDate+t.Contact+t.Notes) == ""
}

type Document struct {
	TitlePage TitlePage `json:"titlePage"`
	Elements  []Element `json:"elements"`
	Settings  Settings  `json:"settings"`
}

// Paper is the page size: "" for US Letter, or A4.
type Paper string

const (
	Letter Paper = ""
	A4     Paper = "a4"
)

// Settings are the document's production settings. The zero value is a
// US Letter script without revisions.
type Settings struct {
	Paper Paper `json:"paper,omitempty"`
	// Revisions are the revision sets issued so far, in order: the text
	// of set n carries Run.Rev == n.
	Revisions []Revision `json:"revisions,omitempty"`
	// Revising marks new text with the latest revision set.
	Revising bool `json:"revising,omitempty"`
	// Locked keeps the page numbers of Element.Locked: added material goes
	// to A pages (12A), removed material leaves shorter or merged pages.
	Locked bool `json:"locked,omitempty"`
}

func (s Settings) IsZero() bool {
	return s.Paper == Letter && len(s.Revisions) == 0 && !s.Revising && !s.Locked
}

// Revision is a revision set, named after the colour of its pages.
type Revision struct {
	Name  string `json:"name"`  // "Blue", "Double Pink"
	Color string `json:"color"` // "blue", "pink"…: the paper colour
	Date  string `json:"date"`
}

// revisionColors is the industry order of revision pages. The first cycle
// starts at Blue (the original draft is White); later cycles start again
// at White, as "Double White", "Double Blue"…
var revisionColors = []string{"White", "Blue", "Pink", "Yellow", "Green", "Goldenrod", "Buff", "Salmon", "Cherry"}

// RevisionSet returns the name and colour of revision set n (from 1).
func RevisionSet(n int) Revision {
	n = max(n, 1)
	cycle, i := n/len(revisionColors), n%len(revisionColors)
	name := revisionColors[i]
	r := Revision{Name: name, Color: strings.ToLower(name)}
	switch cycle {
	case 0:
	case 1:
		r.Name = "Double " + name
	case 2:
		r.Name = "Triple " + name
	default:
		r.Name = ordinal(cycle+1) + " " + name
	}
	return r
}

func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// New returns an empty screenplay, ready to receive its first scene heading.
func New() *Document {
	return &Document{
		TitlePage: TitlePage{Credit: "Written by"},
		Elements:  []Element{NewElement(Scene, "")},
	}
}

// NormalizeRuns drops empty runs and merges neighbours with the same style.
func NormalizeRuns(runs []Run) []Run {
	out := make([]Run, 0, len(runs))
	for _, r := range runs {
		if r.Text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].SameStyle(r) {
			out[n-1].Text += r.Text
			continue
		}
		out = append(out, r)
	}
	return out
}

// CharacterName extracts the bare name from a character cue:
// "John (V.O.)" -> "JOHN".
func CharacterName(cue string) string {
	cue = strings.TrimSuffix(strings.TrimSpace(cue), "^")
	if i := strings.Index(cue, "("); i >= 0 {
		cue = cue[:i]
	}
	return strings.ToUpper(strings.TrimSpace(cue))
}

// UpperRunes upper-cases rune by rune so that offsets are preserved
// (strings.ToUpper may change the length, e.g. "ß" -> "SS").
func UpperRunes(s string) string {
	r := []rune(s)
	for i, c := range r {
		r[i] = unicode.ToUpper(c)
	}
	return string(r)
}
