// Typed access to the Go methods bound by Wails (window.go.main.App) and to
// the Wails runtime.

export type ElementType =
  | 'scene'
  | 'action'
  | 'character'
  | 'parenthetical'
  | 'dialogue'
  | 'transition'
  | 'shot'
  | 'intertitle'
  | 'centered'
  | 'section'
  | 'synopsis'

export interface Run {
  text: string
  b?: boolean
  i?: boolean
  u?: boolean
  /** A writer's note: shown in the editor, never printed. */
  n?: boolean
  /** Revision set (from 1) in which the text was added or changed. */
  rev?: number
}

/** A locked page starting at a rune offset of the element's text. */
export interface PageLock {
  offset: number
  page: string
}

export interface Element {
  type: ElementType
  runs: Run[] | null
  sceneNumber?: string
  dual?: boolean
  /** Depth of a section, from 1. */
  level?: number
  /** Forced page break before the element (Fountain "==="). */
  newPage?: boolean
  locked?: PageLock[] | null
}

export interface TitlePage {
  title: string
  credit: string
  author: string
  source: string
  draftDate: string
  contact: string
  notes: string
}

export interface Revision {
  name: string // "Blue", "Double Pink"
  color: string // "blue", "pink"…
  date: string
}

/** Production settings (screenplay.Settings); every field may be missing. */
export interface Settings {
  paper?: '' | 'a4'
  revisions?: Revision[] | null
  revising?: boolean
  locked?: boolean
}

export interface ScriptDocument {
  titlePage: TitlePage
  elements: Element[]
  settings?: Settings
}

export interface OpenResult {
  path: string
  document: ScriptDocument
}

export interface PageBreak {
  element: number
  offset: number
  page: number
  /** The printed page number: "12", "12A", "12-13". */
  label: string
}

export interface ScenePage {
  element: number
  number: string
  page: number
  label: string
}

export interface LayoutInfo {
  pages: number
  breaks: PageBreak[] | null
  scenes: ScenePage[] | null
}

/** A misspelled word: element index as in PageBreak, rune offset and length. */
export interface Misspelling {
  element: number
  offset: number
  length: number
  word: string
}

export interface SpellInfo {
  languages: string[] | null
  default: string
}

/** How to draw the window frame (see WindowFrame in app.go). */
export interface WindowFrame {
  /** GTK decoration layout, e.g. "appmenu:minimize,maximize,close". */
  buttons: string
  /** False without a compositor: the window cannot have rounded corners. */
  rounded: boolean
}

/** Whether the translation model is installed, and its download size in bytes. */
export interface TranslationStatus {
  installed: boolean
  size: number
}

/** A translated element, to put in place of element `index` (flat order). */
export interface TranslatedElement {
  index: number
  element: Element
}

export interface TranslationResult {
  changes: TranslatedElement[] | null
  /** Set when the title page changed. */
  titlePage?: TitlePage
}

export interface ExportOptions {
  sceneNumbers: boolean
  titlePage: boolean
  revisions: boolean
}

interface GoApp {
  NewDocument(): Promise<ScriptDocument>
  InitialFile(): Promise<OpenResult | null>
  Open(): Promise<OpenResult | null>
  Save(doc: ScriptDocument, path: string): Promise<string>
  SaveAs(doc: ScriptDocument, current: string): Promise<string>
  ExportPDF(doc: ScriptDocument, opts: ExportOptions, current: string): Promise<string>
  ExportFDX(doc: ScriptDocument, current: string): Promise<string>
  Paginate(doc: ScriptDocument): Promise<LayoutInfo>
  ParseFountain(text: string): Promise<Element[] | null>
  SetDirty(dirty: boolean): Promise<void>
  SaveRecovery(doc: ScriptDocument): Promise<void>
  ClearRecovery(): Promise<void>
  Recovery(): Promise<OpenResult | null>
  WindowFrame(): Promise<WindowFrame>
  RevisionSet(n: number): Promise<Revision>
  SpellLanguages(): Promise<SpellInfo>
  SpellCheck(doc: ScriptDocument, lang: string): Promise<Misspelling[] | null>
  SpellSuggest(word: string, lang: string): Promise<string[] | null>
  AddToDictionary(word: string): Promise<void>
  TranslationStatus(): Promise<TranslationStatus>
  /** False if cancelled. Progress: "translation-progress" (bytes done, total). */
  DownloadTranslationModel(): Promise<boolean>
  /** Null if cancelled. Progress: "translation-progress" (sentences done, total; 0, 0 while loading). */
  Translate(doc: ScriptDocument, elements: number[] | null, titlePage: boolean): Promise<TranslationResult | null>
  CancelTranslation(): Promise<void>
}

interface WailsRuntime {
  WindowSetTitle(title: string): void
  WindowMinimise(): void
  WindowToggleMaximise(): void
  WindowIsMaximised(): Promise<boolean>
  EventsOn(event: string, callback: (...data: unknown[]) => void): () => void
  Quit(): void
}

declare global {
  interface Window {
    go: { main: { App: GoApp } }
    runtime: WailsRuntime
  }
}

export const api = new Proxy({} as GoApp, {
  get: (_, name: string) => (...args: unknown[]) =>
    (window.go.main.App as unknown as Record<string, (...a: unknown[]) => unknown>)[name](...args),
})

export const runtime = new Proxy({} as WailsRuntime, {
  get: (_, name: string) => (...args: unknown[]) =>
    (window.runtime as unknown as Record<string, (...a: unknown[]) => unknown>)[name](...args),
})
