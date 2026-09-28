package pdf

import (
	"bytes"
	"compress/zlib"
	"io"
	"os"
	"strings"
	"testing"

	"plotzilla/internal/fountain"
	"plotzilla/internal/layout"
	"plotzilla/internal/screenplay"
)

const sample = `Title: Le Dernier Rouleau
Credit: Written by
Author: Jane Doe
Contact: jane@example.com
Draft date: 28/09/2026

INT. CABINE DE PROJECTION - NUIT

La poussière danse dans le **faisceau** du projecteur. Élodie _entre_.

ÉLODIE (V.O.)
(à voix basse)
Ça finit toujours *ici*.

MARC ^
(criant)
Non ! Pas encore, pas ce soir.

CUT TO:
`

func TestRender(t *testing.T) {
	doc := fountain.Parse(sample)
	pages := []layout.Page{layout.TitlePage(doc.TitlePage, doc.Settings.Paper)}
	pages = append(pages, layout.Paginate(doc, layout.Options{SceneNumbers: true}).Pages...)
	var buf bytes.Buffer
	if err := Render(&buf, pages, Options{Title: doc.TitlePage.Title, Author: doc.TitlePage.Author}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "%PDF-1.4") || !strings.HasSuffix(out, "%%EOF\n") || !strings.Contains(out, "/Count 2") {
		t.Fatalf("malformed PDF:\n%.300s", out)
	}
	// PLOTZILLA_PDF=/tmp/x.pdf go test ./internal/pdf keeps the file for inspection.
	if path := os.Getenv("PLOTZILLA_PDF"); path != "" {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRevisionsAndA4(t *testing.T) {
	doc := fountain.Parse(sample)
	doc.Settings.Paper = screenplay.A4
	doc.Settings.Revisions = []screenplay.Revision{{Name: "Blue", Color: "blue", Date: "09/28/26"}}
	doc.Elements[1].Runs[0].Rev = 1
	var buf bytes.Buffer
	pages := layout.Paginate(doc, layout.Options{}).Pages
	if err := Render(&buf, pages, Options{Paper: doc.Settings.Paper, Revisions: doc.Settings.Revisions}); err != nil {
		t.Fatal(err)
	}
	stream := uncompressed(t, buf.Bytes())
	if !strings.Contains(buf.String(), "/MediaBox [0 0 595.44 841.68]") {
		t.Errorf("not an A4 page")
	}
	if !strings.Contains(stream, "(Blue Revision - 09/28/26) Tj") || strings.Count(stream, "(*) Tj") != 1 {
		t.Errorf("revision header or mark missing:\n%s", stream)
	}

	// Without revisions in the options, nothing is marked.
	buf.Reset()
	if err := Render(&buf, pages, Options{Paper: doc.Settings.Paper}); err != nil {
		t.Fatal(err)
	}
	if stream := uncompressed(t, buf.Bytes()); strings.Contains(stream, "Revision") || strings.Contains(stream, "(*)") {
		t.Errorf("unexpected revision marks:\n%s", stream)
	}
}

// uncompressed returns the content streams of a PDF written by Render.
func uncompressed(t *testing.T, pdf []byte) string {
	var out strings.Builder
	for _, part := range bytes.Split(pdf, []byte(">>\nstream\n"))[1:] {
		end := bytes.Index(part, []byte("\nendstream"))
		r, err := zlib.NewReader(bytes.NewReader(part[:end]))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		out.Write(data)
	}
	return out.String()
}
