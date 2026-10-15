package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"plotzilla/internal/fdx"
	"plotzilla/internal/fountain"
	"plotzilla/internal/layout"
	"plotzilla/internal/pdf"
	"plotzilla/internal/screenplay"
	"plotzilla/internal/spell"
)

// App is bound to the frontend: every exported method is callable from JS.
type App struct {
	ctx context.Context
	// dirty decides whether closing the window asks for confirmation. It is
	// written from Wails call goroutines (SetDirty) and read by the GTK
	// close handler: atomic, like spellMu protects the checker.
	dirty       atomic.Bool
	initialPath string

	// Wails runs each call on its own goroutine: spelling calls share one
	// loaded dictionary. dicts caches them per language so the translation's
	// language detection does not read the same files again.
	spellMu     sync.Mutex
	checker     *spell.Checker
	checkerLang string
	dicts       map[string]*spell.Dictionary

	tr translation
}

type OpenResult struct {
	Path     string               `json:"path"`
	Document *screenplay.Document `json:"document"`
}

type LayoutInfo struct {
	Pages  int                `json:"pages"`
	Breaks []layout.Break     `json:"breaks"`
	Scenes []layout.ScenePage `json:"scenes"`
}

type SpellInfo struct {
	Languages []string `json:"languages"`
	Default   string   `json:"default"`
}

type ExportOptions struct {
	SceneNumbers bool `json:"sceneNumbers"`
	TitlePage    bool `json:"titlePage"`
	// Revisions prints revision headers and asterisks.
	Revisions bool `json:"revisions"`
}

var scriptFilters = []runtime.FileFilter{
	{DisplayName: "Screenplays (*.fountain, *.fdx)", Pattern: "*.fountain;*.fdx;*.spmd;*.txt"},
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	followSystemColorScheme()
}

// beforeClose keeps the window open while there are unsaved changes and
// lets the frontend ask; it quits again once the user has decided.
func (a *App) beforeClose(ctx context.Context) bool {
	if !a.dirty.Load() {
		return false
	}
	runtime.EventsEmit(a.ctx, "confirm-close")
	return true
}

func (a *App) NewDocument() *screenplay.Document { return screenplay.New() }

// InitialFile returns the script passed on the command line, if any.
func (a *App) InitialFile() (*OpenResult, error) {
	if a.initialPath == "" {
		return nil, nil
	}
	path := a.initialPath
	a.initialPath = ""
	return a.load(path)
}

func (a *App) Open() (*OpenResult, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "Open Screenplay", Filters: scriptFilters})
	if err != nil || path == "" {
		return nil, err
	}
	return a.load(path)
}

func (a *App) load(path string) (*OpenResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc *screenplay.Document
	if strings.EqualFold(filepath.Ext(path), ".fdx") {
		if doc, err = fdx.Read(bytes.NewReader(data)); err != nil {
			return nil, err
		}
	} else {
		doc = fountain.Parse(string(data))
	}
	return &OpenResult{Path: path, Document: doc}, nil
}

// Save writes the document to path, asking for one when empty. It returns
// the path written, or "" if the user cancelled.
func (a *App) Save(doc screenplay.Document, path string) (string, error) {
	if path == "" {
		return a.SaveAs(doc, "")
	}
	var buf bytes.Buffer
	if strings.EqualFold(filepath.Ext(path), ".fdx") {
		if err := fdx.Write(&buf, &doc); err != nil {
			return "", err
		}
	} else {
		buf.WriteString(fountain.Write(&doc))
	}
	// The dirty flag is owned by the frontend (SetDirty): an autosave may
	// finish after newer edits were made.
	if err := writeAtomic(path, buf.Bytes()); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) SaveAs(doc screenplay.Document, current string) (string, error) {
	path, err := a.saveDialog("Save Screenplay", current, ".fountain", scriptFilters)
	if err != nil || path == "" {
		return "", err
	}
	return a.Save(doc, path)
}

// ExportResult describes a written PDF export.
type ExportResult struct {
	Path string `json:"path"` // "" when the user cancelled the dialog
	// Replaced are the characters the standard PDF fonts cannot draw:
	// each one is written as "?" in the file.
	Replaced []string `json:"replaced,omitempty"`
}

