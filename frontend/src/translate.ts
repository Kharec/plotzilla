// Applying a French → English translation computed in Go: which elements
// the selection covers, and the transaction that puts the translated
// elements in place.
import type { EditorState, Transaction } from 'prosemirror-state'
import type { Revision, TranslatedElement } from './backend'
import { flatElements, fromElement, schema } from './schema'
import { ownLocks, type CurrentRevision } from './revisions'

/** Flat indexes of the elements the selection touches; null for the whole script. */
export function selectedElements(state: EditorState): number[] | null {
  const { from, to, empty } = state.selection
  if (empty) return null
  const out: number[] = []
  flatElements(state.doc).forEach(({ node, pos }, i) => {
    if (pos + node.nodeSize > from && pos < to) out.push(i)
  })
  return out
}

/**
 * Replaces each translated element, in one undoable step. Go has already
 * placed the element's locked pages in the translated text, so the lock
 * plugin is told to leave them; translated text is marked with the current
 * revision set when revisions are being marked.
 */
export function applyTranslation(
  state: EditorState,
  changes: TranslatedElement[],
  revisions: Revision[],
  current: CurrentRevision | null,
): Transaction {
  const flat = flatElements(state.doc)
  const tr = state.tr
  const mark = current && schema.marks.revision.create(current)
  // From the end, so the positions of the elements before stay valid.
  for (const change of [...changes].sort((a, b) => b.index - a.index)) {
    const at = flat[change.index]
    if (!at) continue
    const node = fromElement(change.element, revisions)
    tr.replaceWith(at.pos, at.pos + at.node.nodeSize, node)
    if (mark && node.content.size) tr.addMark(at.pos + 1, at.pos + 1 + node.content.size, mark)
  }
  return ownLocks(tr)
}
