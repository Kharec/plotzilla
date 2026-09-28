// Spelling: wavy underlines under the words the Go checker reports. The
// word being typed is left alone until the cursor leaves it.
import { EditorState, Plugin, PluginKey, Transaction } from 'prosemirror-state'
import { Decoration, DecorationSet } from 'prosemirror-view'
import { Node as PMNode } from 'prosemirror-model'
import type { Misspelling } from './backend'
import { blockText, flatElements } from './schema'
import { utf16Offset } from './decorations'

interface SpellState {
  set: DecorationSet
  typing: boolean // the last change was an edit, not a cursor move
}

interface WordSpec {
  word: string
}

export const spellKey = new PluginKey<SpellState>('spell')

export function buildSpellDecorations(doc: PMNode, found: Misspelling[], ignored: Set<string>): DecorationSet {
  const flat = flatElements(doc)
  const decos: Decoration[] = []
  for (const m of found) {
    const entry = flat[m.element]
    if (!entry || ignored.has(m.word.toLowerCase())) continue
    const text = blockText(entry.node)
    const start = entry.pos + 1
    const spec: WordSpec = { word: m.word }
    decos.push(
      Decoration.inline(
        start + utf16Offset(text, m.offset),
        start + utf16Offset(text, m.offset + m.length),
        { class: 'misspelled' },
        spec,
      ),
    )
  }
  return DecorationSet.create(doc, decos)
}

/** Maps the underlines through a change, dropping those it touched. */
function mapThrough(set: DecorationSet, tr: Transaction): DecorationSet {
  let mapped = set.map(tr.mapping, tr.doc)
  tr.mapping.maps.forEach((map, i) => {
    const rest = tr.mapping.slice(i + 1)
    map.forEach((_oldStart, _oldEnd, start, end) => {
      const touched = mapped.find(rest.map(start, -1), rest.map(end, 1))
      if (touched.length) mapped = mapped.remove(touched)
    })
  })
  return mapped
}

export function spellPlugin(): Plugin<SpellState> {
  return new Plugin<SpellState>({
    key: spellKey,
    state: {
      init: () => ({ set: DecorationSet.empty, typing: false }),
      apply(tr, prev) {
        const fresh = tr.getMeta(spellKey) as DecorationSet | undefined
        const typing = tr.docChanged ? true : tr.selectionSet ? false : prev.typing
        if (fresh) return { set: fresh, typing }
        if (tr.docChanged) return { set: mapThrough(prev.set, tr), typing }
        return typing === prev.typing ? prev : { set: prev.set, typing }
      },
    },
    props: {
      decorations(state) {
        const { set, typing } = spellKey.getState(state)!
        if (!typing) return set
        const head = state.selection.head
        const here = set.find(head, head)
        return here.length ? set.remove(here) : set
      },
    },
  })
}

export interface MisspelledWord {
  from: number
  to: number
  word: string
}

/** The underlined word at pos, if any. */
export function misspellingAt(state: EditorState, pos: number): MisspelledWord | null {
  const d = spellKey.getState(state)?.set.find(pos, pos).find((d) => d.from <= pos && pos <= d.to)
  return d ? { from: d.from, to: d.to, word: (d.spec as WordSpec).word } : null
}

/** The current underlines without those of word (added or ignored). */
export function withoutWord(state: EditorState, word: string): DecorationSet {
  const set = spellKey.getState(state)!.set
  const lower = word.toLowerCase()
  return set.remove(set.find(undefined, undefined, (spec: WordSpec) => spec.word.toLowerCase() === lower))
}
