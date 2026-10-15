# AGENTS.md

Everything you need to start working on **Plotzilla**, a Linux screenwriting app (an Arc Studio Pro /
Final Draft alternative) that produces scripts in the Hollywood standard format. Read this file
first; `README.md` is the user-facing documentation.

## 1. At a glance

| | |
|---|---|
| Backend | Go ≥ 1.23, [Wails v2](https://wails.io) (GTK 3 + WebKitGTK 4.1) |
| Frontend | TypeScript 7, [ProseMirror](https://prosemirror.net), Vite 8, plain CSS, no UI framework |
| Target | Any Linux desktop (GNOME, Plasma, Xfce…; Wayland and X11), GNOME look (Arch dev machine) |
| Native format | Fountain (`.fountain`); also FDX import/export and PDF export |
| License | GPL-3.0 (`LICENSE`); new dependencies must be GPL-3.0 compatible |
| Language | **Everything user-facing is English**: UI, messages, dialogs, README, docs, comments, commit messages |

## 2. Commands

```sh
make            # npm install + vite build + go build → build/bin/plotzilla
make test       # go test ./internal/...
make screenshots # regenerate the README images (needs Firefox and pdftoppm)
make install    # binary + desktop/ files under PREFIX (/usr/local); make uninstall
make dev        # wails dev (live reload; needs `go install github.com/wailsapp/wails/v2/cmd/wails@latest`)
./build/bin/plotzilla path/to/script.fountain

cd frontend && npm run build     # tsc --noEmit (type check) + vite build → frontend/dist
go vet -tags desktop,production,webkit2_41 ./...
gofmt -l .                       # must print nothing
```

- The Go build **requires the tag `webkit2_41`** (the machine only ships WebKitGTK 4.1). Plain
  `go build .` fails; always go through `make` or pass `-tags desktop,production,webkit2_41`.
- `main.go` embeds `frontend/dist` (`//go:embed all:frontend/dist`). The frontend must be built
  before the Go binary. `frontend/public/.gitkeep` keeps `dist/` non-empty on a fresh clone.
- System deps (Arch): `go nodejs npm webkit2gtk-4.1 gtk3`. Optional font: `ttf-courier-prime`.
  Optional Hunspell dictionaries for spell checking: `hunspell-en_us`, `hunspell-fr`…

Definition of done for any change: `make` succeeds, `make test` passes, `gofmt -l .` is empty,
`npm run build` has no type errors, `npm audit` reports no vulnerabilities, README updated if
behaviour or shortcuts changed.

## 3. Repository map

```
main.go                 Wails bootstrap: frameless translucent window, LANGUAGE=en, CLI file argument
app.go                  App struct bound to JS: file I/O, dialogs, pagination, autosave recovery
translation.go          App methods for translation: model status/download (pinned files + SHA-256,
                        resumable), Translate, CancelTranslation, model loaded lazily and freed
                        after 5 min idle
desktop_linux.go        cgo: desktop light/dark preference for GTK dialogs, window button layout, compositing
desktop/                app icon (plotzilla.svg, source of truth; plotzilla.png = 256 px render embedded as
                        the X11 window icon), plotzilla.desktop, MIME types for .fountain/.fdx
packaging/arch/PKGBUILD Arch package: builds tag $pkgver, installs with `make install`
internal/screenplay     the model: Document{TitlePage, Elements, Settings}, Element{Type, Runs, SceneNumber,
                        Dual, Level, Locked}, Run{Text, b/i/u, Note, Rev}, revision set names
internal/fountain       Fountain parser (parse.go) and writer (write.go), incl. Plotzilla's boneyard data
internal/fdx            Final Draft XML reader/writer
internal/layout         line wrapping + pagination (paper sizes, locked pages); the single source of truth
                        for page geometry
internal/pdf            hand-written PDF 1.4 writer, standard Courier fonts, zero dependencies
internal/spell          Hunspell .aff/.dic reader, checker (compounds too), suggestions, word scan
internal/marian         MarianMT (OPUS-MT) inference in pure Go: safetensors, SentencePiece unigram,
                        transformer encoder-decoder, beam search, verified download
internal/translate      French → English script translation: language detection, screenplay term
                        tables, per-element rules, name protection, parallel sentence translation
frontend/index.html     app shell: header bar, navigator, paper, HUD, <dialog>s
frontend/src/main.ts    app controller: state, save/autosave, pagination loop, navigator, menus, shortcuts, window frame
frontend/src/schema.ts  ProseMirror schema + conversion PM doc ⇄ ScriptDocument JSON
frontend/src/commands.ts screenplay keymap (Enter/Tab/Ctrl+1…9), retype, dual dialogue, auto-format
frontend/src/revisions.ts revision marking and locked-page anchors (plugins), lock/unlock/clear
frontend/src/autocomplete.ts suggestion popup (characters, locations, times, extensions, shots, transitions)
frontend/src/decorations.ts live page-break widgets + empty-element placeholder
frontend/src/search.ts  find & replace: query, match highlights, next/previous, replace (all)
frontend/src/spellcheck.ts misspelling underlines (from Go), word under the cursor left alone
frontend/src/menu.ts    popup menus with keyboard navigation (spelling corrections, languages)
frontend/src/translate.ts elements covered by the selection, the transaction applying a translation
frontend/src/backend.ts typed wrappers for window.go.main.App and the Wails runtime
frontend/src/icons.ts   inline Lucide-style SVG icons
frontend/src/style.css  the whole UI theme (dark, blue) and the on-screen page geometry
screenshots/            README images (2880×1800 UI captures + pdf-output.png)
tools/screenshots/      `make screenshots`: sample script, mock server (main.go), Firefox capture (capture.mjs)
VERSION                 current version
CHANGELOG.md            Keep a Changelog; sections Added, Changed, Fixed only
```

## 4. Architecture

### Data flow

```
ProseMirror doc ──toScript()──▶ ScriptDocument JSON ──Wails──▶ screenplay.Document (Go)
       ▲                                                             │
       └────────fromScript()◀──── OpenResult ◀── fountain/fdx ◀──────┘
                                                                     ├─▶ layout.Paginate ─▶ breaks → editor widgets
                                                                     └─▶ layout.Paginate ─▶ pdf.Render
```

- The **Go model is canonical**. The frontend never parses or writes files itself; it sends the
  whole `ScriptDocument` to Go (`Save`, `Paginate`, `ExportPDF`, …). Documents are small enough
  for this to be cheap; pagination is debounced (300 ms).
- **Pagination lives only in Go** (`internal/layout`). The editor shows page breaks computed by
  that same code, so the screen and the PDF always agree. Never reimplement wrapping or page
  rules in TypeScript.

### The JSON contract (`backend.ts` ⇄ `internal/screenplay`)

`ScriptDocument = { titlePage, elements, settings? }`
`Element = { type, runs: [{text, b?, i?, u?, n?, rev?}] | null, sceneNumber?, dual?, level?, newPage?, locked? }`
`Settings = { paper?: '' | 'a4', revisions?: [{name, color, date}], revising?, locked? }`

- `runs` may be `null` from Go; always read it as `el.runs ?? []`. Every settings field may be
  missing (Go omits zero values).
- `n: true` marks a note run (Fountain `[[…]]`): part of `Text()` and of every offset, never
  printed (`Element.PrintedText()`, `printedText()` in the frontend). `rev: n` is the revision
  set the text belongs to (`settings.revisions[n-1]`).
- `newPage: true` forces a page break before the element (Fountain `===`, FDX
  `StartsNewPage`). On a dialogue part or a dual dialogue it breaks before the whole block;
  on an element that prints nothing it moves to the next printed one. `Ctrl+Enter`
  (`pageBreak` in commands.ts) sets it on the block's first element, never on a part.
- `level` is a section's depth (from 1). `locked: [{offset, page}]` lists the locked pages that
  start in the element (rune offsets, like `Break`); in the editor they live in the element's
  `locked` attribute as UTF-16 offsets (`runeOffset`/`utf16Offset` in schema.ts convert).
- A `"\n"` inside a run is a hard line break (ProseMirror `hard_break` node).
- `dual: true` on a **character** element means "this dialogue block sits to the right of the
  previous one". In the editor this is a nested `dual > column column > element+` structure.
- `Break.label` / `ScenePage.label` is the printed page number (`12`, `12A`, `12-13`); `page` is
  the physical page index. Show the label, compare with `page`.
- Element **indexes** used by `Break` and `ScenePage` count elements in flat document order,
  including empty ones and elements inside dual dialogues. Use `flatElements(doc)` in the
  frontend whenever you map an index to a position; never use `doc.child(i)`.
- `Break.offset` is a **rune** (code point) offset. JS strings are UTF-16: convert with
  `utf16Offset()` (decorations.ts). `Misspelling` (`element`, `offset`, `length`) follows the
  same rules.
- Any change to these structs must be made in both `internal/screenplay/screenplay.go` and
  `frontend/src/backend.ts`, in the same change.

### Element types

`scene, action, character, parenthetical, dialogue, transition, shot, intertitle, centered,
section, synopsis` (Ctrl+1…9 for the first nine, in that order; the order of `ELEMENT_TYPES` in
schema.ts defines the shortcuts). Sections and synopses are outline elements: not printed
(`ElementType.Printed()` / `isPrinted()`), typed with `# ` / `= ` (auto-format).

**Checklist to add or change an element type** (the compiler catches the `Record<ElementType,…>`
maps, but not the rest):

1. `internal/screenplay`: constant, `Uppercase()` / `InDialogue()` / `Printed()` if relevant.
2. `internal/layout`: `specs` (left inch, width chars, blank lines before), `x()` alignment,
   keep-with-next / splittable rules.
3. `internal/fountain`: parse rule and writer rule (Fountain has few types; document any lossy
   mapping in README "Formats").
4. `internal/fdx`: `typeNames` and the reader switch.
5. Tests in `fountain_test.go`, `fdx_test.go`, `layout_test.go`.
6. `frontend/src/backend.ts` union, `schema.ts` `ELEMENT_TYPES` + `LABELS`.
7. `commands.ts`: `NEXT_ON_ENTER`, `RETYPE_ON_EMPTY_ENTER`, `TAB`, `SHIFT_TAB`, auto-detection.
8. `autocomplete.ts` if it has suggestions; `style.css` `.el-<type>` + `--t-<type>` color.
9. README shortcut table and format table; tooltip in `index.html` if the Ctrl+N range changes.

### Screenplay format rules (do not break)

US Letter, Courier 12 pt = 10 chars/inch, 6 lines/inch, 54 body lines per page, 1" top/bottom,
1.5" left margin. Indents (inches from page edge / width in chars): scene & action 1.5/60,
character 3.7/38, parenthetical 3.1/25 with 1-char hanging indent, dialogue 2.5/35, transition
right-aligned at 7.5", dual columns at 1.5" and 4.5". Pagination: scene headings and inserts are
kept with the next two lines; action and dialogue split only at sentence ends with ≥ 2 lines on
each side; never end a page on a parenthetical; split dialogue gets `(MORE)` / `NAME (CONT'D)`;
dual dialogue is never split; title page and page 1 are unnumbered; sections, synopses, notes
and lines holding only a note are not printed.
A4 (`layout.PaperGeometry`) keeps every margin and width: the page is 8.27" wide and holds 58
lines; right scene numbers move from 7.75" to 7.6". Revision asterisks go at 8.1" (A4: 7.95"), a
revised page's header (`Pink Revision - 09/28/26`) at 1.5" on the page-number line; a page is
starred only for its latest revision.
Locked pages (`settings.locked` + `Element.Locked`): each locked page starts where its anchor
is; overflow makes A pages (`12` → `12A` → `12B`), a page left empty is merged into the previous
one (`12-13`), an anchor inside a dialogue block cuts it with `(MORE)` / `(CONT'D)`, an anchor
inside a dual dialogue starts the page above the whole block.
The editor CSS reproduces the same geometry with `in` and `ch` units (1ch = 0.1in), the page
width through `--page-width` (`.paper[data-paper='a4']`): **if you change a spec in `layout.go`,
change `style.css` too** (and vice versa).

### Frontend state and saving

- `app` in `main.ts` holds `path`, `dirty`, `revision`, `saving`, `draftSaved`, `titlePage`,
  `settings`, `layout`, preferences. The ProseMirror doc is the source of truth for the script
  body; the title page lives in `app.titlePage` and the production settings (paper, revision
  sets, marking, locking) in `app.settings`. Settings are not editor transactions: after
  changing one, call `settingsChanged()` (marks the script changed, repaginates).
- Revisions (`revisions.ts`): `revisionPlugin` marks, in an appended transaction, the ranges
  each change inserted (a deletion marks its neighbour); it skips undo/redo
  (`isHistoryTransaction`) so undo removes text and mark together. `lockPlugin` maps every
  locked-page anchor through each change and rewrites the `locked` attributes; lock, unlock and
  clearing marks are built by `lockPages`, `unlockPages`, `clearRevisionMarks`. Their own
  transactions carry the `INTERNAL` meta so the plugins never react to each other. Locking and
  unlocking are not undoable (`addToHistory: false`), like the setting they go with.
- The `revision` mark is not inclusive (typing next to revised text is not revised unless
  marking is on); the `note` mark is, like bold. Hard breaks carry note/revision marks only.
- **The frontend owns the dirty flag** and pushes it to Go with `SetDirty`. Go's `Save` must not
  reset it: an autosave can finish after newer edits (`revision` guards this).
- Every change goes through `markChanged()` (revision++, dirty, schedule autosave).
- Autosave (1.2 s idle): writes the real file when there is a path, otherwise
  `~/.local/share/plotzilla/recovery.fountain` (offered back at launch, removed on explicit discard
  or first save).
- Spelling is checked in Go (`SpellCheck`, 500 ms after the last change, stale results
  dropped like pagination); the frontend only draws underlines and menus. Dictionaries are the
  system's Hunspell files, loaded lazily per language; the personal dictionary is
  `~/.local/share/plotzilla/dictionary.txt`. Compound words are tried only when the word
  itself is unknown (`Dictionary.accepts`): parts need COMPOUNDFLAG or the BEGIN/MIDDLE/LAST
  flag of their position; the first part may take a prefix, the last a suffix, any other affix
  needs COMPOUNDPERMITFLAG in its continuation classes. Suggestions do not build compounds.
- Translation (`Ctrl+Shift+L`) runs in Go, offline: `internal/marian` runs the OPUS-MT fr-en
  model (Hugging Face form: `config.json`, `model.safetensors`, `source.spm`, `target.spm`,
  `vocab.json`), downloaded on demand into `~/.local/share/plotzilla/models/opus-mt-fr-en`
  (also looked up in `$XDG_DATA_DIRS/plotzilla/models/`). The download resumes the `.partial`
  files an interruption left, skips files already downloaded, and aborts when nothing arrives
  for 30 s; a directory only counts as installed when every file has its pinned size.
  `translate.Document` returns the
  changed elements (flat indexes) and the title page; the frontend replaces them in one
  transaction (`applyTranslation`), only if the doc is unchanged. Both tasks report progress
  with the `translation-progress` event (done, total; `0, 0` while loading) and stop on
  `CancelTranslation`, returning `nil`/`false` (not an error) when cancelled. The model loads
  without holding the translation lock (and `marian.Load` takes the context) so Cancel stays
  responsive during the seconds-long load; `CancelTranslation` waits for the task to wind down.
- Find & replace is frontend-only (`search.ts`): matches are computed on `blockText`, where one
  character is one document position. Replacements go through `replaceText()` (commands.ts),
  which keeps the marks, and each one is its own undo step (`closeHistory`).
- Closing: `OnBeforeClose` in Go returns "prevent" while dirty and emits `confirm-close`; the
  frontend asks with its own dialog and calls `runtime.Quit()` after `SetDirty(false)`.

### Window and desktop integration

- The window is **frameless and translucent**; the top bar is a GNOME-style header bar on every
  desktop (`--wails-draggable: drag`, every interactive child `no-drag`, double-click toggles
  maximise). `body` draws the 12 px rounded corners; `body.maximized` removes them, and so does
  `body.opaque` (no compositor, hence no alpha channel: rounded corners would show black
  wedges); `body.backdrop` dims the bar when unfocused.
- **Nothing may depend on GNOME being the desktop.** Read desktop settings through GTK or the
  XDG portal, never with `gsettings`/`org.gnome.*` alone (they hold GNOME's defaults, not the
  user's choice, on other desktops). `WindowFrame()` returns GTK's `gtk-decoration-layout`
  (each desktop feeds it: GNOME/Plasma settings, XSettings on Xfce/MATE/Cinnamon,
  `settings.ini`) and whether the window is really translucent; the frontend lays out the
  buttons from it and ignores entries it does not draw (`menu`, `appmenu`, `icon`, `spacer`).
  The light/dark preference for native dialogs comes from the portal's
  `org.freedesktop.appearance color-scheme`, with GNOME's `color-scheme` as the fallback when
  no portal answers; "no preference" leaves GTK's configured theme alone.
- **Never use Wails `runtime.MessageDialog` for questions**: on Linux it ignores custom buttons
  and shows GTK stock Yes/No/OK translated into the system language. Use `messageBox()` in
  `main.ts` (supports `destructive`).
- `main.go` sets `LANGUAGE=en` so GTK's own strings (file chooser) are English.
- `desktop_linux.go` must only touch GTK through `g_idle_add` (main loop); Wails callbacks run
  on other goroutines. `query_window_frame` shows how to get an answer back synchronously
  (refcounted request + `GCond` with a timeout); never call it from the main thread.

### Translation rules (`internal/translate`)

- Text is translated only when it reads as French (`scores`: function words, elisions,
  accents, then the Hunspell lexicon when both fr and en dictionaries exist). Ties follow the
  other sentences of the text, then the nearest decided element (`languages`). Translating
  twice must change nothing (`TestIdempotent`).
- Two passes: the rules run once collecting the sentences to translate (`mt` records them),
  the model translates the unique sentences in parallel, then the rules run again with the
  results. Keep the rules deterministic and free of side effects.
- Fixed terms (times of day, extensions, transitions, shots, wrylies, credits) are tables in
  `rules.go`, keyed by `fold` (capitals, no accents). The English shots must start with a
  prefix the Fountain parser knows (`shotRe` in `fountain/parse.go`).
- Character names (cues without an article) are swapped for invented `placeholders` before the
  model sees them and put back after: OPUS-MT translates names (`Marie` → `Mary`). A new
  placeholder must be checked against the model (some get altered: `Nuvrak` → `Nukrak`).
- Capitals are translated in lower case (`caps` mode) and slugline locations with a French
  article added and the English one removed (`location` mode): the model mistranslates both
  otherwise (`CUISINE DE MARIE` → `MARIE CUISINE`).

## 5. Testing

- Go: unit tests live next to each package (`make test`). Add a test for every parser,
  writer or pagination change; round-trip tests (`Parse(Write(doc)) == doc`) protect formats.
- Translation: `internal/translate` tests use a fake translator and lexicon. The real model runs
  in `TestRealModel` when `PLOTZILLA_MT_MODEL` names its directory; `TestTinyModel` covers the
  whole Marian pipeline on a random 8-dimension model written by the test.
- PDF visual check: `PLOTZILLA_PDF=/tmp/x.pdf go test ./internal/pdf`, then
  `pdftoppm -png -r 60 /tmp/x.pdf /tmp/page` and look at the image, or
  `pdftotext -layout /tmp/x.pdf -` to check columns.
- UI without the Go side: serve `frontend/dist` over HTTP with a small script defining
  `window.go.main.App` (async stubs returning sample data) and `window.runtime`, injected before
  the module script; screenshot with
  `firefox --headless -no-remote -profile <tmpdir> --window-size=1440,900 --screenshot out.png URL`.
  Disable animations in that page (`*{animation:none!important;transition:none!important}`)
  or the capture happens mid-fade. Firefox is not WebKitGTK: confirm anything engine-specific
  (zoom, drag, dialogs, fonts) in the real app with `make dev`.
- Screenshots (`screenshots/`): regenerate them with `make screenshots` whenever the UI changes
  visibly, and keep the README references in sync (a new image: add its steps to
  `tools/screenshots/capture.mjs`). The run is deterministic: unchanged UI gives identical
  pixels. How it works:
  - `tools/screenshots/main.go` serves `frontend/dist` with a `window.go.main.App` proxy posting
    each call to `/api/<Method>`, answered by the real `fountain`, `layout` and `screenplay`
    packages, so pagination, locked pages and revision sets are **real**. The sample script is
    `tools/screenshots/the-last-reel.fountain`; `?v=typos` and `?v=outline` serve variants (the
    misspellings `projecter`, `thraeds`, `wacth` for `spelling.png`, whose suggestions are fixed,
    so no dictionary is needed; sections, synopses and a note for `outline.png` and
    `revisions.png`). `Save` writes nothing; revision sets have fixed dates.
  - `capture.mjs` drives Firefox through `puppeteer-core` (WebDriver BiDi) at 1440×900, device
    scale 2, with animations, transitions and the caret disabled and `<html spellcheck="false">`
    (WebKitGTK does not spell-check; Firefox does). It moves the pointer off the header before
    each capture (tooltips).
  - BiDi cannot omit the background, so `main.go` masks the 12 px window corners (radius 24 px
    at scale 2), and renders `pdf-output.png`: the PDF export of the sample with scene numbers,
    `pdftoppm -r 110`, pages side by side on the `--bg` color.
  - `FIREFOX=/path/to/firefox` picks another browser binary; `SCREENSHOTS_FLAGS="-out dir"`
    writes the images elsewhere (to compare before replacing).
- Behaviour that only exists in the real window (drag, resize, GTK dialogs, close flow,
  autosave timing) must be checked by running `./build/bin/plotzilla`; say so when you could not.

## 6. Go best practices (this repo)

- `gofmt` everything; `go vet` clean with the build tags. Standard library first: the PDF writer
  and all formats have **no third-party dependency**, keep it that way unless there is a strong reason.
- Business logic lives in `internal/…` packages that know nothing about Wails; `app.go` is a thin
  adapter (dialogs, file system, JSON-friendly return types). Keep packages free of GUI imports
  so they stay testable with plain `go test`.
- Bound methods: exported methods on `App` become JS APIs. Return `(T, error)` and let the
  frontend handle the error; return `nil`/`""` (not an error) when the user cancels a dialog.
- Wrap errors with context (`fmt.Errorf("reading FDX: %w", err)`), lower-case, no trailing
  punctuation. Never `panic` on user input; malformed files degrade to best effort.
- Write files atomically (`writeAtomic`: temp file in the same directory + rename).
- Text is Unicode: iterate with runes, use `screenplay.UpperRunes` when offsets must survive
  upper-casing (`strings.ToUpper("ß")` changes length). Offsets exchanged with the UI are runes.
- Prefer small pure functions and value types; avoid package-level mutable state (the specs
  tables are read-only). Use `min`/`max` builtins, `strings.Builder`, `bytes.Buffer`.
- Comments explain *why* (format rules, GTK quirks), not what; doc comments on exported identifiers
  start with the name.
- cgo is confined to `desktop_linux.go` behind `//go:build linux`.

## 7. TypeScript best practices (this repo)

- `strict` mode, `noUnusedLocals`; `npm run build` must type-check cleanly. No `any`: use
  `unknown` and narrow, or a precise type. Use `import type` for type-only imports.
- No framework: DOM APIs, small modules, one responsibility per file. Query elements once and keep
  references; build DOM with `createElement`/`textContent`. Use `innerHTML` only for static
  trusted markup (icons); **never** for script text, file names or error messages.
- ProseMirror rules:
  - All document changes go through transactions (`view.dispatch(tr)`); never mutate DOM inside
    the editor. Build one transaction per user action so undo is a single step; set
    `addToHistory: false` for non-content metadata (page-break decorations).
  - Commands follow the `(state, dispatch) => boolean` contract: return `false` when not
    applicable, do nothing if `dispatch` is undefined.
  - When positions change inside a transaction, map them with `tr.mapping.map()` instead of
    recomputing from the old document.
  - Positions are UTF-16 offsets; convert Go rune offsets explicitly.
  - Plugin order matters: autocomplete before the screenplay keymap, keymaps before `baseKeymap`.
- Async calls to Go can resolve after the document changed: compare against a captured doc or
  `revision` before applying results (see `paginate()` and `autosave()`).
- Handle every promise: `await` inside `try/catch`, or `.catch(reportError | console.error)`.
- `localStorage` only for per-user preferences (zoom, panel, autosave, export options), always
  through the `load`/`store` helpers (they swallow storage errors).
- Keyboard shortcuts: global ones in the `shortcuts` map (capture phase, skipped while a dialog is
  open); editing ones in `screenplayKeys()`. Update the README table and tooltips together.

## 8. CSS best practices (this repo)

- One stylesheet, organised in commented sections (topbar, menu, main, hud, paper, page break,
  autocomplete, dialogs). Plain CSS, no preprocessor.
- **Design tokens only**: colors, radii, easing and fonts come from the custom properties in
  `:root` (`--bg`, `--surface`, `--text`, `--accent`, `--t-<element>`…). Do not hard-code a new
  color in a rule; add a token. The theme is intentionally always dark, blue-dominant.
- Screen geometry of the page uses physical units (`in`, `pt`, `ch`) that mirror
  `internal/layout`; UI chrome uses `px`. Do not mix them.
- Class names are short, kebab-case, component-scoped (`.nav-head`, `.win-button`); state is a
  class on `body` (`.focus-mode`, `.no-navigator`, `.maximized`, `.backdrop`) or a `data-*`
  attribute (`[data-state='saved']`), never inline styles except computed positions.
- Animate only `opacity` and `transform`/`translate` with `var(--ease)`; keep durations
  ≤ 350 ms; every hover state has a matching transition. Respect focus visibility
  (`:focus-visible` outlines on buttons).
- Custom properties inherit, which the page-break widget relies on (`--indent` per element type)
  and so does window dragging (`--wails-draggable`). Remember both when nesting new elements in
  `.topbar` or `.el`.
- Target WebKitGTK: `-webkit-` scrollbar pseudo-elements plus standard `scrollbar-*` properties;
  `zoom` is used for editor zoom; `backdrop-filter` is fine. Avoid features WebKit lacks.

## 9. Gotchas already paid for

- `go:embed` fails if `frontend/dist` is missing → run `make`, not `go build`.
- GTK stock buttons follow the system locale (`fr_FR` here) → in-app dialogs + `LANGUAGE=en`.
- GTK 3 ignores the desktop's dark preference → `desktop_linux.go` (portal, then GNOME fallback).
- GTK reports GNOME's `appmenu` button as `menu`; both are ignored by the header bar.
- `gsettings` exists on every desktop that has GTK 3, but outside GNOME it returns GNOME's
  defaults: it is not a way to read the user's desktop settings.
- Fountain cannot express insert/shot or intertitle types: shots round-trip only when they start
  with a known prefix (`INSERT`, `CLOSE ON`, `POV`…); all-caps centred text is read as an
  intertitle. Documented in README; keep the parse and write rules symmetric.
- Fountain has no revisions, locks or paper size: they are boneyard comments, which other apps
  ignore. Inline markers travel with the text (`/*rev 2*/…/*/rev*/`, `/*page 12*/`), settings are
  one `/* Plotzilla {json} */` block at the end (`*/` inside JSON is written `*\/`). The parser
  first replaces notes and markers by tokens (U+E000 payload U+E001, no line break) so a note
  spanning lines keeps its paragraph; line rules test `bare(line)` (tokens removed) and `inline`
  turns tokens back into runs and locks. Writers must put Fountain's force prefixes (`.`, `@`,
  `!`, `>`) **before** any marker. A script without these features must write byte-identical
  output to before (`TestPlainScriptUnchanged`).
- The Fountain writer skips empty elements: their locked pages and forced page break are carried
  to the next element written (layout does the same for elements that print nothing).
- Final Draft has no sections or synopses: the FDX writer stores them as `ScriptNote`s whose
  text starts with `#` / `=`, attached (Range `0,0`) to the next paragraph, or to an empty
  trailing Action paragraph at the end; the reader turns such notes back into elements. An
  ordinary note starting with `#`, `=` or `\` is written with a leading `\`, removed on
  reading. Revised text is `<Text RevisionID="n">`, the sets a `<Revisions>` block named
  `Blue Revision - date`. The exact Final Draft schema for these (ScriptNote placement,
  Revisions attributes) was written from memory, not checked against Final Draft itself.
- The translation transaction carries the `ownLocks` meta (revisions.ts): replacing a whole
  element would make `lockPlugin` move its locked pages to the element's start, so Go remaps
  them (`remapLocks`, proportionally) and the transaction adds the revision mark itself.
- COMPOUNDRULE parts are bare dictionary words (no affixes), as in Hunspell; the first
  COMPOUNDRULE line is the rule count.
- A literal BOM or other invisible character in Go source breaks `go vet`; write `"﻿"`.
- `npm` blocks install scripts (esbuild postinstall); the build still works with the bundled
  binary.
- `packaging/arch/PKGBUILD` builds the git tag `$pkgver` (tags have no `v`: `1.1`): on a
  release, bump `pkgver` with `VERSION` and push the tag. It installs through
  `make install DESTDIR=… PREFIX=/usr`, so a new installed file goes in the Makefile, not in
  the PKGBUILD.
- Vite empties `dist/` on each build; placeholders belong in `frontend/public/`.
- The icon exists three times: `desktop/plotzilla.svg`, its PNG render (`rsvg-convert -w 256
  -h 256 desktop/plotzilla.svg -o desktop/plotzilla.png`) and a simplified copy for small sizes,
  `frontend/public/favicon.svg`, which is also the header bar logo (`.brand .logo`). Change
  them together. A clapperboard with a fountain pen whose nib ends the last script line on
  the slate: film and writing.
- Wayland shows the icon of the `.desktop` file whose name matches the app id (`plotzilla`,
  from `ProgramName`); the embedded PNG only serves X11. Keep the file name, `StartupWMClass`
  and `ProgramName` identical.
- Floating layers over the script (menus, suggestions, HUD) use the opaque `--popover` token:
  translucent backgrounds let the script text bleed through.
- A `position: fixed` popup anchored to the text must follow scrolling (see `reposition` in
  autocomplete.ts).
- Hidden-but-hoverable UI (focus mode) must stay in place with `opacity: 0`; moving it off-screen
  with a transform also moves its hover area.
- The dev shell is zsh: unquoted variables are not word-split and an unmatched glob is an error.
- `pkill -f <pattern>` also matches the shell running it when the pattern is in its command
  line (it kills your own command, exit code 144); use `pkill -x <name>`.
- Firefox gives a synthetic `ClipboardEvent` an empty `clipboardData`: test paste with
  `navigator.clipboard.writeText` + a real `Ctrl+V`.

## 10. Git and commits

- Commits are **atomic**: one logical change per commit, with the code, its tests and its
  documentation together. Every commit must build (`make`) and pass `make test`, so any commit
  can be checked out or reverted on its own. Do not mix a fix with a feature or a reformat.
- Messages are in **English** and follow this convention:

  | Prefix | Use for |
  |---|---|
  | `feat:` | a new feature or a user-visible improvement |
  | `fix:` | a bug fix |
  | `chore:` | changelog and maintenance (dependency updates, build and project configuration) |
  | `ci:` | anything that touches CI |
  | `test:` | changes to tests only |
  | `docs:` | README, AGENTS.md and other documentation |

- Summary line: `<prefix> <what the commit does>`, imperative mood, lower case, no final period,
  72 characters at most (`feat: add dual dialogue`, `fix: keep autocomplete popup under the
  caret when scrolling`). Leave a blank line, then explain *why* in the body when it is not
  obvious.
- A feature commit includes its tests; use `test:` only for commits that change tests alone.
  Screenshot updates go with `docs:`.
- Never commit build output (`build/bin`, `frontend/dist` except `.gitkeep`,
  `frontend/node_modules`, `frontend/wailsjs`); `.gitignore` covers them.

## 11. Roadmap (not implemented)

Character/location reports, recent files, locked pages and paper size in FDX.
Keep new features consistent with the rules above: Go owns formats and layout, the frontend
owns interaction.
