// Find and replace in the script body. The plugin keeps the query and its
// matches and highlights them; the commands move between matches by
// selecting them and replace through ordinary transactions, so undo works.
import { Command, EditorState, Plugin, PluginKey, TextSelection } from 'prosemirror-state'
import { Decoration, DecorationSet } from 'prosemirror-view'
import { Node as PMNode } from 'prosemirror-model'
import { closeHistory } from 'prosemirror-history'
import { blockText, flatElements } from './schema'
import { replaceText } from './commands'

export interface SearchQuery {
  text: string
  caseSensitive: boolean
  wholeWord: boolean
}

export interface Match {
  from: number
  to: number
}

interface SearchState {
  query: SearchQuery
  matches: Match[]
}

export const searchKey = new PluginKey<SearchState>('search')

const EMPTY: SearchQuery = { text: '', caseSensitive: false, wholeWord: false }

function pattern(query: SearchQuery): RegExp {
  const escaped = query.text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const source = query.wholeWord ? `(?<![\\p{L}\\p{N}_])${escaped}(?![\\p{L}\\p{N}_])` : escaped
  return new RegExp(source, query.caseSensitive ? 'gu' : 'giu')
}

function findMatches(doc: PMNode, query: SearchQuery): Match[] {
  if (!query.text) return []
  const re = pattern(query)
  const matches: Match[] = []
  for (const { node, pos } of flatElements(doc)) {
    // blockText has one character per position (hard breaks included), so
    // string indexes map directly onto document positions.
    const text = blockText(node)
    for (const m of text.matchAll(re)) {
      matches.push({ from: pos + 1 + m.index, to: pos + 1 + m.index + m[0].length })
    }
  }
  return matches
}

export function searchPlugin(): Plugin<SearchState> {
  return new Plugin<SearchState>({
    key: searchKey,
    state: {
      init: () => ({ query: EMPTY, matches: [] }),
      apply(tr, prev, _old, state) {
        const query = (tr.getMeta(searchKey) as SearchQuery | undefined) ?? prev.query
        if (query === prev.query && !tr.docChanged) return prev
        return { query, matches: findMatches(state.doc, query) }
      },
    },
    props: {
      decorations(state) {
        const { matches } = searchKey.getState(state)!
        if (!matches.length) return null
        const { from, to } = state.selection
        return DecorationSet.create(
          state.doc,
          matches.map((m) =>
            Decoration.inline(m.from, m.to, { class: m.from === from && m.to === to ? 'search-match active' : 'search-match' }),
          ),
        )
      },
    },
  })
}

export function searchMatches(state: EditorState): Match[] {
  return searchKey.getState(state)?.matches ?? []
}

/** Index of the match that is currently selected, or -1. */
export function activeMatch(state: EditorState): number {
  const { from, to } = state.selection
  return searchMatches(state).findIndex((m) => m.from === from && m.to === to)
}

export function setSearch(query: SearchQuery): Command {
  return (state, dispatch) => {
    if (dispatch) dispatch(state.tr.setMeta(searchKey, query).setMeta('addToHistory', false))
    return true
  }
}

/**
 * Selects the next match after the selection (dir 1) or the previous one
 * (dir -1), wrapping around the script. With `here`, a match starting at
 * the cursor counts as the next one (incremental search while typing).
 */
export function selectMatch(dir: 1 | -1, here = false): Command {
  return (state, dispatch) => {
    const matches = searchMatches(state)
    if (!matches.length) return false
    const { from, to } = state.selection
    const m =
      dir > 0
        ? (matches.find((m) => m.from >= (here ? from : to)) ?? matches[0])
        : (matches.findLast((m) => m.to <= from) ?? matches[matches.length - 1])
    if (dispatch) dispatch(state.tr.setSelection(TextSelection.create(state.doc, m.from, m.to)).scrollIntoView())
    return true
  }
}

/** Replaces the selected match, then selects the next one. */
export function replaceMatch(text: string): Command {
  return (state, dispatch) => {
    const i = activeMatch(state)
    if (i < 0) return selectMatch(1)(state, dispatch)
    if (dispatch) {
      const m = searchMatches(state)[i]
      // Its own undo step, even right after typing or another replacement.
      const tr = replaceText(closeHistory(state.tr), m.from, m.to, text)
      const after = tr.mapping.map(m.to)
      const next = findMatches(tr.doc, searchKey.getState(state)!.query).find((n) => n.from >= after)
      if (next) tr.setSelection(TextSelection.create(tr.doc, next.from, next.to))
      dispatch(tr.scrollIntoView())
    }
    return true
  }
}

/** Replaces every match in one step, so a single undo restores them all. */
export function replaceAll(text: string): Command {
  return (state, dispatch) => {
    const matches = searchMatches(state)
    if (!matches.length) return false
    if (dispatch) {
      const tr = closeHistory(state.tr)
      for (const m of [...matches].reverse()) replaceText(tr, m.from, m.to, text)
      dispatch(tr)
    }
    return true
  }
}
