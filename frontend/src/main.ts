import 'prosemirror-view/style/prosemirror.css'
import './style.css'

import { EditorState, TextSelection } from 'prosemirror-state'
import { DecorationSet, EditorView } from 'prosemirror-view'
import { Fragment, MarkType, Node as PMNode, Slice } from 'prosemirror-model'
import { keymap } from 'prosemirror-keymap'
import { baseKeymap, toggleMark } from 'prosemirror-commands'
import { history } from 'prosemirror-history'

import {
  api,
  runtime,
  type ElementType,
  type LayoutInfo,
  type ScriptDocument,
  type Settings,
  type TitlePage,
} from './backend'
import {
  ELEMENT_TYPES,
  LABELS,
  elementType,
  flatElements,
  fromElement,
  fromScript,
  isPrinted,
  printedText,
  schema,
  toScript,
} from './schema'
import { NEXT_ON_ENTER, TAB, autoFormatPlugin, replaceText, screenplayKeys, setType, toggleDual } from './commands'
import { autocompletePlugin } from './autocomplete'
import { buildPageDecorations, pagesKey, pagesPlugin, placeholderPlugin } from './decorations'
import { buildSpellDecorations, misspellingAt, spellKey, spellPlugin, withoutWord, type MisspelledWord } from './spellcheck'
import { closeMenu, menuOpen, openMenu, type MenuItem } from './menu'
import { activeMatch, replaceAll, replaceMatch, searchMatches, searchPlugin, selectMatch, setSearch } from './search'
import { clearRevisionMarks, lockPages, lockPlugin, revisionPlugin, unlockPages, type CurrentRevision } from './revisions'
import { applyTranslation, selectedElements } from './translate'
import { icon } from './icons'

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T

const AUTOSAVE_DELAY = 1200

const app = {
  path: '',
  dirty: false,
  revision: 0, // bumped on every change, so an autosave knows if it is still current
  saving: false,
  draftSaved: false, // untitled script copied to the recovery file
  titlePage: { title: '', credit: 'Written by', author: '', source: '', draftDate: '', contact: '', notes: '' } as TitlePage,
  settings: {} as Settings, // paper, revisions, locked pages
  layout: null as LayoutInfo | null,
  zoom: Number(load('zoom') ?? 1),
  autosave: load('autosave') !== 'off',
  spelling: load('spelling') !== 'off',
  spellLang: load('spellLang') ?? '',
  spellLanguages: [] as string[],
  ignoredWords: new Set<string>(), // "Ignore All", for this session
}

function load(key: string): string | null {
  try {
    return localStorage.getItem(`plotzilla.${key}`)
  } catch {
    return null
  }
}

function store(key: string, value: string) {
  try {
    localStorage.setItem(`plotzilla.${key}`, value)
  } catch {
    /* storage unavailable: preferences are not kept */
  }
}

document.querySelectorAll<HTMLElement>('[data-icon]').forEach((el) => {
  el.insertAdjacentHTML('afterbegin', icon(el.dataset.icon!))
})

// ---------------------------------------------------------------- editor

const plugins = [
  autocompletePlugin(),
  autoFormatPlugin(),
  keymap(screenplayKeys()),
  keymap(baseKeymap),
  history(),
  pagesPlugin(),
  placeholderPlugin(),
  searchPlugin(),
  spellPlugin(),
  revisionPlugin(currentRevision),
  lockPlugin(() => !!app.settings.locked),
]

/** The revision set new text is marked with, while revisions are on. */
function currentRevision(): CurrentRevision | null {
  const sets = app.settings.revisions ?? []
  if (!app.settings.revising || !sets.length) return null
  return { level: sets.length, color: sets[sets.length - 1].color }
}

const newState = (doc: PMNode) => EditorState.create({ doc, plugins })

const view = new EditorView($('#editor'), {
  state: newState(fromScript({ titlePage: app.titlePage, elements: [{ type: 'scene', runs: [] }] })),
  dispatchTransaction(tr) {
    view.updateState(view.state.apply(tr))
    if (tr.docChanged) {
      markChanged()
      schedulePagination()
      scheduleSpellCheck()
    }
    updateStatus()
  },
  handlePaste(view, event) {
    const data = event.clipboardData
    if (!data) return false
    if (data.getData('text/html').includes('data-type=')) return false // copied from Plotzilla itself
    const text = data.getData('text/plain')
    if (!text.includes('\n')) return false
    // Multi-line plain text is read as Fountain so pasted scripts keep their formatting.
    api
      .ParseFountain(text)
      .then((elements) => {
        if (!elements?.length) return
        const revisions = app.settings.revisions ?? []
        const slice = new Slice(Fragment.fromArray(elements.map((el) => fromElement(el, revisions))), 1, 1)
        view.dispatch(view.state.tr.replaceSelection(slice).scrollIntoView())
      })
      .catch(reportError)
    return true
  },
})

function script(): ScriptDocument {
  return toScript(view.state.doc, app.titlePage, app.settings)
}

