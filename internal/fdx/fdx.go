// Package fdx reads and writes Final Draft XML (.fdx) files.
//
// Final Draft has no Fountain sections or synopses: they are written as
// script notes whose text starts with "#" or "=", attached to the start of
// the next paragraph, and read back as such. Notes inside the text are
// script notes at their position; revised text carries the RevisionID of
// its revision set; a forced page break is StartsNewPage.
package fdx

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"plotzilla/internal/screenplay"
)

type file struct {
	XMLName      xml.Name   `xml:"FinalDraft"`
	DocumentType string     `xml:"DocumentType,attr"`
	Template     string     `xml:"Template,attr"`
	Version      string     `xml:"Version,attr"`
	Content      content    `xml:"Content"`
	TitlePage    *titlePage `xml:"TitlePage,omitempty"`
	Revisions    *revisions `xml:"Revisions,omitempty"`
}

type revisions struct {
	ActiveSet    int        `xml:"ActiveSet,attr"`
	RevisionMode string     `xml:"RevisionMode,attr"`
	Items        []revision `xml:"Revision"`
}

type revision struct {
	ID           int    `xml:"ID,attr"`
	Name         string `xml:"Name,attr"`
	Color        string `xml:"Color,attr,omitempty"`
	PageColor    string `xml:"PageColor,attr,omitempty"`
	Mark         string `xml:"Mark,attr,omitempty"`
	FullRevision string `xml:"FullRevision,attr,omitempty"`
}

type scriptNote struct {
	ID         int         `xml:"ID,attr"`
	Range      string      `xml:"Range,attr"` // "start,length" in the paragraph's text
	Paragraphs []paragraph `xml:"Paragraph"`
}

type content struct {
	Paragraphs []paragraph `xml:"Paragraph"`
}

type titlePage struct {
	Content content `xml:"Content"`
}

type paragraph struct {
	Type          string       `xml:"Type,attr,omitempty"`
	Number        string       `xml:"Number,attr,omitempty"`
	Alignment     string       `xml:"Alignment,attr,omitempty"`
	StartsNewPage string       `xml:"StartsNewPage,attr,omitempty"`
	ScriptNotes   []scriptNote `xml:"ScriptNote"`
	Texts         []text       `xml:"Text"`
	DualDialogue  *content     `xml:"DualDialogue,omitempty"`
}

type text struct {
	Style      string `xml:"Style,attr,omitempty"`
	RevisionID int    `xml:"RevisionID,attr,omitempty"`
	Value      string `xml:",chardata"`
}

// revisionColors are Final Draft's 48-bit page colours for each revision
// colour, in the order they are searched for in a revision set's name.
var revisionColors = []struct{ name, hex string }{
	{"goldenrod", "#DADAA5A52020"},
	{"white", "#FFFFFFFFFFFF"},
	{"blue", "#ADADD8D8E6E6"},
	{"pink", "#FFFFC0C0CBCB"},
	{"yellow", "#FFFFFFFF9999"},
	{"green", "#9999FFFF9999"},
	{"buff", "#F0F0DCDC8282"},
	{"salmon", "#FAFA80807272"},
	{"cherry", "#DEDE31316363"},
}

// revisionName is how Plotzilla names a set in Final Draft: "Blue
// Revision - 09/28/26".
var revisionName = regexp.MustCompile(`^(.*?) Revision(?: - (.+))?$`)

var typeNames = map[screenplay.ElementType]string{
	screenplay.Scene:         "Scene Heading",
	screenplay.Action:        "Action",
	screenplay.Character:     "Character",
	screenplay.Parenthetical: "Parenthetical",
	screenplay.Dialogue:      "Dialogue",
	screenplay.Transition:    "Transition",
	screenplay.Centered:      "Action",
	screenplay.Shot:          "Shot",
	screenplay.Intertitle:    "Action",
}

// Read parses an FDX document.
func Read(r io.Reader) (*screenplay.Document, error) {
	var f file
	dec := xml.NewDecoder(r)
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("reading FDX: %w", err)
	}
	doc := &screenplay.Document{}
	rd := &reader{levels: map[int]int{}}
	if f.Revisions != nil {
		for i, rv := range f.Revisions.Items {
			rd.levels[rv.ID] = i + 1
			doc.Settings.Revisions = append(doc.Settings.Revisions, readRevision(rv, i+1))
		}
		doc.Settings.Revising = f.Revisions.RevisionMode == "Yes" && len(f.Revisions.Items) > 0
	}
	for _, p := range f.Content.Paragraphs {
		rd.paragraph(p)
	}
	doc.Elements = rd.out
	if len(doc.Elements) == 0 {
		doc.Elements = []screenplay.Element{screenplay.NewElement(screenplay.Scene, "")}
	}
	if f.TitlePage != nil {
		doc.TitlePage = readTitlePage(f.TitlePage.Content.Paragraphs)
	}
	return doc, nil
}

