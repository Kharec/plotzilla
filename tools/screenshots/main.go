// Command screenshots regenerates the README images in screenshots/ from the
// sample script "The Last Reel". Run it from the repository root with
// `make screenshots`, which builds the frontend and installs puppeteer-core
// first.
//
// It serves frontend/dist with a stand-in for the Wails bridge whose calls are
// answered by the real internal packages, so pagination, locked pages and
// revision sets on the images are real. capture.mjs drives Firefox through it
// (1440×900, device scale 2); the window corners are then masked, since the
// browser cannot capture a transparent background. pdf-output.png is the
// PDF export of the script, rendered by pdftoppm and laid out on the app
// background.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"plotzilla/internal/fountain"
	"plotzilla/internal/layout"
	"plotzilla/internal/pdf"
	"plotzilla/internal/screenplay"
	"plotzilla/internal/spell"
)

const (
	dist   = "frontend/dist"
	sample = "tools/screenshots/the-last-reel.fountain"
)

// bridge replaces the Wails runtime in the page. Each call is posted to
// /api/<Method>, with the page's query string naming the script variant.
const bridge = `<script>
(() => {
  const call = (name) => async (...args) => {
    const r = await fetch('/api/' + name + location.search, { method: 'POST', body: JSON.stringify(args) })
    return r.json()
  }
  window.go = { main: { App: new Proxy({}, { get: (_, n) => call(n) }) } }
  window.runtime = { WindowSetTitle() {}, WindowMinimise() {}, WindowToggleMaximise() {},
    WindowIsMaximised: async () => false, EventsOn: () => () => {}, Quit() {} }
  // WebKitGTK does not spell-check the page; Firefox would.
  document.documentElement.setAttribute('spellcheck', 'false')
})()
</script>
<style>*{animation:none!important;transition:none!important;caret-color:transparent!important}</style>
`

// typos are the misspellings of the "typos" variant, with their
// corrections: spelling.png needs no Hunspell dictionary.
var typos = map[string][]string{"projecter": {"projector", "projected"}, "thraeds": {"threads"}, "wacth": {"watch"}}

// variant returns the sample script, or one of its variants: "typos" for the
// spelling image, "outline" (sections, synopses, a note) for the outline and
// revision images.
func variant(src, name string) string {
	var pairs []string
	switch name {
	case "typos":
		pairs = []string{
			"of the projector. ELODIE", "of the projecter. ELODIE",
			"threads a reel by hand", "thraeds a reel by hand",
			"Soldiers watch from", "Soldiers wacth from",
		}
	case "outline":
		pairs = []string{
			"FADE IN:\n", "# Act One\n\n= Elodie finds the reel her grandmother hid in 1942.\n\nFADE IN:\n",
			"of the projector. ELODIE", "of the projector. [[Warmer light here?]] ELODIE",
			"> PARIS, 1942 <\n", "> PARIS, 1942 <\n\n## The Rialto\n\n= The young woman smuggles the can into the cinema.\n",
			"MATCH CUT TO:\n", "MATCH CUT TO:\n\n# Act Two\n",
		}
	}
	for i := 0; i < len(pairs); i += 2 {
		src = strings.Replace(src, pairs[i], pairs[i+1], 1)
	}
	return src
}

type server struct {
	src  string // the sample script
	path string // its absolute path, shown as the file name
}