function loadScript(doc: ScriptDocument, path: string) {
  app.titlePage = { ...app.titlePage, ...doc.titlePage }
  app.settings = { ...doc.settings }
  renderPaper()
  app.path = path
  app.layout = null
  app.draftSaved = false
  clearTimeout(autosaveTimer)
  const state = newState(fromScript(doc))
  view.updateState(state.apply(state.tr.setSelection(TextSelection.atStart(state.doc))))
  setDirty(false)
  renderNavigator()
  updateStatus()
  paginate()
  spellCheck()
  $('#scroller').scrollTop = 0
  view.focus()
}

// ------------------------------------------------------ saving & autosave

let autosaveTimer = 0

function markChanged() {
  app.revision++
  setDirty(true)
  scheduleAutosave()
}

function scheduleAutosave() {
  clearTimeout(autosaveTimer)
  if (app.autosave && app.dirty) autosaveTimer = window.setTimeout(autosave, AUTOSAVE_DELAY)
}

/**
 * Saves silently: into the script's own file when it has one, otherwise into
 * a recovery file offered back at the next launch.
 */
async function autosave() {
  if (!app.dirty) return
  if (app.saving) return scheduleAutosave()
  const revision = app.revision
  app.saving = true
  renderSaveState()
  try {
    if (app.path) {
      await api.Save(script(), app.path)
      if (app.revision === revision) setDirty(false)
    } else {
      await api.SaveRecovery(script())
      app.draftSaved = app.revision === revision
    }
  } catch (err) {
    console.error('autosave', err)
  } finally {
    app.saving = false
    renderSaveState()
  }
}

async function saveTo(path: string | Promise<string>) {
  const revision = app.revision
  const wasUntitled = !app.path
  const written = await path
  if (!written) return
  app.path = written
  if (app.revision === revision) setDirty(false)
  else renderSaveState()
  if (wasUntitled) api.ClearRecovery().catch(console.error)
}

function setDirty(dirty: boolean) {
  if (app.dirty !== dirty) {
    app.dirty = dirty
    api.SetDirty(dirty).catch(console.error)
  }
  if (dirty) app.draftSaved = false
  renderSaveState()
}

function renderSaveState() {
  const name = app.path ? app.path.split('/').pop()! : 'Untitled'
  runtime.WindowSetTitle(`${app.dirty ? '• ' : ''}${name} — Plotzilla`)
  $('#doc-name').textContent = name
  const el = $('#save-state')
  let state = ''
  let label = ''
  if (app.saving) [state, label] = ['saving', 'Saving…']
  else if (app.dirty && app.draftSaved) [state, label] = ['draft', 'Draft backed up']
  else if (app.dirty) [state, label] = ['dirty', 'Edited']
  else if (app.path) [state, label] = ['saved', app.autosave ? 'Saved · auto' : 'Saved']
  el.dataset.state = state
  el.textContent = label
}

const autosaveToggle = $<HTMLInputElement>('#autosave-toggle')
autosaveToggle.checked = app.autosave
autosaveToggle.addEventListener('change', () => {
  app.autosave = autosaveToggle.checked
  store('autosave', app.autosave ? 'on' : 'off')
  scheduleAutosave()
  renderSaveState()
})

// ------------------------------------------------------------ pagination

let paginationTimer = 0

function schedulePagination() {
  clearTimeout(paginationTimer)
  paginationTimer = window.setTimeout(paginate, 300)
}

async function paginate() {
  const doc = view.state.doc
  try {
    const info = await api.Paginate(toScript(doc, app.titlePage, app.settings))
    if (view.state.doc !== doc) return // stale: a newer run is scheduled
    app.layout = info
    view.dispatch(view.state.tr.setMeta(pagesKey, buildPageDecorations(doc, info.breaks ?? [])).setMeta('addToHistory', false))
    renderNavigator()
    updateStatus()
  } catch (err) {
    console.error(err)
  }
}

// -------------------------------------------------------------- spelling

let spellTimer = 0

function scheduleSpellCheck() {
  clearTimeout(spellTimer)
  spellTimer = window.setTimeout(spellCheck, 500)
}

function showMisspellings(set: DecorationSet) {
  view.dispatch(view.state.tr.setMeta(spellKey, set).setMeta('addToHistory', false))
}

async function spellCheck() {
  clearTimeout(spellTimer)
  const doc = view.state.doc
  if (!app.spelling || !app.spellLang) {
    showMisspellings(DecorationSet.empty)
    return
  }
  try {
    const found = await api.SpellCheck(toScript(doc, app.titlePage, app.settings), app.spellLang)
    if (view.state.doc !== doc) return // stale: a newer check is scheduled
    showMisspellings(buildSpellDecorations(doc, found ?? [], app.ignoredWords))
  } catch (err) {
    console.error('spelling', err)
  }
}

async function setupSpelling() {
  const info = await api.SpellLanguages()
  app.spellLanguages = info.languages ?? []
  if (!app.spellLanguages.includes(app.spellLang)) app.spellLang = info.default
  updateStatus()
  spellCheck()
}

const languageNames = new Intl.DisplayNames(['en'], { type: 'language' })

/** "fr_FR" → "French (France)"; dictionaries are named after their locale. */
function languageName(code: string): string {
  try {
    return languageNames.of(code.replace(/_/g, '-')) ?? code
  } catch {
    return code // not a valid language tag, e.g. "de_DE_frami"
  }
}