func readRevision(rv revision, level int) screenplay.Revision {
	def := screenplay.RevisionSet(level)
	r := screenplay.Revision{Name: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rv.Name), "Rev."))}
	if m := revisionName.FindStringSubmatch(rv.Name); m != nil {
		r.Name, r.Date = m[1], m[2]
	}
	lower := strings.ToLower(r.Name)
	for _, c := range revisionColors {
		if strings.Contains(lower, c.name) {
			r.Color = c.name
			break
		}
	}
	if r.Color == "" {
		r.Color = def.Color
	}
	if r.Name == "" {
		r.Name = def.Name
	}
	return r
}

type reader struct {
	out     []screenplay.Element
	levels  map[int]int // Final Draft RevisionID → revision set
	newPage bool        // a page break on a paragraph that was left out
}

type inlineNote struct {
	offset int
	text   string
}

// notes adds the sections and synopses stored in a paragraph's script
// notes and returns its other notes.
func (rd *reader) notes(sns []scriptNote) []inlineNote {
	var inline []inlineNote
	for _, sn := range sns {
		var lines []string
		for _, p := range sn.Paragraphs {
			var b strings.Builder
			for _, t := range p.Texts {
				b.WriteString(t.Value)
			}
			lines = append(lines, b.String())
		}
		body := strings.Join(lines, "\n")
		switch {
		case strings.HasPrefix(body, "#"):
			rest := strings.TrimLeft(body, "#")
			rd.out = append(rd.out, screenplay.Element{Type: screenplay.Section, Level: len(body) - len(rest),
				Runs: []screenplay.Run{{Text: strings.TrimSpace(rest)}}})
		case strings.HasPrefix(body, "="):
			rd.out = append(rd.out, screenplay.Element{Type: screenplay.Synopsis,
				Runs: []screenplay.Run{{Text: strings.TrimSpace(strings.TrimPrefix(body, "="))}}})
		default:
			start, _, _ := strings.Cut(sn.Range, ",")
			offset, _ := strconv.Atoi(strings.TrimSpace(start))
			inline = append(inline, inlineNote{offset: max(offset, 0), text: strings.TrimPrefix(body, `\`)})
		}
	}
	return inline
}

func (rd *reader) paragraph(p paragraph) {
	notes := rd.notes(p.ScriptNotes)
	newPage := p.StartsNewPage == "Yes" || rd.newPage
	rd.newPage = false
	if p.DualDialogue != nil {
		start := len(rd.out)
		cues := 0
		for _, inner := range p.DualDialogue.Paragraphs {
			n := len(rd.out)
			rd.paragraph(inner)
			if inner.Type == "Character" && len(rd.out) > n {
				if cues++; cues == 2 {
					rd.out[len(rd.out)-1].Dual = true
				}
			}
		}
		if newPage && len(rd.out) > start {
			rd.out[start].NewPage = true
		} else {
			rd.newPage = newPage
		}
		return
	}
	el := screenplay.Element{SceneNumber: p.Number, NewPage: newPage}
	switch p.Type {
	case "Scene Heading":
		el.Type = screenplay.Scene
	case "Character":
		el.Type = screenplay.Character
	case "Parenthetical":
		el.Type = screenplay.Parenthetical
	case "Dialogue":
		el.Type = screenplay.Dialogue
	case "Transition":
		el.Type = screenplay.Transition
	case "Shot":
		el.Type = screenplay.Shot
	default:
		el.Type = screenplay.Action
		if p.Alignment == "Center" {
			el.Type = screenplay.Centered
		}
	}
	if el.Type != screenplay.Scene {
		el.SceneNumber = ""
	}
	for _, t := range p.Texts {
		el.Runs = append(el.Runs, screenplay.Run{
			Text:      t.Value,
			Bold:      strings.Contains(t.Style, "Bold"),
			Italic:    strings.Contains(t.Style, "Italic"),
			Underline: strings.Contains(t.Style, "Underline"),
			Rev:       rd.levels[t.RevisionID],
		})
	}
	el.Runs = insertNotes(screenplay.NormalizeRuns(el.Runs), notes)
	if el.IsEmpty() {
		rd.newPage = newPage
		return
	}
	// Final Draft has no intertitle: they are centred, all-caps action.
	if el.Type == screenplay.Centered && isAllCaps(el.PrintedText()) {
		el.Type = screenplay.Intertitle
	}
	rd.out = append(rd.out, el)
}

// insertNotes puts note runs at their rune offsets in the text.
func insertNotes(runs []screenplay.Run, notes []inlineNote) []screenplay.Run {
	if len(notes) == 0 {
		return runs
	}
	slices.SortStableFunc(notes, func(a, b inlineNote) int { return a.offset - b.offset })
	var out []screenplay.Run
	pos, next := 0, 0
	for _, r := range runs {
		rs := []rune(r.Text)
		start := 0
		for ; next < len(notes) && notes[next].offset-pos < len(rs); next++ {
			if at := notes[next].offset - pos; at > start {
				part := r
				part.Text = string(rs[start:at])
				out = append(out, part)
				start = at
			}
			out = append(out, screenplay.Run{Text: notes[next].text, Note: true})
		}
		if start < len(rs) {
			r.Text = string(rs[start:])
			out = append(out, r)
		}
		pos += len(rs)
	}
	for ; next < len(notes); next++ {
		out = append(out, screenplay.Run{Text: notes[next].text, Note: true})
	}
	return screenplay.NormalizeRuns(out)
}

// readTitlePage maps Final Draft's free-form title page onto our fields:
// the first centred line is the title, a line containing "by" is the credit
// and the following one the author; left-aligned text goes to contact.
func readTitlePage(ps []paragraph) screenplay.TitlePage {
	var tp screenplay.TitlePage
	var centered, left, right []string
	for _, p := range ps {
		var b strings.Builder
		for _, t := range p.Texts {
			b.WriteString(t.Value)
		}
		s := strings.TrimSpace(b.String())
		if s == "" {
			continue
		}
		switch p.Alignment {
		case "Center":
			centered = append(centered, s)
		case "Right":
			right = append(right, s)
		default:
			left = append(left, s)
		}
	}
loop:
	for i, s := range centered {
		switch {
		case tp.Title == "":
			tp.Title = s
		case tp.Credit == "" && strings.Contains(strings.ToLower(s), "by"):
			tp.Credit = s
		case tp.Credit != "" && tp.Author == "":
			tp.Author = s
		default:
			tp.Source = strings.TrimSpace(strings.Join(centered[i:], "\n"))
			break loop
		}
	}
	tp.Contact = strings.Join(left, "\n")
	tp.DraftDate = strings.Join(right, "\n")
	return tp
}

// Write serialises a Document to FDX. Locked pages and the paper size are
// not written.
func Write(w io.Writer, doc *screenplay.Document) error {
	wr := &writer{}
	wr.f = file{DocumentType: "Script", Template: "No", Version: "5"}
	els := doc.Elements
	for i := 0; i < len(els); {
		el := els[i]
		if !el.Type.Printed() {
			if !el.IsEmpty() {
				wr.pending = append(wr.pending, wr.note(outlineText(el), 0))
			}
			wr.newPage = wr.newPage || el.NewPage
			i++
			continue
		}
		if el.Type == screenplay.Character {
			j := blockEnd(els, i)
			if j < len(els) && els[j].Type == screenplay.Character && els[j].Dual {
				k := blockEnd(els, j)
				dual := &content{}
				for _, inner := range els[i:k] {
					if !inner.IsEmpty() {
						dual.Paragraphs = append(dual.Paragraphs, wr.paragraph(inner))
					}
				}
				wr.add(paragraph{DualDialogue: dual}, el.NewPage)
				i = k
				continue
			}
		}
		if el.IsEmpty() {
			wr.newPage = wr.newPage || el.NewPage
		} else {
			wr.add(wr.paragraph(el), el.NewPage)
		}
		i++
	}
	if len(wr.pending) > 0 {
		// Outline at the very end: an empty paragraph carries it.
		wr.f.Content.Paragraphs = append(wr.f.Content.Paragraphs, paragraph{Type: "Action", ScriptNotes: wr.pending})
	}
	if !doc.TitlePage.IsEmpty() {
		wr.f.TitlePage = &titlePage{Content: content{Paragraphs: writeTitlePage(doc.TitlePage)}}
	}
	if sets := doc.Settings.Revisions; len(sets) > 0 {
		rs := &revisions{ActiveSet: len(sets), RevisionMode: "No"}
		if doc.Settings.Revising {
			rs.RevisionMode = "Yes"
		}
		for i, set := range sets {
			name := set.Name + " Revision"
			if set.Date != "" {
				name += " - " + set.Date
			}
			hex := ""
			for _, c := range revisionColors {
				if c.name == set.Color {
					hex = c.hex
				}
			}
			rs.Items = append(rs.Items, revision{ID: i + 1, Name: name, Color: hex, PageColor: hex, Mark: "*", FullRevision: "No"})
		}
		wr.f.Revisions = rs
	}

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="no" ?>` + "\n")
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(wr.f); err != nil {
		return err
	}
	buf.WriteString("\n")
	_, err := w.Write(buf.Bytes())
	return err
}

type writer struct {
	f       file
	notes   int          // script notes written so far, for their IDs
	pending []scriptNote // sections and synopses waiting for the next paragraph
	newPage bool         // page break of an element that was left out
}

func (wr *writer) note(body string, offset int) scriptNote {
	wr.notes++
	sn := scriptNote{ID: wr.notes, Range: strconv.Itoa(offset) + ",0"}
	for _, line := range strings.Split(body, "\n") {
		sn.Paragraphs = append(sn.Paragraphs, paragraph{Texts: []text{{Value: line}}})
	}
	return sn
}

// add appends a top-level paragraph with the outline and page break that
// precede it.
func (wr *writer) add(p paragraph, newPage bool) {
	if newPage || wr.newPage {
		p.StartsNewPage = "Yes"
	}
	p.ScriptNotes = append(wr.pending, p.ScriptNotes...)
	wr.pending, wr.newPage = nil, false
	wr.f.Content.Paragraphs = append(wr.f.Content.Paragraphs, p)
}

func outlineText(el screenplay.Element) string {
	text := strings.ReplaceAll(el.Text(), "\n", " ")
	if el.Type == screenplay.Section {
		return strings.Repeat("#", max(el.Level, 1)) + " " + text
	}
	return "= " + text
}

// blockEnd returns the index just past the dialogue block starting at the
// character cue i.
func blockEnd(els []screenplay.Element, i int) int {
	j := i + 1
	for j < len(els) && els[j].Type.InDialogue() {
		j++
	}
	return j
}

func (wr *writer) paragraph(el screenplay.Element) paragraph {
	p := paragraph{Type: typeNames[el.Type]}
	if p.Type == "" {
		p.Type = "Action"
	}
	if el.Type == screenplay.Centered || el.Type == screenplay.Intertitle {
		p.Alignment = "Center"
	}
	if el.Type == screenplay.Scene {
		p.Number = el.SceneNumber
	}
	var runs []screenplay.Run
	offset := 0 // runes of printed text so far
	for _, r := range el.Runs {
		if r.Note {
			body := r.Text
			// A note starting like an outline element would be read as one.
			if strings.HasPrefix(body, "#") || strings.HasPrefix(body, "=") || strings.HasPrefix(body, `\`) {
				body = `\` + body
			}
			p.ScriptNotes = append(p.ScriptNotes, wr.note(body, offset))
			continue
		}
		offset += len([]rune(r.Text))
		runs = append(runs, r)
	}
	for _, r := range screenplay.NormalizeRuns(runs) {
		t := r.Text
		if el.Type.Uppercase() {
			t = screenplay.UpperRunes(t)
		}
		p.Texts = append(p.Texts, text{Style: style(r), RevisionID: r.Rev, Value: t})
	}
	return p
}

func writeTitlePage(tp screenplay.TitlePage) []paragraph {
	var ps []paragraph
	add := func(align, s string) {
		for _, line := range strings.Split(s, "\n") {
			ps = append(ps, paragraph{Type: "Action", Alignment: align, Texts: []text{{Value: line}}})
		}
	}
	blank := func(n int) {
		for i := 0; i < n; i++ {
			add("Center", "")
		}
	}
	blank(16)
	add("Center", tp.Title)
	blank(2)
	add("Center", tp.Credit)
	blank(1)
	add("Center", tp.Author)
	if tp.Source != "" {
		blank(2)
		add("Center", tp.Source)
	}
	blank(16)
	if tp.Contact != "" {
		add("Left", tp.Contact)
	}
	if tp.DraftDate != "" {
		add("Right", tp.DraftDate)
	}
	if tp.Notes != "" {
		add("Right", tp.Notes)
	}
	return ps
}

func isAllCaps(s string) bool {
	return strings.ContainsFunc(s, unicode.IsLetter) && !strings.ContainsFunc(s, unicode.IsLower)
}

func style(r screenplay.Run) string {
	var parts []string
	if r.Bold {
		parts = append(parts, "Bold")
	}
	if r.Italic {
		parts = append(parts, "Italic")
	}
	if r.Underline {
		parts = append(parts, "Underline")
	}
	return strings.Join(parts, "+")
}
