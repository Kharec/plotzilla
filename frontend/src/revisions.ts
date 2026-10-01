// Production revisions: new text is marked with the current revision set
// while revisions are on, and locked pages keep their starting point as the
// text around them is edited. Pagination itself stays in Go.
import { EditorState, Plugin, Transaction } from 'prosemirror-state'
import { Mapping } from 'prosemirror-transform'
import { Node as PMNode } from 'prosemirror-model'
import { isHistoryTransaction } from 'prosemirror-history'
import type { PageBreak } from './backend'
import { blockText, flatElements, schema, utf16Offset, type LockAttr } from './schema'

/** Transactions made by these plugins, or that set locked pages themselves. */
const INTERNAL = 'plotzilla-revisions'

export interface CurrentRevision {
  level: number
  color: string
}

/** Every range a transaction inserted or replaced, in its final document. */
function changedRanges(trs: readonly Transaction[]): [number, number][] {
  let ranges: [number, number][] = []
  for (const tr of trs) {
    ranges = ranges.map(([a, b]) => [tr.mapping.map(a, -1), tr.mapping.map(b, 1)])
    if (!tr.docChanged || tr.getMeta(INTERNAL) || isHistoryTransaction(tr)) continue
    tr.steps.forEach((step, i) => {
      const rest = tr.mapping.slice(i + 1)
      step.getMap().forEach((_oldStart, _oldEnd, start, end) => {
        ranges.push([rest.map(start, -1), rest.map(end, 1)])
      })
    })
  }
  return ranges
}

/**
 * Marks what is typed, pasted or replaced with the current revision set.
 * A deletion marks the character next to it, so the line still gets its
 * asterisk. Undo and redo are left alone.
 */
export function revisionPlugin(current: () => CurrentRevision | null): Plugin {
  return new Plugin({
    appendTransaction(trs, _old, state) {
      const rev = current()
      if (!rev || !trs.some((t) => t.docChanged)) return null
      const mark = schema.marks.revision.create(rev)
      const tr = state.tr
      const size = state.doc.content.size
      for (const [from, to] of changedRanges(trs)) {
        if (to > from) {
          tr.addMark(from, to, mark)
          continue
        }
        const $pos = state.doc.resolve(Math.min(from, size))
        if ($pos.parent.type !== schema.nodes.element) continue
        if ($pos.parentOffset > 0) tr.addMark(from - 1, from, mark)
        else if ($pos.parent.content.size > 0) tr.addMark(from, from + 1, mark)
      }
      return tr.docChanged ? tr.setMeta(INTERNAL, true) : null
    },
  })
}

interface Anchor {
  pos: number
  page: string
}

function anchors(doc: PMNode): Anchor[] {
  const out: Anchor[] = []
  for (const { node, pos } of flatElements(doc)) {
    for (const l of (node.attrs.locked as LockAttr[] | null) ?? []) out.push({ pos: pos + 1 + l.offset, page: l.page })
  }
  return out
}

/** Sets the locked pages of every element; returns tr for chaining. */
function setLocks(tr: Transaction, perElement: Map<number, LockAttr[]>): Transaction {
  for (const { node, pos } of flatElements(tr.doc)) {
    const want = perElement.get(pos) ?? null
    const have = node.attrs.locked as LockAttr[] | null
    if (JSON.stringify(have) !== JSON.stringify(want)) tr.setNodeMarkup(pos, undefined, { ...node.attrs, locked: want })
  }
  return tr
}

/**
 * Keeps each locked page where it started: its anchor follows the edits
 * around it, into the next element when its own is deleted.
 */
export function lockPlugin(locked: () => boolean): Plugin {
  return new Plugin({
    appendTransaction(trs, oldState, state) {
      if (!locked() || !trs.some((t) => t.docChanged) || trs.some((t) => t.getMeta(INTERNAL))) return null
      const old = anchors(oldState.doc)
      if (!old.length) return null
      const mapping = new Mapping()
      for (const tr of trs) mapping.appendMapping(tr.mapping)
      const moved = old.map((a) => ({ pos: mapping.map(a.pos, -1), page: a.page })).sort((a, b) => a.pos - b.pos)

      const flat = flatElements(state.doc)
      const perElement = new Map<number, LockAttr[]>()
      let e = 0
      for (const a of moved) {
        // The element holding the anchor, or the next one when it fell
        // between elements.
        while (e < flat.length - 1 && a.pos > flat[e].pos + 1 + flat[e].node.content.size) e++
        const { node, pos } = flat[e]
        const offset = Math.min(Math.max(a.pos - pos - 1, 0), node.content.size)
        perElement.set(pos, [...(perElement.get(pos) ?? []), { offset, page: a.page }])
      }
      const tr = setLocks(state.tr, perElement)
      return tr.docChanged ? tr.setMeta(INTERNAL, true) : null
    },
  })
}

/** Locks the pages where they break now. Not undoable, like the setting. */
export function lockPages(state: EditorState, breaks: PageBreak[]): Transaction {
  const flat = flatElements(state.doc)
  const perElement = new Map<number, LockAttr[]>()
  for (const br of breaks) {
    const entry = flat[br.element]
    if (!entry) continue
    const offset = utf16Offset(blockText(entry.node), br.offset)
    perElement.set(entry.pos, [...(perElement.get(entry.pos) ?? []), { offset, page: br.label || String(br.page) }])
  }
  return setLocks(state.tr, perElement).setMeta(INTERNAL, true).setMeta('addToHistory', false)
}

export function unlockPages(state: EditorState): Transaction {
  return setLocks(state.tr, new Map()).setMeta(INTERNAL, true).setMeta('addToHistory', false)
}

/**
 * Lets a transaction set the locked pages itself (in the elements' `locked`
 * attribute) instead of the plugin mapping them. Revision marks are then
 * the transaction's job too.
 */
export function ownLocks(tr: Transaction): Transaction {
  return tr.setMeta(INTERNAL, true)
}

/** Removes every revision mark; one undo step. */
export function clearRevisionMarks(state: EditorState): Transaction {
  return state.tr.removeMark(0, state.doc.content.size, schema.marks.revision).setMeta(INTERNAL, true)
}