function setSpelling(on: boolean, lang = app.spellLang) {
  app.spelling = on
  app.spellLang = lang
  store('spelling', on ? 'on' : 'off')
  store('spellLang', lang)
  updateStatus()
  spellCheck()
}

function spellingMenu() {
  const button = $('[data-action="spelling"]')
  if (menuOpen()) return closeMenu()
  const items: MenuItem[] = [
    { label: 'Check Spelling', checked: app.spelling, hint: 'F7', action: () => setSpelling(!app.spelling) },
    { separator: true },
  ]
  if (!app.spellLanguages.length) {
    items.push({ label: 'No dictionary installed', disabled: true })
    items.push({ label: 'Install hunspell-en_us or another Hunspell dictionary', disabled: true })
  }
  for (const code of app.spellLanguages) {
    items.push({ label: languageName(code), checked: code === app.spellLang, hint: code, action: () => setSpelling(true, code) })
  }
  const rect = button.getBoundingClientRect()
  openMenu(items, rect.left, rect.bottom + 8, { from: button })
}

/** Right-click on an underlined word: corrections, add to dictionary, ignore. */
async function correctionMenu(word: MisspelledWord, x: number, y: number) {
  const lang = app.spellLang
  const suggestions = (await api.SpellSuggest(word.word, lang).catch(reportError)) ?? []
  const doc = view.state.doc
  view.dispatch(view.state.tr.setSelection(TextSelection.create(doc, word.from, word.to)))
  // Applies only if the word is still there, unchanged.
  const unchanged = () => view.state.doc.textBetween(word.from, word.to) === doc.textBetween(word.from, word.to)
  const forget = () => {
    showMisspellings(withoutWord(view.state, word.word))
    view.focus()
  }
  const items: MenuItem[] = suggestions.map((s) => ({
    label: s,
    strong: true,
    action() {
      if (unchanged()) view.dispatch(replaceText(view.state.tr, word.from, word.to, s))
      view.focus()
    },
  }))
  if (!items.length) items.push({ label: 'No suggestions', disabled: true })
  items.push(
    { separator: true },
    {
      label: 'Add to Dictionary',
      action: () => {
        api.AddToDictionary(word.word).then(forget).catch(reportError)
      },
    },
    {
      label: 'Ignore All',
      action() {
        app.ignoredWords.add(word.word.toLowerCase())
        forget()
      },
    },
  )
  openMenu(items, x, y)
}

view.dom.addEventListener('contextmenu', (e) => {
  const at = view.posAtCoords({ left: e.clientX, top: e.clientY })
  const word = at && misspellingAt(view.state, at.pos)
  if (!word) return
  e.preventDefault()
  correctionMenu(word, e.clientX, e.clientY)
})

// ------------------------------------------------------------- navigator

/**
 * The outline: scenes, with the sections that group them and the synopses
 * that sum them up.
 */
function renderNavigator() {
  const pages = new Map((app.layout?.scenes ?? []).map((s) => [s.element, s.label || String(s.page)]))
  const items: HTMLLIElement[] = []
  let scenes = 0
  flatElements(view.state.doc).forEach(({ node, pos }, index) => {
    const type = elementType(node)
    const text = printedText(node).trim()
    if (!text || (type !== 'scene' && type !== 'section' && type !== 'synopsis')) return
    const li = document.createElement('li')
    li.dataset.pos = String(pos)
    li.addEventListener('click', () => goTo(pos))
    items.push(li)
    if (type !== 'scene') {
      li.className = `nav-${type}`
      if (type === 'section') li.dataset.level = String(Math.min(node.attrs.level, 3))
      li.textContent = text
      return
    }
    scenes++
    const num = document.createElement('span')
    num.className = 'num'
    num.textContent = node.attrs.sceneNumber || String(scenes)
    const label = document.createElement('span')
    label.className = 'text'
    label.textContent = text.toUpperCase()
    const page = document.createElement('span')
    page.className = 'page'
    page.textContent = pages.has(index) ? `p. ${pages.get(index)}` : ''
    li.append(num, label, page)
  })
  $('#scene-list').replaceChildren(...items)
  $('#scene-count').textContent = scenes ? String(scenes) : ''
  $('#scene-empty').hidden = items.length > 0
  highlightScene()
}

function goTo(pos: number) {
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, pos + 1)))
  view.focus()
  ;(view.nodeDOM(pos) as HTMLElement | null)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
}

function highlightScene() {
  const cursor = view.state.selection.from
  let active: HTMLElement | null = null
  for (const li of $('#scene-list').children as HTMLCollectionOf<HTMLElement>) {
    li.classList.remove('active')
    if (Number(li.dataset.pos) < cursor) active = li
  }
  active?.classList.add('active')
}

// ------------------------------------------------------------- type menu

const typeMenu = $<HTMLUListElement>('#type-menu')
const typeButton = $<HTMLButtonElement>('#type-button')
let menuIndex = 0

ELEMENT_TYPES.forEach((type, i) => {
  const li = document.createElement('li')
  li.style.setProperty('--dot', `var(--t-${type})`)
  // Sections and synopses have no Ctrl shortcut: they are typed as in Fountain.
  const key = i < 9 ? `Ctrl+${i + 1}` : type === 'section' ? '#' : '='
  li.innerHTML = `<span class="type-dot"></span>${LABELS[type]}<kbd>${key}</kbd>`
  li.addEventListener('mousedown', (e) => {
    e.preventDefault()
    chooseType(type)
  })
  typeMenu.appendChild(li)
})

