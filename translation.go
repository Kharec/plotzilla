package main

import (
	"context"
	"errors"
	"fmt"
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
	// stallTimeout is how long a download may go without receiving a byte
	// (or its response headers) before it is aborted as stalled.
	stallTimeout = 30 * time.Second
)

// errStalled aborts a download that receives nothing for stallTimeout.
var errStalled = fmt.Errorf("the download stalled (no data received for %s)", stallTimeout)

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
	done    chan struct{} // closed when the running task is over
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

// installedModel returns the first model directory holding every file at
// its pinned size. Checking the digests would mean reading 300 MB on
// every call; the sizes catch truncated or stale files, and the digests
// were checked when the files were downloaded.
func installedModel() string {
	for _, d := range modelDirs() {
		if modelComplete(d) {
			return d
		}
	}
	return ""
}

func modelComplete(dir string) bool {
	for _, f := range modelFiles {
		st, err := os.Stat(filepath.Join(dir, f.Name))
		if err != nil || st.Size() != f.Size {
			return false
		}
	}
	return true
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
		return nil, errors.New("a translation task is already running")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.tr.cancel = cancel
	a.tr.done = make(chan struct{})
	return ctx, nil
}

func (a *App) endTask() {
	a.tr.mu.Lock()
	defer a.tr.mu.Unlock()
	if a.tr.cancel != nil {
		a.tr.cancel()
		a.tr.cancel = nil
		close(a.tr.done)
		a.tr.done = nil
	}
}

// CancelTranslation stops the running download or translation, waiting
// for it to wind down so that a new task can start right away.
func (a *App) CancelTranslation() {
	a.tr.mu.Lock()
	cancel, done := a.tr.cancel, a.tr.done
	a.tr.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
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
// directory. An interrupted download resumes where it stopped. It returns
// false if the user cancelled.
func (a *App) DownloadTranslationModel() (bool, error) {
	if installedModel() != "" {
		return true, nil
	}
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
	// No total timeout (300 MB can take a while) but abort a stalled
	// transfer: the watchdog is reset by every progress report.
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	emit := a.progressEmitter()
	stalled := time.AfterFunc(stallTimeout, func() { stop(errStalled) })
	defer stalled.Stop()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = stallTimeout
	client := &http.Client{Transport: transport}
	err = marian.Download(ctx, client, modelDirs()[0], files, func(done, total int64) {
		stalled.Reset(stallTimeout)
		emit(done, total)
	})
	if err != nil {
		if errors.Is(context.Cause(ctx), errStalled) {
			return false, errStalled
		}
		if errors.Is(err, context.Canceled) {
			return false, nil
		}
		return false, err
	}
	return true, nil
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
	if installedModel() == "" {
		return nil, errors.New("the translation model is not installed")
	}
	wails.EventsEmit(a.ctx, "translation-progress", 0, 0) // loading
	model, lexicon, err := a.translator(ctx)
	if errors.Is(err, context.Canceled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer a.releaseLater()
	emit := a.progressEmitter()
	res, err := translate.Document(ctx, &doc, model, lexicon, translate.Options{
		Elements:  elements,
		TitlePage: titlePage,
		Workers:   translateWorkers(),
		Progress:  func(done, total int) { emit(int64(done), int64(total)) },
	})
	if errors.Is(err, context.Canceled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// translateWorkers is how many sentences are translated at once: half the
// CPUs, and each one splits its decoding steps across its share of them.
func translateWorkers() int { return max(1, runtime.NumCPU()/2) }

// translator returns the model and the dictionaries that help tell French
// from English, loading them the first time. The load happens without
// a.tr.mu: reading 300 MB takes seconds, and CancelTranslation must stay
// responsive.
func (a *App) translator(ctx context.Context) (*marian.Model, translate.Lexicon, error) {
	a.tr.mu.Lock()
	if a.tr.release != nil {
		a.tr.release.Stop()
	}
	model, lexicon := a.tr.model, a.tr.lexicon
	a.tr.mu.Unlock()
	if model != nil {
		return model, lexicon, nil
	}
	dir := installedModel()
	if dir == "" {
		return nil, nil, errors.New("the translation model is not installed")
	}
	m, err := marian.Load(ctx, dir)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("loading the translation model: %w", err)
	}
	// Every sentence worker gets an equal share of the CPUs for its
	// decoding steps, so workers × threads never oversubscribe the machine.
	m.SetThreads(max(1, runtime.NumCPU()/translateWorkers()))
	lexicon = a.loadLexicon()
	a.tr.mu.Lock()
	if ctx.Err() == nil { // cancelled while loading: cache nothing
		a.tr.model, a.tr.lexicon = m, lexicon
	}
	a.tr.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return m, lexicon, nil
}

// releaseLater frees the model once it has been unused for modelIdle.
func (a *App) releaseLater() {
	a.tr.mu.Lock()
	defer a.tr.mu.Unlock()
	a.tr.release = time.AfterFunc(modelIdle, a.releaseModel)
}

// releaseModel frees the model when no task is running. When one is (a
// download loads nothing but holds the task slot), it tries again after
// another modelIdle.
func (a *App) releaseModel() {
	a.tr.mu.Lock()
	defer a.tr.mu.Unlock()
	if a.tr.cancel != nil {
		a.tr.release = time.AfterFunc(modelIdle, a.releaseModel)
		return
	}
	a.tr.model, a.tr.lexicon = nil, nil
}

// lexicon tells French words from English ones with the installed
// Hunspell dictionaries.
type lexicon struct{ fr, en *spell.Dictionary }

func (l lexicon) Knows(word string) (french, english bool) { return l.fr.Check(word), l.en.Check(word) }

// loadLexicon needs both a French and an English dictionary: with one
// only, every word it knows would count for its language. Without them,
// detection relies on function words and accents. The dictionaries come
// from the same cache as the spell checker's: a Dictionary is read-only
// once loaded, so it can be shared.
func (a *App) loadLexicon() translate.Lexicon {
	langs := spell.Available(dictionaryDirs())
	pick := func(lang, preferred string) string {
		if i := slices.IndexFunc(langs, func(l spell.Language) bool { return l.Code == preferred }); i >= 0 {
			return langs[i].Code
		}
		if i := slices.IndexFunc(langs, func(l spell.Language) bool { return strings.HasPrefix(l.Code, lang+"_") }); i >= 0 {
			return langs[i].Code
		}
		return ""
	}
	frCode, enCode := pick("fr", "fr_FR"), pick("en", "en_US")
	if frCode == "" || enCode == "" {
		return nil
	}
	a.spellMu.Lock()
	defer a.spellMu.Unlock()
	fr, err := a.dictionaryLocked(frCode)
	if err != nil {
		return nil
	}
	en, err := a.dictionaryLocked(enCode)
	if err != nil {
		return nil
	}
	return lexicon{fr, en}
}
