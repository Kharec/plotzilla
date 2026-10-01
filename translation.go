package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	wails "github.com/wailsapp/wails/v2/pkg/runtime"

	"plotzilla/internal/marian"
	"plotzilla/internal/screenplay"
	"plotzilla/internal/spell"
	"plotzilla/internal/translate"
)

// The French → English model is OPUS-MT fr-en (University of Helsinki,
// Apache-2.0), pinned to one revision so that every file can be checked.
const (
	modelName = "opus-mt-fr-en"
	modelURL  = "https://huggingface.co/Helsinki-NLP/opus-mt-fr-en/resolve/c4aed37b318c763fd177aa449b44e3b783cc6c02/"
	// modelIdle is how long the model (300 MB of memory) stays loaded
	// after a translation, ready for the next one.
	modelIdle = 5 * time.Minute
)

var modelFiles = []marian.RemoteFile{
	{Name: "config.json", Size: 1416, SHA256: "b3be13d046d9899d7aab8cf4ed624d9a79f5776038ba793f6b4d2ce3e02192f7"},
	{Name: "source.spm", Size: 802397, SHA256: "78d0e717c77053f1c4b856d8661d9cb87c64f083a35418c087b9146300e4f585"},
	{Name: "target.spm", Size: 778395, SHA256: "173e9f493a668fe396d599e28d414a201193094e6ffd7a4678e5aab0f6d3d838"},
	{Name: "vocab.json", Size: 1339166, SHA256: "945c604346ce15ce4aff9001001e7f925e336d942c4087017f191871162cbdc4"},
	{Name: "model.safetensors", Size: 300803608, SHA256: "6e3837f34b903802c3d0d670362b997cee6e87584a1108eb3fa89e4625e4424a"},
}

// translation is the state of the translation feature: the loaded model,
// and the running task (one at a time), which the user can cancel.
type translation struct {
	mu      sync.Mutex
	model   *marian.Model
	lexicon translate.Lexicon
	release *time.Timer
	cancel  context.CancelFunc
}

// TranslationStatus says whether the model is installed, and how much a
// download weighs.
type TranslationStatus struct {
	Installed bool  `json:"installed"`
	Size      int64 `json:"size"`
}

// modelDirs lists where the model is looked for: the user's data
// directory (where it is downloaded), then the system data directories,
// where a distribution package may install it.
func modelDirs() []string {
	dirs := []string{filepath.Join(dataDir(), "plotzilla", "models", modelName)}
	system := os.Getenv("XDG_DATA_DIRS")
	if system == "" {
		system = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(system) {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, "plotzilla", "models", modelName))
		}
	}
	return dirs
}

func installedModel() string {
	for _, d := range modelDirs() {
		if marian.Installed(d) {
			return d
		}
	}
	return ""
}

func (a *App) TranslationStatus() TranslationStatus {
	var size int64
	for _, f := range modelFiles {
		size += f.Size
	}
	return TranslationStatus{Installed: installedModel() != "", Size: size}
}