func (a *App) ExportPDF(doc screenplay.Document, opts ExportOptions, current string) (ExportResult, error) {
	path, err := a.saveDialog("Export PDF", current, ".pdf",
		[]runtime.FileFilter{{DisplayName: "PDF (*.pdf)", Pattern: "*.pdf"}})
	if err != nil || path == "" {
		return ExportResult{}, err
	}
	var pages []layout.Page
	if opts.TitlePage && !doc.TitlePage.IsEmpty() {
		pages = append(pages, layout.TitlePage(doc.TitlePage, doc.Settings.Paper))
	}
	pages = append(pages, layout.Paginate(&doc, layout.Options{SceneNumbers: opts.SceneNumbers}).Pages...)
	pdfOpts := pdf.Options{Title: doc.TitlePage.Title, Author: doc.TitlePage.Author, Paper: doc.Settings.Paper}
	if opts.Revisions {
		pdfOpts.Revisions = doc.Settings.Revisions
	}
	var buf bytes.Buffer
	replaced, err := pdf.Render(&buf, pages, pdfOpts)
	if err != nil {
		return ExportResult{}, err
	}
	if err := writeAtomic(path, buf.Bytes()); err != nil {
		return ExportResult{}, err
	}
	res := ExportResult{Path: path}
	for _, r := range replaced {
		res.Replaced = append(res.Replaced, string(r))
	}
	return res, nil
}

func (a *App) ExportFDX(doc screenplay.Document, current string) (string, error) {
	path, err := a.saveDialog("Export to Final Draft", current, ".fdx",
		[]runtime.FileFilter{{DisplayName: "Final Draft (*.fdx)", Pattern: "*.fdx"}})
	if err != nil || path == "" {
		return "", err
	}
	var buf bytes.Buffer
	if err := fdx.Write(&buf, &doc); err != nil {
		return "", err
	}
	return path, writeAtomic(path, buf.Bytes())
}

// Paginate returns where pages break so the editor can draw them live.
func (a *App) Paginate(doc screenplay.Document) LayoutInfo {
	res := layout.Paginate(&doc, layout.Options{})
	return LayoutInfo{Pages: len(res.Pages), Breaks: res.Breaks, Scenes: res.Scenes}
}

// ParseFountain converts pasted plain text into elements.
func (a *App) ParseFountain(text string) []screenplay.Element {
	return fountain.ParseText(text)
}

// RevisionSet returns revision set n (from 1), dated today.
func (a *App) RevisionSet(n int) screenplay.Revision {
	r := screenplay.RevisionSet(n)
	r.Date = time.Now().Format("01/02/06")
	return r
}

func (a *App) SetDirty(dirty bool) { a.dirty.Store(dirty) }

// dataDir is the XDG data directory (~/.local/share).
func dataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// recoveryPath is where untitled scripts are autosaved.
func recoveryPath() string { return filepath.Join(dataDir(), "plotzilla", "recovery.fountain") }

// personalDictionaryPath holds the words added with "Add to Dictionary",
// one per line, shared by every language.
func personalDictionaryPath() string { return filepath.Join(dataDir(), "plotzilla", "dictionary.txt") }

// SaveRecovery autosaves a script that has no file yet.
func (a *App) SaveRecovery(doc screenplay.Document) error {
	path := recoveryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, []byte(fountain.Write(&doc)))
}

func (a *App) ClearRecovery() { _ = os.Remove(recoveryPath()) }

// Recovery returns the autosaved untitled script left by a previous
// session, or nil.
func (a *App) Recovery() (*OpenResult, error) {
	path := recoveryPath()
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	res, err := a.load(path)
	if err != nil {
		return nil, err
	}
	res.Path = ""
	return res, nil
}

// dictionaryDirs lists where Hunspell dictionaries are installed, in the
// order Hunspell itself searches them.
func dictionaryDirs() []string {
	var dirs []string
	if p := os.Getenv("DICPATH"); p != "" {
		dirs = append(dirs, filepath.SplitList(p)...)
	}
	return append(dirs,
		filepath.Join(dataDir(), "hunspell"),
		"/usr/local/share/hunspell",
		"/usr/share/hunspell",
		"/usr/share/myspell",
		"/usr/share/myspell/dicts",
	)
}

// SpellLanguages lists the installed dictionaries and the one matching
// the system locale.
func (a *App) SpellLanguages() SpellInfo {
	codes := []string{}
	for _, l := range spell.Available(dictionaryDirs()) {
		codes = append(codes, l.Code)
	}
	locale := os.Getenv("LC_ALL")
	if locale == "" {
		locale = os.Getenv("LANG")
	}
	return SpellInfo{Languages: codes, Default: spell.Preferred(codes, locale)}
}