function openTypeMenu() {
  const rect = typeButton.getBoundingClientRect()
  typeMenu.style.left = `${rect.left}px`
  typeMenu.style.top = `${rect.bottom + 8}px`
  menuIndex = ELEMENT_TYPES.indexOf(elementType(view.state.selection.$from.parent))
  renderTypeMenu()
  typeMenu.hidden = false
}

function renderTypeMenu() {
  ;[...typeMenu.children].forEach((li, i) => li.classList.toggle('active', i === menuIndex))
}

function chooseType(type: ElementType) {
  typeMenu.hidden = true
  setType(type)(view.state, view.dispatch)
  view.focus()
}

typeButton.addEventListener('mousedown', (e) => {
  e.preventDefault()
  if (typeMenu.hidden) openTypeMenu()
  else typeMenu.hidden = true
})

document.addEventListener('mousedown', (e) => {
  if (!typeMenu.hidden && !typeMenu.contains(e.target as Node) && e.target !== typeButton) typeMenu.hidden = true
})

// ---------------------------------------------------------------- status

/** The printed number of the page under the cursor ("12", "12A"). */
function currentPage(): string {
  const cursor = view.state.selection.from
  const index = flatElements(view.state.doc).findLastIndex((e) => e.pos < cursor)
  const pages = app.layout?.pages ?? 1
  let page = 1
  let label = ''
  for (const br of app.layout?.breaks ?? []) {
    if (br.element < index || (br.element === index && br.offset === 0)) [page, label] = [br.page, br.label]
  }
  return page > pages ? String(pages) : label || String(page)
}

/** Words of the printed script: notes, sections and synopses do not count. */
function wordCount(): number {
  let words = 0
  for (const { node } of flatElements(view.state.doc)) {
    if (isPrinted(elementType(node))) words += printedText(node).match(/\S+/g)?.length ?? 0
  }
  return words
}

function markActive(type: MarkType): boolean {
  const { from, $from, to, empty } = view.state.selection
  if (empty) return !!type.isInSet(view.state.storedMarks || $from.marks())
  return view.state.doc.rangeHasMark(from, to, type)
}

function updateStatus() {
  const { $from } = view.state.selection
  const type = elementType($from.parent)
  const dot = `var(--t-${type})`
  typeButton.style.setProperty('--dot', dot)
  $('#type-label').textContent = LABELS[type]
  const hudType = $('#st-type')
  hudType.style.setProperty('--dot', dot)
  hudType.textContent = LABELS[type]
  $('#st-hint').textContent = `↵ ${LABELS[NEXT_ON_ENTER[type]]}   ⇥ ${LABELS[TAB[type]]}`
  const pages = app.layout?.pages ?? 1
  $('#st-pages').textContent = `Page ${currentPage()} / ${pages}`
  $('#st-words').textContent = `${wordCount().toLocaleString('en-US')} words`
  renderRevisionState()

  const toggles: [string, boolean][] = [
    ['bold', markActive(schema.marks.bold)],
    ['italic', markActive(schema.marks.italic)],
    ['underline', markActive(schema.marks.underline)],
    ['note', markActive(schema.marks.note)],
    ['dual', $from.depth > 1],
    ['spelling', app.spelling && !!app.spellLang],
    ['revisions', !!app.settings.revising || !!app.settings.locked],
  ]
  for (const [action, on] of toggles) {
    document.querySelector(`[data-action="${action}"]`)?.classList.toggle('on', on)
  }
  highlightScene()
  renderSearchCount()
}

// ------------------------------------------------------------- revisions

/** HUD chip: the revision being marked and whether pages are locked. */
function renderRevisionState() {
  const sets = app.settings.revisions ?? []
  const chip = $('#st-rev')
  const parts: string[] = []
  const current = currentRevision()
  if (current) parts.push(`${sets[sets.length - 1].name} Revision`)
  if (app.settings.locked) parts.push('Pages Locked')
  chip.textContent = parts.join(' · ')
  chip.style.setProperty('--dot', current ? `var(--rev-${current.color})` : 'var(--muted)')
  chip.hidden = !parts.length
  $('#st-rev-sep').hidden = chip.hidden
}

/** Settings are not part of the editor's document: record the change. */
function settingsChanged() {
  markChanged()
  updateStatus()
  paginate()
}

async function newRevision() {
  const sets = app.settings.revisions ?? []
  const set = await api.RevisionSet(sets.length + 1)
  app.settings = { ...app.settings, revisions: [...sets, set], revising: true }
  settingsChanged()
}

async function toggleLock() {
  if (app.settings.locked) {
    app.settings = { ...app.settings, locked: false }
    view.dispatch(unlockPages(view.state))
    settingsChanged()
    return
  }
  // Lock the pages as they break now, from a fresh pagination.
  const doc = view.state.doc
  const info = await api.Paginate(toScript(doc, app.titlePage, { ...app.settings, locked: false }))
  if (view.state.doc !== doc) return toggleLock()
  app.settings = { ...app.settings, locked: true }
  view.dispatch(lockPages(view.state, info.breaks ?? []))
  settingsChanged()
}

