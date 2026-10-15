// Package pdf renders laid-out pages to PDF. It writes the file by hand
// using the standard Courier fonts that every PDF reader ships with, so no
// font is embedded and there is no external dependency.
package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"unicode/utf16"

	"plotzilla/internal/layout"
	"plotzilla/internal/screenplay"
)

type Options struct {
	Title  string
	Author string
	Paper  screenplay.Paper
	// Revisions are the document's revision sets. When set, a page whose
	// text was revised gets a header naming its revision, and its revised
	// lines an asterisk in the right margin.
	Revisions []screenplay.Revision
}

const (
	fontSize = 12.0
	ptPerIn  = 72.0
)

var fontNames = []string{"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique"}

// Render writes pages to w. The title page, if any, must be the first page
// and have Number 0 so that it is not numbered. It returns, sorted, the
// characters the standard Courier fonts cannot draw: each one is written
// as "?" in the output, so the caller can warn instead of corrupting the
// script silently.
func Render(w io.Writer, pages []layout.Page, opt Options) ([]rune, error) {
	geo := layout.PaperGeometry(opt.Paper)
	var objs [][]byte // objs[i] is object number i+1
	add := func(s string) int {
		objs = append(objs, []byte(s))
		return len(objs)
	}
	// 1: catalog, 2: page tree, 3-6: fonts, 7: info. Pages follow.
	add("<< /Type /Catalog /Pages 2 0 R >>")
	add("") // page tree, filled once the kids are known
	for _, f := range fontNames {
		add("<< /Type /Font /Subtype /Type1 /BaseFont /" + f + " /Encoding /WinAnsiEncoding >>")
	}
	add(fmt.Sprintf("<< /Title %s /Author %s /Creator (Plotzilla) /Producer (Plotzilla) >>", textString(opt.Title), textString(opt.Author)))

	missing := map[rune]bool{}
	var kids []string
	for _, pg := range pages {
		stream, err := deflate(pageContent(pg, geo, opt.Revisions, missing))
		if err != nil {
			return nil, err
		}
		content := add(fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(stream), stream))
		page := add(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] "+
			"/Resources << /Font << /F1 3 0 R /F2 4 0 R /F3 5 0 R /F4 6 0 R >> >> /Contents %d 0 R >>",
			points(geo.Width), points(geo.Height), content))
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
	}
	objs[1] = []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids)))

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n", i+1)
		buf.Write(o)
		buf.WriteString("\nendobj\n")
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info 7 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	_, err := w.Write(buf.Bytes())
	return sortedRunes(missing), err
}

func sortedRunes(set map[rune]bool) []rune {
	out := make([]rune, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// points converts inches to PDF points, rounded to 1/100 pt.
func points(in float64) float64 { return math.Round(in*ptPerIn*100) / 100 }

// baseline returns the PDF y coordinate of the baseline of a body row.
func baseline(geo layout.Geometry, row int) float64 {
	top := (geo.Height - layout.TopMargin) * ptPerIn
	return top - float64(row)*fontSize - 9
}

func text(b *bytes.Buffer, x, y float64, s string, missing map[rune]bool) {
	fmt.Fprintf(b, "BT /F1 12 Tf %.2f %.2f Td %s Tj ET\n", x, y, pdfString(s, missing))
}

func pageContent(pg layout.Page, geo layout.Geometry, revisions []screenplay.Revision, missing map[rune]bool) []byte {
	var b bytes.Buffer
	header := (geo.Height-0.5)*ptPerIn - 9
	if pg.Number > 0 && pg.Label != "" && pg.Label != "1" {
		num := pg.Label + "."
		text(&b, layout.RightEdge*ptPerIn-float64(len([]rune(num)))*fontSize*0.6, header, num, missing)
	}
	// A revised page names its revision; its latest changes are starred.
	rev := pg.Rev()
	if rev > len(revisions) {
		rev = 0
	}
	if rev > 0 {
		r := revisions[rev-1]
		label := r.Name + " Revision"
		if r.Date != "" {
			label += " - " + r.Date
		}
		text(&b, 1.5*ptPerIn, header, label, missing)
	}
	for _, ln := range pg.Lines {
		y := baseline(geo, ln.Row)
		x := ln.X * ptPerIn
		if ln.SceneNumber != "" {
			text(&b, 0.75*ptPerIn, y, ln.SceneNumber, missing)
			text(&b, geo.SceneNumber*ptPerIn, y, ln.SceneNumber, missing)
		}
		if rev > 0 && ln.Rev == rev {
			text(&b, geo.RevisionMark*ptPerIn, y, "*", missing)
		}
		fmt.Fprintf(&b, "BT %.2f %.2f Td ", x, y)
		cursor := x
		var underlines [][2]float64
		for _, s := range ln.Segments {
			font := 1
			if s.Bold {
				font++
			}
			if s.Italic {
				font += 2
			}
			width := float64(len([]rune(s.Text))) * fontSize * 0.6
			if s.Underline {
				underlines = append(underlines, [2]float64{cursor, cursor + width})
			}
			cursor += width
			fmt.Fprintf(&b, "/F%d 12 Tf %s Tj ", font, pdfString(s.Text, missing))
		}
		b.WriteString("ET\n")
		for _, u := range underlines {
			fmt.Fprintf(&b, "0.6 w %.2f %.2f m %.2f %.2f l S\n", u[0], y-1.8, u[1], y-1.8)
		}
	}
	return b.Bytes()
}

func deflate(data []byte) ([]byte, error) {
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// pdfString encodes s as a WinAnsi literal string for the Courier fonts,
// recording in missing the characters they cannot draw.
func pdfString(s string, missing map[rune]bool) string {
	var b strings.Builder
	b.WriteByte('(')
	for _, r := range s {
		c, ok := winAnsi(r)
		if !ok {
			missing[r] = true
		}
		if c == '(' || c == ')' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte(')')
	return b.String()
}

// textString encodes s for the document info dictionary.
func textString(s string) string {
	ascii := true
	for _, r := range s {
		if r > 0x7e || r < 0x20 {
			ascii = false
			break
		}
	}
	if ascii {
		return pdfString(s, map[rune]bool{}) // ASCII only: nothing to record
	}
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteString(">")
	return b.String()
}

var cp1252 = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87,
	'ˆ': 0x88, '‰': 0x89, 'Š': 0x8a, '‹': 0x8b, 'Œ': 0x8c, 'Ž': 0x8e,
	'‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9a, '›': 0x9b, 'œ': 0x9c, 'ž': 0x9e, 'Ÿ': 0x9f,
}

// winAnsi encodes r as a WinAnsi byte; ok is false when the standard
// fonts cannot draw r and '?' is written instead.
func winAnsi(r rune) (c byte, ok bool) {
	switch {
	case r >= 0x20 && r < 0x7f:
		return byte(r), true
	case r >= 0xa0 && r <= 0xff:
		return byte(r), true
	}
	if c, ok := cp1252[r]; ok {
		return c, true
	}
	if r == ' ' || r == ' ' {
		return ' ', true // narrow spaces used before French punctuation
	}
	return '?', false
}