// startTask registers a cancellable task; only one runs at a time.
func (a *App) startTask() (context.Context, error) {
	a.tr.mu.Lock()
	defer a.tr.mu.Unlock()
	if a.tr.cancel != nil {
		return nil, errors.New("a translation is already running")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.tr.cancel = cancel
	return ctx, nil
}

func (a *App) endTask() {
	a.tr.mu.Lock()
	defer a.tr.mu.Unlock()
	if a.tr.cancel != nil {
		a.tr.cancel()
		a.tr.cancel = nil
	}
}

// CancelTranslation stops the running download or translation.
func (a *App) CancelTranslation() {
	a.tr.mu.Lock()
	defer a.tr.mu.Unlock()
	if a.tr.cancel != nil {
		a.tr.cancel()
	}
}

// progressEmitter sends "translation-progress" events, at most every
// 100 ms: a download reports every 32 KB.
func (a *App) progressEmitter() func(done, total int64) {
	var mu sync.Mutex
	var last time.Time
	return func(done, total int64) {
		mu.Lock()
		defer mu.Unlock()
		if done < total && time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		wails.EventsEmit(a.ctx, "translation-progress", done, total)
	}
}

// DownloadTranslationModel downloads the model into the user's data
// directory. It returns false if the user cancelled.
func (a *App) DownloadTranslationModel() (bool, error) {
	ctx, err := a.startTask()
	if err != nil {
		return false, err
	}
	defer a.endTask()
	files := make([]marian.RemoteFile, len(modelFiles))
	for i, f := range modelFiles {
		f.URL = modelURL + f.Name
		files[i] = f
	}
	err = marian.Download(ctx, &http.Client{}, modelDirs()[0], files, a.progressEmitter())
	if errors.Is(err, context.Canceled) {
		return false, nil
	}
	return err == nil, err
}

// Translate translates the French text of the elements (flat indexes; all
// of them when nil) and, if titlePage, of the title page. It returns nil
// if the user cancelled.
func (a *App) Translate(doc screenplay.Document, elements []int, titlePage bool) (*translate.Result, error) {
	ctx, err := a.startTask()
	if err != nil {
		return nil, err
	}
	defer a.endTask()
	wails.EventsEmit(a.ctx, "translation-progress", 0, 0) // loading
	model, lexicon, err := a.translator()
	if err != nil {
		return nil, err
	}
	defer a.releaseLater()
	emit := a.progressEmitter()
	res, err := translate.Document(ctx, &doc, model, lexicon, translate.Options{
		Elements:  elements,
		TitlePage: titlePage,
		// The model splits its largest product across every CPU already:
		// half as many sentences at once keeps the machine responsive.
		Workers:  max(1, runtime.NumCPU()/2),
		Progress: func(done, total int) { emit(int64(done), int64(total)) },
	})
	if errors.Is(err, context.Canceled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// translator returns the model and the dictionaries that help tell French
// from English, loading them the first time.
func (a *App) translator() (*marian.Model, translate.Lexicon, error) {
	a.tr.mu.Lock()
	defer a.tr.mu.Unlock()
	if a.tr.release != nil {
		a.tr.release.Stop()
	}
	if a.tr.model == nil {
		dir := installedModel()
		if dir == "" {
			return nil, nil, errors.New("the translation model is not installed")
		}
		m, err := marian.Load(dir)
		if err != nil {
			return nil, nil, err
		}
		a.tr.model = m
		a.tr.lexicon = loadLexicon()
	}
	return a.tr.model, a.tr.lexicon, nil
}

func (a *App) releaseLater() {
	a.tr.mu.Lock()
	defer a.tr.mu.Unlock()
	a.tr.release = time.AfterFunc(modelIdle, func() {
		a.tr.mu.Lock()
		defer a.tr.mu.Unlock()
		if a.tr.cancel == nil {
			a.tr.model, a.tr.lexicon = nil, nil
		}
	})
}

// lexicon tells French words from English ones with the installed
// Hunspell dictionaries.
type lexicon struct{ fr, en *spell.Dictionary }

func (l lexicon) Knows(word string) (french, english bool) { return l.fr.Check(word), l.en.Check(word) }

// loadLexicon needs both a French and an English dictionary: with one
// only, every word it knows would count for its language. Without them,
// detection relies on function words and accents.
func loadLexicon() translate.Lexicon {
	langs := spell.Available(dictionaryDirs())
	load := func(lang, preferred string) *spell.Dictionary {
		i := slices.IndexFunc(langs, func(l spell.Language) bool { return l.Code == preferred })
		if i < 0 {
			i = slices.IndexFunc(langs, func(l spell.Language) bool { return strings.HasPrefix(l.Code, lang+"_") })
		}
		if i < 0 {
			return nil
		}
		d, err := spell.Load(langs[i].Aff, langs[i].Dic)
		if err != nil {
			return nil
		}
		return d
	}
	fr, en := load("fr", "fr_FR"), load("en", "en_US")
	if fr == nil || en == nil {
		return nil
	}
	return lexicon{fr, en}
}