async function clearMarks() {
  const clear = await messageBox({
    title: 'Clear revision marks?',
    text: 'The revised text of every revision set will be shown and printed as original text. The revision sets are kept.',
    ok: 'Clear Marks',
    cancel: 'Cancel',
    destructive: true,
  })
  if (clear) view.dispatch(clearRevisionMarks(view.state))
}

function revisionsMenu() {
  const button = $('[data-action="revisions"]')
  if (menuOpen()) return closeMenu()
  const sets = app.settings.revisions ?? []
  const next = sets.length ? 'New Revision Set' : 'Start Revisions'
  const items: MenuItem[] = [
    {
      label: 'Mark Revisions',
      checked: !!currentRevision(),
      hint: sets.length ? sets[sets.length - 1].name : '',
      action: () => {
        if (!sets.length) return void newRevision().catch(reportError)
        app.settings = { ...app.settings, revising: !app.settings.revising }
        settingsChanged()
      },
    },
    { label: next, action: () => void newRevision().catch(reportError) },
    { separator: true },
  ]
  for (const set of sets.slice().reverse()) {
    items.push({ label: `${set.name} Revision`, hint: set.date, disabled: true })
  }
  if (!sets.length) items.push({ label: 'No revision sets', disabled: true })
  items.push(
    { separator: true },
    { label: 'Lock Pages', checked: !!app.settings.locked, action: () => void toggleLock().catch(reportError) },
    { label: 'Clear Revision Marks…', disabled: !sets.length, action: () => void clearMarks().catch(reportError) },
  )
  const rect = button.getBoundingClientRect()
  openMenu(items, rect.left, rect.bottom + 8, { from: button })
}

// ----------------------------------------------------------------- paper

const PAPER_NAMES = { '': 'US Letter', a4: 'A4' }

/** The page on screen has the width of the paper; Go paginates for it. */
function renderPaper() {
  $('#paper').dataset.paper = app.settings.paper || 'letter'
}

// ---------------------------------------------------------------- search

const searchBar = $('#search')
const searchInput = $<HTMLInputElement>('#search-input')
const replaceInput = $<HTMLInputElement>('#replace-input')
const searchOptions = { caseSensitive: false, wholeWord: false }
const caseButton = searchBar.querySelector<HTMLButtonElement>('[data-search="case"]')!
const wordButton = searchBar.querySelector<HTMLButtonElement>('[data-search="word"]')!

function openSearch(replace: boolean) {
  const { from, to, empty } = view.state.selection
  const selected = empty ? '' : view.state.doc.textBetween(from, to, '\n')
  if (selected && !selected.includes('\n')) searchInput.value = selected
  if (replace || searchBar.hidden) searchBar.dataset.mode = replace ? 'replace' : 'find'
  searchBar.hidden = false
  updateSearch()
  const input = replace && searchInput.value ? replaceInput : searchInput
  input.focus()
  input.select()
}

function closeSearch() {
  searchBar.hidden = true
  setSearch({ text: '', ...searchOptions })(view.state, view.dispatch)
  view.focus()
}

/** Searches again after the query or an option changed, from the cursor. */
function updateSearch() {
  setSearch({ text: searchInput.value, ...searchOptions })(view.state, view.dispatch)
  selectMatch(1, true)(view.state, view.dispatch)
  renderSearchCount()
}

function renderSearchCount() {
  if (searchBar.hidden) return
  const total = searchMatches(view.state).length
  const current = activeMatch(view.state)
  let label = ''
  if (searchInput.value) label = !total ? 'No results' : current >= 0 ? `${current + 1} of ${total}` : `${total} found`
  $('#search-count').textContent = label
  searchBar.dataset.state = searchInput.value && !total ? 'none' : ''
  caseButton.classList.toggle('on', searchOptions.caseSensitive)
  wordButton.classList.toggle('on', searchOptions.wholeWord)
}

const searchActions: Record<string, () => void> = {
  toggle: () => openSearch(searchBar.dataset.mode !== 'replace'),
  case() {
    searchOptions.caseSensitive = !searchOptions.caseSensitive
    updateSearch()
  },
  word() {
    searchOptions.wholeWord = !searchOptions.wholeWord
    updateSearch()
  },
  previous: () => selectMatch(-1)(view.state, view.dispatch),
  next: () => selectMatch(1)(view.state, view.dispatch),
  close: closeSearch,
  replace: () => replaceMatch(replaceInput.value)(view.state, view.dispatch),
  replaceAll: () => replaceAll(replaceInput.value)(view.state, view.dispatch),
}

searchBar.querySelectorAll<HTMLButtonElement>('button[data-search]').forEach((btn) => {
  btn.addEventListener('mousedown', (e) => e.preventDefault()) // keep the focus in the fields
  btn.addEventListener('click', () => searchActions[btn.dataset.search!]())
})

searchInput.addEventListener('input', updateSearch)
searchInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') searchActions[e.shiftKey ? 'previous' : 'next']()
  else if (e.key === 'Escape') closeSearch()
  else return
  e.preventDefault()
})
replaceInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') searchActions[e.ctrlKey && e.altKey ? 'replaceAll' : 'replace']()
  else if (e.key === 'Escape') closeSearch()
  else return
  e.preventDefault()
})

