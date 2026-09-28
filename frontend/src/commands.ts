// Screenplay editing behaviour, modelled on Final Draft / Arc Studio:
// Enter moves to the element that naturally follows, Tab cycles the current
// element's type, and typing is auto-detected where it is unambiguous.
import { Command, EditorState, Plugin, TextSelection, Transaction } from 'prosemirror-state'
import { Node as PMNode } from 'prosemirror-model'
import { toggleMark } from 'prosemirror-commands'
import { undo, redo } from 'prosemirror-history'
import type { ElementType } from './backend'
import { ELEMENT_TYPES, blockText, dualNode, elementType, schema } from './schema'

/** Element created by Enter at the end of a non-empty element. */
export const NEXT_ON_ENTER: Record<ElementType, ElementType> = {
  scene: 'action',
  action: 'action',
  character: 'dialogue',
  parenthetical: 'dialogue',
  dialogue: 'action',
  transition: 'scene',
  shot: 'action',
  intertitle: 'action',
  centered: 'action',
  section: 'scene',
  synopsis: 'action',
}

/** Enter on an empty element changes its type instead of adding a line. */
const RETYPE_ON_EMPTY_ENTER: Record<ElementType, ElementType> = {
  scene: 'action',
  action: 'scene',
  character: 'action',
  parenthetical: 'dialogue',
  dialogue: 'action',
  transition: 'action',
  shot: 'action',
  intertitle: 'action',
  centered: 'action',
  section: 'action',
  synopsis: 'action',
}

export const TAB: Record<ElementType, ElementType> = {
  action: 'character',
  character: 'transition',
  transition: 'scene',
  scene: 'action',
  dialogue: 'parenthetical',
  parenthetical: 'dialogue',
  shot: 'action',
  intertitle: 'action',
  centered: 'action',
  section: 'action',
  synopsis: 'action',
}

const SHIFT_TAB: Record<ElementType, ElementType> = {
  action: 'scene',
  scene: 'transition',
  transition: 'character',
  character: 'action',
  dialogue: 'character',
  parenthetical: 'dialogue',
  shot: 'action',
  intertitle: 'action',
  centered: 'action',
  section: 'action',
  synopsis: 'action',
}