// dictionaryLocked returns the Hunspell dictionary for a language code
// ("en_US"), loading it the first time. spellMu must be held.
func (a *App) dictionaryLocked(code string) (*spell.Dictionary, error) {
	if d, ok := a.dicts[code]; ok {
		return d, nil
	}
	langs := spell.Available(dictionaryDirs())
	i := slices.IndexFunc(langs, func(l spell.Language) bool { return l.Code == code })
	if i < 0 {
		return nil, fmt.Errorf("no %s dictionary installed", code)
	}
	d, err := spell.Load(langs[i].Aff, langs[i].Dic)
	if err != nil {
		return nil, fmt.Errorf("loading the %s dictionary: %w", code, err)
	}
	if a.dicts == nil {
		a.dicts = map[string]*spell.Dictionary{}
	}
	a.dicts[code] = d
	return d, nil
}

// spellChecker returns the checker for lang, loading its dictionary the
// first time it is asked for.
func (a *App) spellChecker(lang string) (*spell.Checker, error) {
	a.spellMu.Lock()
	defer a.spellMu.Unlock()
	if a.checker != nil && a.checkerLang == lang {
		return a.checker, nil
	}
	dict, err := a.dictionaryLocked(lang)
	if err != nil {
		return nil, err
	}
	var personal []string
	if data, err := os.ReadFile(personalDictionaryPath()); err == nil {
		personal = strings.Fields(string(data))
	}
	a.checker, a.checkerLang = spell.NewChecker(dict, personal), lang
	return a.checker, nil
}

// SpellCheck returns the misspelled words of the script.
func (a *App) SpellCheck(doc screenplay.Document, lang string) ([]spell.Misspelling, error) {
	c, err := a.spellChecker(lang)
	if err != nil {
		return nil, err
	}
	return c.Misspellings(&doc), nil
}

func (a *App) SpellSuggest(word, lang string) ([]string, error) {
	c, err := a.spellChecker(lang)
	if err != nil {
		return nil, err
	}
	return c.Suggest(word), nil
}

// AddToDictionary accepts word in every language from now on.
func (a *App) AddToDictionary(word string) error {
	word = strings.TrimSpace(word)
	if word == "" || strings.ContainsAny(word, " \t\n") {
		return nil
	}
	a.spellMu.Lock()
	defer a.spellMu.Unlock()
	path := personalDictionaryPath()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading the personal dictionary: %w", err)
	}
	words := strings.Fields(string(data))
	if !slices.Contains(words, word) {
		words = append(words, word)
		slices.Sort(words)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeAtomic(path, []byte(strings.Join(words, "\n")+"\n")); err != nil {
			return fmt.Errorf("saving the personal dictionary: %w", err)
		}
	}
	if a.checker != nil {
		a.checker.Add(word)
	}
	return nil
}

// WindowFrame describes how the in-app header bar should draw the window.
type WindowFrame struct {
	// Buttons is the desktop's title bar button layout in GTK's
	// gtk-decoration-layout format, e.g. "appmenu:minimize,maximize,close".
	Buttons string `json:"buttons"`
	// Rounded is false when no compositor gives the window transparency.
	Rounded bool `json:"rounded"`
}

// WindowFrame returns the title bar button layout that GTK windows use on
// this desktop (GNOME, Plasma, Xfce…) so the in-app header bar can place its
// window buttons like every other window, and whether rounded corners can
// be drawn.
func (a *App) WindowFrame() WindowFrame { return systemWindowFrame() }

func (a *App) saveDialog(title, current, ext string, filters []runtime.FileFilter) (string, error) {
	dir, name := "", "untitled"+ext
	if current != "" {
		dir = filepath.Dir(current)
		name = strings.TrimSuffix(filepath.Base(current), filepath.Ext(current)) + ext
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: title, DefaultDirectory: dir, DefaultFilename: name, Filters: filters,
	})
	if err != nil || path == "" {
		return "", err
	}
	// The GTK dialog does not append the extension on its own.
	if filepath.Ext(path) == "" {
		path += ext
	}
	return path, nil
}

// writeAtomic writes data to path atomically and durably: a temporary file
// in the same directory is written, given the file's permissions and synced
// to disk, then renamed over path and the directory itself synced, so a
// power cut just after a save cannot leave an empty script behind (the very
// failure autosave, which calls this every 1.2 s, exists to prevent). A
// symlinked path is resolved first: the file it points to is replaced, not
// the link. An existing file keeps its permissions; a new one gets 0o644.
func writeAtomic(path string, data []byte) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	// The rename is a directory entry: sync the directory so it too
	// survives a crash.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