// ----------------------------------------------------------- translation

interface ProgressOptions {
  title: string
  /** The line under the bar, from Go's progress (a total of 0: not started yet). */
  describe: (done: number, total: number) => string
}

/**
 * Runs a Go task that reports "translation-progress" behind a progress
 * dialog; closing the dialog (Cancel, Esc) cancels the task.
 */
async function withProgress<T>(opts: ProgressOptions, task: () => Promise<T>): Promise<T> {
  const dialog = $<HTMLDialogElement>('#progress-dialog')
  const track = $('#progress')
  const bar = $('#progress-bar')
  const show = (done: number, total: number) => {
    track.dataset.state = total ? '' : 'busy'
    bar.style.transform = total ? `scaleX(${Math.min(1, done / total)})` : ''
    $('#progress-text').textContent = opts.describe(done, total)
  }
  $('#progress-title').textContent = opts.title
  show(0, 0)
  const off = runtime.EventsOn('translation-progress', (done, total) => show(Number(done), Number(total)))
  let running = true
  dialog.onclose = () => {
    if (running) api.CancelTranslation().catch(console.error)
  }
  typeMenu.hidden = true
  dialog.showModal()
  try {
    return await task()
  } finally {
    running = false
    off()
    if (dialog.open) dialog.close()
    view.focus()
  }
}

const megabytes = (bytes: number) => Math.round(bytes / 1e6).toLocaleString('en-US')

/**
 * Translates the French text of the selection, or of the whole script and
 * its title page, into English, offline. Text already in English is left
 * as it is, so a script can be translated a part at a time.
 */
async function translateScript() {
  const status = await api.TranslationStatus()
  if (!status.installed) {
    const download = await messageBox({
      title: 'Download the translation model?',
      text: `Plotzilla translates French into English on this computer, without an internet connection. It needs the OPUS-MT French–English model (${megabytes(status.size)} MB), downloaded once from Hugging Face.`,
      ok: 'Download',
      cancel: 'Cancel',
    })
    if (!download) return
    const installed = await withProgress(
      {
        title: 'Downloading the translation model',
        describe: (done, total) => (total ? `${megabytes(done)} of ${megabytes(total)} MB` : 'Connecting…'),
      },
      () => api.DownloadTranslationModel(),
    )
    if (!installed) return
  }

  const doc = view.state.doc
  const elements = selectedElements(view.state)
  const whole = elements === null
  const result = await withProgress(
    {
      title: whole ? 'Translating the script' : 'Translating the selection',
      describe: (done, total) =>
        total ? `${done.toLocaleString('en-US')} of ${total.toLocaleString('en-US')} sentences` : 'Loading the translation model…',
    },
    () => api.Translate(toScript(doc, app.titlePage, app.settings), elements, whole),
  )
  if (!result) return // cancelled
  if (view.state.doc !== doc) {
    await messageBox({
      title: 'Translation not applied',
      text: 'The script changed while it was being translated. Translate it again to include the changes.',
      ok: 'OK',
    })
    return
  }
  const changes = result.changes ?? []
  if (changes.length) {
    view.dispatch(applyTranslation(view.state, changes, app.settings.revisions ?? [], currentRevision()))
  }
  if (result.titlePage) {
    app.titlePage = { ...result.titlePage }
    markChanged()
  }
  if (!changes.length && !result.titlePage) {
    await messageBox({
      title: 'Nothing to translate',
      text: `No French text was found in the ${whole ? 'script' : 'selection'}. Text already in English is left as it is.`,
      ok: 'OK',
    })
  }
}

// ----------------------------------------------------------------- files

interface MessageOptions {
  title: string
  text: string
  ok: string
  cancel?: string // omitted: a single-button notice
  destructive?: boolean
}

/**
 * In-app message box. The native GTK one only offers stock buttons
 * (Yes/No, OK) translated to the system language.
 */
function messageBox(opts: MessageOptions): Promise<boolean> {
  const dialog = $<HTMLDialogElement>('#message-dialog')
  $('#message-title').textContent = opts.title
  $('#message-text').textContent = opts.text
  const ok = $<HTMLButtonElement>('#message-ok')
  const cancel = $<HTMLButtonElement>('#message-cancel')
  ok.textContent = opts.ok
  ok.classList.toggle('destructive', !!opts.destructive)
  cancel.textContent = opts.cancel ?? ''
  cancel.hidden = !opts.cancel
  dialog.returnValue = ''
  typeMenu.hidden = true
  return new Promise((resolve) => {
    dialog.onclose = () => {
      resolve(dialog.returnValue === 'ok')
      view.focus()
    }
    dialog.showModal()
    ;(opts.cancel && opts.destructive ? cancel : ok).focus()
  })
}

function reportError(err: unknown) {
  console.error(err)
  messageBox({ title: 'Something went wrong', text: String(err), ok: 'OK' })
}