func (s *server) call(name, v string, raw []json.RawMessage) any {
	doc := func() *screenplay.Document {
		var d screenplay.Document
		if len(raw) > 0 {
			json.Unmarshal(raw[0], &d)
		}
		return &d
	}
	switch name {
	case "NewDocument":
		return screenplay.New()
	case "InitialFile":
		return map[string]any{"path": s.path, "document": fountain.Parse(variant(s.src, v))}
	case "Save", "SaveAs":
		return s.path // nothing is written
	case "Paginate":
		res := layout.Paginate(doc(), layout.Options{})
		return map[string]any{"pages": len(res.Pages), "breaks": res.Breaks, "scenes": res.Scenes}
	case "ParseFountain":
		var text string
		json.Unmarshal(raw[0], &text)
		return fountain.ParseText(text)
	case "RevisionSet":
		var n int
		json.Unmarshal(raw[0], &n)
		r := screenplay.RevisionSet(n)
		r.Date = []string{"09/14/26", "09/28/26"}[min(max(n, 1), 2)-1]
		return r
	case "WindowFrame":
		return map[string]any{"buttons": "appmenu:close", "rounded": true}
	case "SpellLanguages":
		return map[string]any{"languages": []string{"en_US"}, "default": "en_US"}
	case "SpellCheck":
		out := []spell.Misspelling{}
		for i, el := range doc().Elements {
			for _, w := range spell.Words(el.Text()) {
				if _, ok := typos[w.Text]; ok {
					out = append(out, spell.Misspelling{Element: i, Offset: w.Offset, Length: w.Length, Word: w.Text})
				}
			}
		}
		return out
	case "SpellSuggest":
		var word string
		json.Unmarshal(raw[0], &word)
		return typos[word]
	}
	return nil // Recovery, SetDirty…
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if name, ok := strings.CutPrefix(r.URL.Path, "/api/"); ok {
		var raw []json.RawMessage
		json.NewDecoder(r.Body).Decode(&raw)
		json.NewEncoder(w).Encode(s.call(name, r.URL.Query().Get("v"), raw))
		return
	}
	if r.URL.Path == "/" {
		html, err := os.ReadFile(filepath.Join(dist, "index.html"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, strings.Replace(string(html), `<script type="module"`, bridge+`<script type="module"`, 1))
		return
	}
	http.ServeFile(w, r, filepath.Join(dist, filepath.Clean(r.URL.Path)))
}

func main() {
	out := flag.String("out", "screenshots", "directory of the README images")
	firefox := flag.String("firefox", envOr("FIREFOX", "firefox"), "Firefox executable")
	flag.Parse()
	log.SetFlags(0)

	src, err := os.ReadFile(sample)
	if err != nil {
		log.Fatalf("run from the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		log.Fatal("build the frontend first (make frontend)")
	}
	browser, err := exec.LookPath(*firefox)
	if err != nil {
		log.Fatalf("Firefox not found: %v", err)
	}
	abs, _ := filepath.Abs(sample)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	go http.Serve(ln, &server{src: string(src), path: abs})

	tmp, err := os.MkdirTemp("", "plotzilla-screenshots-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	raw := filepath.Join(tmp, "raw")
	os.MkdirAll(raw, 0o755)

	cmd := exec.Command("node", "tools/screenshots/capture.mjs")
	cmd.Env = append(os.Environ(), "URL=http://"+ln.Addr().String()+"/", "OUT="+raw,
		"PROFILE="+filepath.Join(tmp, "profile"), "FIREFOX="+browser)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("capture: %v", err)
	}

	shots, _ := filepath.Glob(filepath.Join(raw, "*.png"))
	for _, f := range shots {
		img, err := readPNG(f)
		if err != nil {
			log.Fatal(err)
		}
		if err := writePNG(filepath.Join(*out, filepath.Base(f)), roundCorners(img, 24)); err != nil {
			log.Fatal(err)
		}
	}
	if err := pdfOutput(string(src), tmp, filepath.Join(*out, "pdf-output.png")); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d screenshots and pdf-output.png written to %s\n", len(shots), *out)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func writePNG(path string, img image.Image) error {
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, img); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// roundCorners makes the corners outside a rounded rectangle transparent:
// the window's 12 px corners, at device scale 2.
func roundCorners(src image.Image, radius float64) *image.NRGBA {
	b := src.Bounds()
	img := image.NewNRGBA(b)
	draw.Draw(img, b, src, b.Min, draw.Src)
	w, h := float64(b.Dx()), float64(b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			// Distance from the centre of the nearest corner circle.
			cx := math.Min(math.Max(float64(x)+0.5, radius), w-radius)
			cy := math.Min(math.Max(float64(y)+0.5, radius), h-radius)
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			if d <= radius-0.5 {
				continue
			}
			i := img.PixOffset(b.Min.X+x, b.Min.Y+y)
			img.Pix[i+3] = uint8(float64(img.Pix[i+3]) * math.Max(0, radius+0.5-d))
		}
	}
	return img
}

// pdfOutput renders the title page and the script pages of the sample
// side by side on the app background.
func pdfOutput(src, tmp, path string) error {
	doc := fountain.Parse(src)
	pages := append([]layout.Page{layout.TitlePage(doc.TitlePage, doc.Settings.Paper)},
		layout.Paginate(doc, layout.Options{SceneNumbers: true}).Pages...)
	var b bytes.Buffer
	if _, err := pdf.Render(&b, pages, pdf.Options{Title: doc.TitlePage.Title, Author: doc.TitlePage.Author}); err != nil {
		return err
	}
	file := filepath.Join(tmp, "sample.pdf")
	if err := os.WriteFile(file, b.Bytes(), 0o644); err != nil {
		return err
	}
	prefix := filepath.Join(tmp, "page")
	if out, err := exec.Command("pdftoppm", "-png", "-r", "110", file, prefix).CombinedOutput(); err != nil {
		return fmt.Errorf("pdftoppm (poppler) is needed for pdf-output.png: %v %s", err, out)
	}
	files, _ := filepath.Glob(prefix + "-*.png")
	const margin = 60
	var imgs []image.Image
	for _, f := range files {
		img, err := readPNG(f)
		if err != nil {
			return err
		}
		imgs = append(imgs, img)
	}
	if len(imgs) == 0 {
		return fmt.Errorf("pdftoppm produced no page")
	}
	pw, ph := imgs[0].Bounds().Dx(), imgs[0].Bounds().Dy()
	canvas := image.NewNRGBA(image.Rect(0, 0, margin+len(imgs)*(pw+margin), ph+2*margin))
	bg := color.NRGBA{7, 12, 24, 255} // --bg
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	for i, img := range imgs {
		r := image.Rect(margin+i*(pw+margin), margin, margin+i*(pw+margin)+pw, margin+ph)
		shadow(canvas, r, bg)
		draw.Draw(canvas, r, img, img.Bounds().Min, draw.Src)
	}
	return writePNG(path, canvas)
}

// shadow darkens the background around a page, fading over 24 px.
func shadow(img *image.NRGBA, r image.Rectangle, bg color.NRGBA) {
	const spread = 24
	for y := r.Min.Y - spread; y < r.Max.Y+spread; y++ {
		for x := r.Min.X - spread; x < r.Max.X+spread; x++ {
			if !(image.Point{x, y}).In(img.Bounds()) || (image.Point{x, y}).In(r) {
				continue
			}
			dx := max(r.Min.X-x, 0, x-r.Max.X+1)
			dy := max(r.Min.Y-y, 0, y-r.Max.Y+1)
			k := 1 - 0.3*(1-math.Min(math.Hypot(float64(dx), float64(dy))/spread, 1))
			img.SetNRGBA(x, y, color.NRGBA{uint8(float64(bg.R) * k), uint8(float64(bg.G) * k), uint8(float64(bg.B) * k), 255})
		}
	}
}