const SCENE_PREFIX = /^(int|ext|est|int\.?\/ext|i\/e)\.?\s$/i
const SHOT_PREFIX = /^insert(\s-|:)?\s$/i
// Fountain's outline markers, typed at the start of an empty line.
const SECTION_PREFIX = /^(#{1,6}) $/
const SYNOPSIS_PREFIX = /^= $/
const TRANSITION = /^[^a-z]+ TO:$/

function isBlank(node: PMNode): boolean {
  const text = blockText(node)
  return text.trim() === '' || (elementType(node) === 'parenthetical' && text.trim() === '()')
}

/**
 * Changes the type of the element at pos, adding or removing the
 * parentheses that a parenthetical carries.
 */
export function retype(tr: Transaction, pos: number, type: ElementType): Transaction {
  const node = tr.doc.nodeAt(pos)!
  const old = elementType(node)
  if (old === type) return tr
  tr.setNodeMarkup(pos, undefined, { ...node.attrs, type })
  const text = blockText(node)
  const start = pos + 1
  const end = start + node.content.size
  if (type === 'parenthetical') {
    if (!text.startsWith('(')) {
      tr.insertText(')', end)
      tr.insertText('(', start)
      const size = tr.doc.nodeAt(pos)!.content.size
      tr.setSelection(TextSelection.create(tr.doc, start + size - 1))
    }
  } else if (old === 'parenthetical') {
    if (text.endsWith(')')) tr.delete(end - 1, end)
    if (text.startsWith('(')) tr.delete(start, start + 1)
  }
  return tr
}

/**
 * Replaces the text between from and to (inside one element), keeping the
 * bold, italic and underline of the text it replaces.
 */
export function replaceText(tr: Transaction, from: number, to: number, text: string): Transaction {
  if (!text) return tr.delete(from, to)
  const marks = tr.doc.nodeAt(from)?.marks ?? []
  return tr.replaceWith(from, to, schema.text(text, marks))
}

export function setType(type: ElementType): Command {
  return (state, dispatch) => {
    const { $from, $to } = state.selection
    if (dispatch) {
      const tr = state.tr
      if ($from.sameParent($to)) {
        retype(tr, $from.before(), type)
      } else {
        state.doc.nodesBetween($from.pos, $to.pos, (node, pos) => {
          if (node.type === schema.nodes.element) {
            tr.setNodeMarkup(pos, undefined, { ...node.attrs, type })
            return false
          }
          return true
        })
      }
      dispatch(tr.scrollIntoView())
    }
    return true
  }
}

function cycle(map: Record<ElementType, ElementType>): Command {
  return (state, dispatch) => {
    const type = elementType(state.selection.$from.parent)
    return setType(map[type])(state, dispatch)
  }
}

export const enter: Command = (state, dispatch) => {
  const tr = state.tr
  if (!state.selection.empty) tr.deleteSelection()
  const $pos = tr.selection.$from
  const node = $pos.parent
  const pos = $pos.before()
  let type = elementType(node)

  // Inside a dual dialogue, finishing a column moves on: the left column
  // hands over to the right one, the right column exits below the block.
  const colDepth = $pos.depth - 1
  if ($pos.node(colDepth).type === schema.nodes.column) {
    const text = blockText(node)
    const atEnd =
      $pos.parentOffset === node.content.size ||
      (type === 'parenthetical' && text.endsWith(')') && $pos.parentOffset === node.content.size - 1)
    const blank = isBlank(node)
    if (blank || (atEnd && type === 'dialogue')) {
      if (dispatch) {
        const dual = $pos.node(colDepth - 1)
        const dualPos = $pos.before(colDepth - 1)
        const isRight = $pos.index(colDepth - 1) === 1
        const rightStart = dualPos + 1 + dual.child(0).nodeSize + 1 // first element of the right column
        const dualEnd = dualPos + dual.nodeSize
        if (blank && $pos.node(colDepth).childCount > 1) tr.delete(pos, pos + node.nodeSize)
        if (isRight) {
          const at = tr.mapping.map(dualEnd)
          tr.insert(at, schema.nodes.element.create({ type: 'action' }))
          tr.setSelection(TextSelection.create(tr.doc, at + 1))
        } else {
          const at = tr.mapping.map(rightStart)
          tr.setSelection(TextSelection.create(tr.doc, at + 1 + tr.doc.nodeAt(at)!.content.size))
        }
        dispatch(tr.scrollIntoView())
      }
      return true
    }
  }

  if (isBlank(node)) {
    if (dispatch) {
      if (type === 'parenthetical') tr.delete(pos + 1, pos + 1 + node.content.size)
      dispatch(retype(tr, pos, RETYPE_ON_EMPTY_ENTER[type]).scrollIntoView())
    }
    return true
  }

  const text = blockText(node)
  if (type === 'action' && TRANSITION.test(text)) {
    tr.setNodeMarkup(pos, undefined, { ...node.attrs, type: 'transition' })
    type = 'transition'
  }
  // Inside a parenthetical, the closing ")" counts as the end.
  const atEnd =
    $pos.parentOffset === node.content.size ||
    (type === 'parenthetical' && text.endsWith(')') && $pos.parentOffset === node.content.size - 1)

  if (dispatch) {
    if (atEnd) {
      const after = pos + node.nodeSize
      tr.insert(after, schema.nodes.element.create({ type: NEXT_ON_ENTER[type] }))
      tr.setSelection(TextSelection.create(tr.doc, after + 1))
    } else {
      // The second half never inherits a forced page break.
      tr.split($pos.pos, 1, [{ type: schema.nodes.element, attrs: { type, sceneNumber: '', level: node.attrs.level } }])
    }
    dispatch(tr.scrollIntoView())
  }
  return true
}

const inDialogue = (t: ElementType | null) => t === 'parenthetical' || t === 'dialogue'

/** Top-level child range [start, end) of the dialogue block containing index. */
function dialogueBlockAt(doc: PMNode, index: number): [number, number] | null {
  const t = (i: number) => {
    const n = doc.child(i)
    return n.type === schema.nodes.element ? elementType(n) : null
  }
  let start = index
  while (start > 0 && inDialogue(t(start))) start--
  if (t(start) !== 'character') return null
  let end = start + 1
  while (end < doc.childCount && inDialogue(t(end))) end++
  return [start, end]
}

/**
 * Ctrl+D: puts the dialogue under the cursor side by side with the next (or
 * previous) one. Without a neighbour, opens an empty right column. Inside a
 * dual dialogue, splits it back into two blocks.
 */
export const toggleDual: Command = (state, dispatch) => {
  const { $from } = state.selection
  const doc = state.doc
  for (let d = $from.depth; d > 0; d--) {
    const dual = $from.node(d)
    if (dual.type !== schema.nodes.dual) continue
    if (dispatch) {
      const pos = $from.before(d)
      const inRight = $from.index(d) === 1
      const tr = state.tr.replaceWith(pos, pos + dual.nodeSize, dual.child(0).content.append(dual.child(1).content))
      tr.setSelection(TextSelection.create(tr.doc, $from.pos - (inRight ? 4 : 2)))
      dispatch(tr.scrollIntoView())
    }
    return true
  }
  if ($from.depth !== 1) return false
  const cur = dialogueBlockAt(doc, $from.index(0))
  if (!cur) return false
  let left = cur
  let right: [number, number] | null = null
  const next = cur[1] < doc.childCount ? dialogueBlockAt(doc, cur[1]) : null
  const prev = cur[0] > 0 ? dialogueBlockAt(doc, cur[0] - 1) : null
  if (next && next[0] === cur[1]) right = next
  else if (prev && prev[1] === cur[0]) [left, right] = [prev, cur]

  if (dispatch) {
    const posOf = (i: number) => {
      let p = 0
      for (let k = 0; k < i; k++) p += doc.child(k).nodeSize
      return p
    }
    const nodes = ([a, b]: [number, number]) => Array.from({ length: b - a }, (_, k) => doc.child(a + k))
    const from = posOf(left[0])
    const mid = posOf(left[1])
    const to = posOf((right ?? left)[1])
    const rightNodes = right ? nodes(right) : [schema.nodes.element.create({ type: 'character' })]
    const tr = state.tr.replaceWith(from, to, dualNode(nodes(left), rightNodes))
    // dual + column open before the left content; +2 more for the right column.
    const sel = !right ? mid + 5 : $from.pos + ($from.pos >= mid ? 4 : 2)
    tr.setSelection(TextSelection.create(tr.doc, sel))
    dispatch(tr.scrollIntoView())
  }
  return true
}

/**
 * Ctrl+Enter: forces a page break at the cursor, like "===" in Fountain. At
 * the start of an element (or on an empty one) it toggles the break before
 * it; elsewhere it splits the element, or opens the next one, on a new page.
 * In a dialogue block, or a dual dialogue, it toggles the break before the
 * whole block: dialogue is never split by hand.
 */
export const pageBreak: Command = (state, dispatch) => {
  const { $from } = state.selection
  if ($from.parent.type !== schema.nodes.element) return false
  const doc = state.doc
  let pos = $from.before()
  let splittable = $from.depth === 1
  if ($from.depth > 1) {
    pos = $from.before(1) + 2 // first element of the left column
    splittable = false
  } else if (inDialogue(elementType($from.parent))) {
    const block = dialogueBlockAt(doc, $from.index(0))
    if (block) {
      pos = 0
      for (let k = 0; k < block[0]; k++) pos += doc.child(k).nodeSize
      splittable = false
    }
  }
  const node = doc.nodeAt(pos)!
  if (dispatch) {
    const tr = state.tr
    if (!state.selection.empty) tr.deleteSelection()
    const $pos = tr.selection.$from
    const type = elementType(node)
    if (splittable && $pos.parentOffset > 0 && !isBlank(node)) {
      if ($pos.parentOffset === $pos.parent.content.size) {
        const after = pos + $pos.parent.nodeSize
        tr.insert(after, schema.nodes.element.create({ type: NEXT_ON_ENTER[type], newPage: true }))
        tr.setSelection(TextSelection.create(tr.doc, after + 1))
      } else {
        const attrs = { type, sceneNumber: '', level: node.attrs.level, newPage: true }
        tr.split($pos.pos, 1, [{ type: schema.nodes.element, attrs }])
      }
    } else {
      tr.setNodeMarkup(pos, undefined, { ...node.attrs, newPage: !node.attrs.newPage })
    }
    dispatch(tr.scrollIntoView())
  }
  return true
}

const hardBreak: Command =(state, dispatch) => {
  if (dispatch) dispatch(state.tr.replaceSelectionWith(schema.nodes.hard_break.create()).scrollIntoView())
  return true
}

export function screenplayKeys(): Record<string, Command> {
  const keys: Record<string, Command> = {
    Enter: enter,
    'Shift-Enter': hardBreak,
    'Mod-Enter': pageBreak,
    Tab: cycle(TAB),
    'Shift-Tab': cycle(SHIFT_TAB),
    'Mod-b': toggleMark(schema.marks.bold),
    'Mod-i': toggleMark(schema.marks.italic),
    'Mod-u': toggleMark(schema.marks.underline),
    'Mod-Shift-n': toggleMark(schema.marks.note),
    'Mod-z': undo,
    'Mod-Shift-z': redo,
    'Mod-y': redo,
    'Mod-d': toggleDual,
  }
  ELEMENT_TYPES.slice(0, 9).forEach((type, i) => {
    keys[`Mod-${i + 1}`] = setType(type)
  })
  return keys
}

/**
 * Auto-detection while typing: "INT. " in an action becomes a scene heading,
 * "INSERT " an insert, "# " (or "## "…) a section and "= " a synopsis, and "("
 * at the start of an empty dialogue opens a parenthetical.
 */
export function autoFormatPlugin(): Plugin {
  return new Plugin({
    props: {
      handleTextInput(view, from, to, text) {
        const { $from } = view.state.selection
        const node = $from.parent
        if (text === '(' && elementType(node) === 'dialogue' && node.content.size === 0 && from === to) {
          view.dispatch(retype(view.state.tr, $from.before(), 'parenthetical').scrollIntoView())
          return true
        }
        return false
      },
    },
    appendTransaction(trs, _old, state: EditorState) {
      if (!trs.some((t) => t.docChanged)) return null
      const { $from } = state.selection
      const node = $from.parent
      if (node.type !== schema.nodes.element) return null
      const current = elementType(node)
      const text = blockText(node)
      if (current === 'action' || current === 'scene') {
        const section = SECTION_PREFIX.exec(text)
        if (section || SYNOPSIS_PREFIX.test(text)) {
          const pos = $from.before()
          const attrs = { ...node.attrs, type: section ? 'section' : 'synopsis', level: section?.[1].length ?? 1 }
          return state.tr.setNodeMarkup(pos, undefined, attrs).delete(pos + 1, pos + 1 + node.content.size)
        }
      }
      if (current !== 'action') return null
      const type = SCENE_PREFIX.test(text) ? 'scene' : SHOT_PREFIX.test(text) ? 'shot' : null
      if (!type) return null
      return state.tr.setNodeMarkup($from.before(), undefined, { ...node.attrs, type })
    },
  })
}