async function confirmDiscard(): Promise<boolean> {
  if (!app.dirty) return true
  const discard = await messageBox({
    title: 'Discard unsaved changes?',
    text: 'This screenplay has changes that have not been saved. If you continue, they will be lost.',
    ok: 'Discard',
    cancel: 'Cancel',
    destructive: true,
  })
  if (!discard) return false
  if (!app.path) await api.ClearRecovery()
  await api.SetDirty(false)
  app.dirty = false
  return true
}

// Closing the window with unsaved changes: Go keeps it open and asks us.
runtime.EventsOn('confirm-close', async () => {
  if (document.querySelector('dialog[open]')) return
  if (await confirmDiscard()) runtime.Quit()
})

const actions: Record<string, () => unknown> = {
  async new() {
    if (await confirmDiscard()) loadScript(await api.NewDocument(), '')
  },
  async open() {
    if (!(await confirmDiscard())) return
    const res = await api.Open()
    if (res) loadScript(res.document, res.path)
  },
  save: () => saveTo(api.Save(script(), app.path)),
  saveAs: () => saveTo(api.SaveAs(script(), app.path)),
  pdf() {
    const dialog = $<HTMLDialogElement>('#export-dialog')
    const form = dialog.querySelector('form')!
    const input = (name: string) => form.elements.namedItem(name) as HTMLInputElement
    const paper = form.elements.namedItem('paper') as HTMLSelectElement
    const opts = { titlePage: true, sceneNumbers: false, revisions: true, ...JSON.parse(load('export') ?? '{}') }
    input('titlePage').checked = opts.titlePage
    input('sceneNumbers').checked = opts.sceneNumbers
    input('revisions').checked = opts.revisions
    input('revisions').closest('label')!.hidden = !app.settings.revisions?.length
    paper.value = app.settings.paper ?? ''
    const describe = () => {
      $('#export-paper').textContent = PAPER_NAMES[paper.value as '' | 'a4'] ?? 'US Letter'
    }
    paper.onchange = describe
    describe()
    dialog.onclose = async () => {
      view.focus()
      if (dialog.returnValue !== 'ok') return
      const chosen = {
        titlePage: input('titlePage').checked,
        sceneNumbers: input('sceneNumbers').checked,
        revisions: input('revisions').checked,
      }
      store('export', JSON.stringify(chosen))
      // The paper size belongs to the script: it changes its pagination.
      const size = paper.value === 'a4' ? 'a4' : ''
      if (size !== (app.settings.paper ?? '')) {
        app.settings = { ...app.settings, paper: size }
        renderPaper()
        settingsChanged()
      }
      await api.ExportPDF(script(), chosen, app.path).catch(reportError)
    }
    dialog.showModal()
  },
  async fdx() {
    await api.ExportFDX(script(), app.path)
  },
  title() {
    const dialog = $<HTMLDialogElement>('#title-dialog')
    const form = dialog.querySelector('form')!
    const fields = Object.keys(app.titlePage) as (keyof TitlePage)[]
    for (const f of fields) {
      const input = form.elements.namedItem(f) as HTMLInputElement | null
      if (input) input.value = app.titlePage[f]
    }
    dialog.onclose = () => {
      view.focus()
      if (dialog.returnValue !== 'ok') return
      let changed = false
      for (const f of fields) {
        const input = form.elements.namedItem(f) as HTMLInputElement | null
        if (input && input.value !== app.titlePage[f]) {
          app.titlePage[f] = input.value
          changed = true
        }
      }
      if (changed) markChanged()
    }
    dialog.showModal()
  },
  bold: () => toggleMark(schema.marks.bold)(view.state, view.dispatch),
  italic: () => toggleMark(schema.marks.italic)(view.state, view.dispatch),
  underline: () => toggleMark(schema.marks.underline)(view.state, view.dispatch),
  note: () => toggleMark(schema.marks.note)(view.state, view.dispatch),
  revisions: revisionsMenu,
  translate: translateScript,
  dual: () => toggleDual(view.state, view.dispatch),
  navigator() {
    document.body.classList.toggle('no-navigator')
    store('navigator', document.body.classList.contains('no-navigator') ? 'off' : 'on')
  },
  focus: () => document.body.classList.toggle('focus-mode'),
  find: () => openSearch(false),
  replace: () => openSearch(true),
  findNext: () => (searchBar.hidden ? openSearch(false) : searchActions.next()),
  findPrevious: () => (searchBar.hidden ? openSearch(false) : searchActions.previous()),
  spelling: spellingMenu,
  toggleSpelling: () => setSpelling(!app.spelling),
  zoomIn: () => setZoom(app.zoom + 0.1),
  zoomOut: () => setZoom(app.zoom - 0.1),
  zoomReset: () => setZoom(1),
}

async function run(name: string) {
  try {
    await actions[name]()
  } catch (err) {
    reportError(err)
  }
  if (!document.querySelector('dialog[open]') && !searchBar.contains(document.activeElement)) view.focus()
}

function setZoom(z: number) {
  app.zoom = Math.round(Math.min(2, Math.max(0.6, z)) * 10) / 10
  $('#paper').style.setProperty('zoom', String(app.zoom))
  store('zoom', String(app.zoom))
}

document.querySelectorAll<HTMLButtonElement>('.tools button[data-action]').forEach((btn) => {
  btn.addEventListener('mousedown', (e) => e.preventDefault()) // keep the editor's selection
  btn.addEventListener('click', () => run(btn.dataset.action!))
})

const shortcuts: Record<string, string> = {
  'ctrl+n': 'new',
  'ctrl+o': 'open',
  'ctrl+s': 'save',
  'ctrl+shift+s': 'saveAs',
  'ctrl+p': 'pdf',
  'ctrl+shift+e': 'fdx',
  'ctrl+shift+t': 'title',
  'ctrl+\\': 'navigator',
  'ctrl+shift+f': 'focus',
  'ctrl+f': 'find',
  'ctrl+h': 'replace',
  'ctrl+g': 'findNext',
  'ctrl+shift+g': 'findPrevious',
  f3: 'findNext',
  'shift+f3': 'findPrevious',
  f7: 'toggleSpelling',
  'ctrl+shift+l': 'translate',
  'ctrl+=': 'zoomIn',
  'ctrl++': 'zoomIn',
  'ctrl+-': 'zoomOut',
  'ctrl+0': 'zoomReset',
}

window.addEventListener(
  'keydown',
  (e) => {
    if (!typeMenu.hidden) {
      const n = ELEMENT_TYPES.length
      const handled: Record<string, () => void> = {
        ArrowDown: () => ((menuIndex = (menuIndex + 1) % n), renderTypeMenu()),
        ArrowUp: () => ((menuIndex = (menuIndex - 1 + n) % n), renderTypeMenu()),
        Enter: () => chooseType(ELEMENT_TYPES[menuIndex]),
        Escape: () => ((typeMenu.hidden = true), view.focus()),
      }
      if (handled[e.key]) {
        e.preventDefault()
        e.stopPropagation()
        handled[e.key]()
        return
      }
    }
    const fkey = /^F\d+$/.test(e.key)
    if ((!e.ctrlKey && !fkey) || e.altKey) return
    const shift = e.shiftKey && (fkey || /^[a-z]$/i.test(e.key))
    const key = `${e.ctrlKey ? 'ctrl+' : ''}${shift ? 'shift+' : ''}${e.key.toLowerCase()}`
    const action = shortcuts[key]
    if (!action || document.querySelector('dialog[open]')) return
    e.preventDefault()
    e.stopPropagation()
    run(action)
  },
  true,
)

// ---------------------------------------------------------- window frame

// The window has no system title bar: the top bar plays the part of a GTK
// header bar (drag to move, double-click to maximise, buttons laid out from
// the desktop's button layout, whatever the desktop).
const WINDOW_BUTTONS: Record<string, [string, () => void]> = {
  minimize: ['Minimize', () => runtime.WindowMinimise()],
  maximize: ['Maximize', () => runtime.WindowToggleMaximise()],
  close: ['Close', () => runtime.Quit()],
}

async function setupWindowControls() {
  const frame = await api.WindowFrame().catch(() => ({ buttons: 'appmenu:close', rounded: true }))
  // Without a compositor the corners cannot be transparent: keep them square.
  document.body.classList.toggle('opaque', !frame.rounded)
  const [left = '', right = ''] = frame.buttons.split(':')
  const fill = (el: HTMLElement, spec: string) => {
    for (const name of spec.split(',').map((s) => s.trim())) {
      const entry = WINDOW_BUTTONS[name]
      if (!entry) continue
      const btn = document.createElement('button')
      btn.className = `win-button win-${name}`
      btn.setAttribute('aria-label', entry[0])
      btn.innerHTML = icon(name)
      btn.addEventListener('mousedown', (e) => e.preventDefault())
      btn.addEventListener('click', entry[1])
      el.appendChild(btn)
    }
    el.hidden = !el.children.length
  }
  fill($('#win-left'), left)
  fill($('#win-right'), right)
}

async function syncMaximised() {
  const maximised = await runtime.WindowIsMaximised().catch(() => false)
  document.body.classList.toggle('maximized', maximised)
  const btn = document.querySelector('.win-maximize')
  if (btn) btn.innerHTML = icon(maximised ? 'restore' : 'maximize')
}

$('#topbar').addEventListener('dblclick', (e) => {
  if ((e.target as HTMLElement).closest('button, .menu')) return
  runtime.WindowToggleMaximise()
})
window.addEventListener('resize', () => syncMaximised())
window.addEventListener('blur', () => document.body.classList.add('backdrop'))
window.addEventListener('focus', () => document.body.classList.remove('backdrop'))

// ------------------------------------------------------------------ boot

async function boot() {
  const initial = await api.InitialFile()
  if (initial) return loadScript(initial.document, initial.path)
  const recovered = await api.Recovery()
  if (!recovered) return paginate()
  const restore = await messageBox({
    title: 'Recover unsaved screenplay?',
    text: 'An unsaved screenplay from your previous session was found. Do you want to restore it?',
    ok: 'Restore',
    cancel: 'Discard',
  })
  if (!restore) {
    await api.ClearRecovery()
    return paginate()
  }
  loadScript(recovered.document, '')
  markChanged()
}

if (load('navigator') === 'off') document.body.classList.add('no-navigator')
setZoom(app.zoom)
renderPaper()
renderSaveState()
renderNavigator()
updateStatus()
view.focus()
setupWindowControls().then(syncMaximised)
setupSpelling().catch(console.error)
boot().catch(reportError)
