import { Schema, Node as PMNode, Fragment, Mark } from 'prosemirror-model'
import type { Element, ElementType, PageLock, Revision, Run, ScriptDocument, Settings, TitlePage } from './backend'

/** The first nine have the shortcuts Ctrl+1…9, in this order. */
export const ELEMENT_TYPES: ElementType[] = [
  'scene',
  'action',
  'character',
  'parenthetical',
  'dialogue',
  'transition',
  'shot',
  'intertitle',
  'centered',
  'section',
  'synopsis',
]

/** Outline elements: part of the script file, never printed. */
export const isPrinted = (t: ElementType) => t !== 'section' && t !== 'synopsis'

export const LABELS: Record<ElementType, string> = {
  scene: 'Scene Heading',
  action: 'Action',
  character: 'Character',
  parenthetical: 'Parenthetical',
  dialogue: 'Dialogue',
  transition: 'Transition',
  shot: 'Insert / Shot',
  intertitle: 'Intertitle',
  centered: 'Centered',
  section: 'Section',
  synopsis: 'Synopsis',
}

/** A locked page in the editor: its offset is in UTF-16 units (positions). */
export interface LockAttr {
  offset: number
  page: string
}

// One node type for every screenplay element: switching type is then a
// simple attribute change, which keeps Tab/Enter cycling cheap. A dual
// dialogue is a "dual" node holding two columns of elements.
export const schema = new Schema({
  nodes: {
    doc: { content: '(element | dual)+' },
    element: {
      content: 'inline*',
      marks: '_',
      defining: true,
      // locked: LockAttr[] | null, the locked pages starting in the element.
      attrs: {
        type: { default: 'action' },
        sceneNumber: { default: '' },
        level: { default: 1 },
        newPage: { default: false },
        locked: { default: null },
      },
      parseDOM: [
        {
          tag: 'p',
          // Locked pages are not copied: a page starts in one place only.
          getAttrs: (dom) => {
            const el = dom as HTMLElement
            const type = el.dataset.type as ElementType | undefined
            return {
              type: type && ELEMENT_TYPES.includes(type) ? type : 'action',
              sceneNumber: el.dataset.sceneNumber ?? '',
              level: Math.max(1, Number(el.dataset.level) || 1),
              newPage: el.dataset.newPage === 'true',
            }
          },
        },
      ],
      toDOM: (node) => [
        'p',
        {
          class: `el el-${node.attrs.type}`,
          'data-type': node.attrs.type,
          ...(node.attrs.sceneNumber ? { 'data-scene-number': node.attrs.sceneNumber } : {}),
          ...(node.attrs.type === 'section' ? { 'data-level': String(node.attrs.level) } : {}),
          ...(node.attrs.newPage ? { 'data-new-page': 'true' } : {}),
        },
        0,
      ],
    },
    dual: {
      content: 'column column',
      isolating: true,
      parseDOM: [{ tag: 'div.dual' }],
      toDOM: () => ['div', { class: 'dual' }, 0],
    },
    column: {
      content: 'element+',
      isolating: true,
      parseDOM: [{ tag: 'div.dual-col' }],
      toDOM: () => ['div', { class: 'dual-col' }, 0],
    },
    text: { group: 'inline' },
    hard_break: {
      inline: true,
      group: 'inline',
      selectable: false,
      leafText: () => '\n',
      parseDOM: [{ tag: 'br' }],
      toDOM: () => ['br'],
    },
  },
  marks: {
    bold: { parseDOM: [{ tag: 'strong' }, { tag: 'b' }], toDOM: () => ['strong', 0] },
    italic: { parseDOM: [{ tag: 'em' }, { tag: 'i' }], toDOM: () => ['em', 0] },
    underline: { parseDOM: [{ tag: 'u' }], toDOM: () => ['u', 0] },
    note: { parseDOM: [{ tag: 'span.note' }], toDOM: () => ['span', { class: 'note' }, 0] },
    // Text of revision set `level`. Not inclusive: typing next to revised
    // text is only marked while revisions are being marked.
    revision: {
      attrs: { level: { default: 1 }, color: { default: 'blue' } },
      inclusive: false,
      parseDOM: [
        {
          tag: 'span.rev',
          getAttrs: (dom) => {
            const el = dom as HTMLElement
            return { level: Math.max(1, Number(el.dataset.level) || 1), color: el.dataset.color ?? 'blue' }
          },
        },
      ],
      toDOM: (mark) => ['span', { class: 'rev', 'data-level': String(mark.attrs.level), 'data-color': mark.attrs.color }, 0],
    },
  },
})

export function elementType(node: PMNode): ElementType {
  return node.attrs.type as ElementType
}

/** Plain text of an element, with hard breaks as "\n" (matches the Go side). */
export function blockText(node: PMNode): string {
  return node.textBetween(0, node.content.size, '\n', '\n')
}

/** The printed text of an element: without its notes. */
export function printedText(node: PMNode): string {
  let text = ''
  node.forEach((child) => {
    if (!schema.marks.note.isInSet(child.marks)) text += child.isText ? child.text! : '\n'
  })
  return text
}

/** Converts a rune offset (Go) into a UTF-16 offset (JS) within text. */
export function utf16Offset(text: string, runes: number): number {
  let i = 0
  for (const ch of text) {
    if (runes-- <= 0) break
    i += ch.length
  }
  return i
}

/** Converts a UTF-16 offset within text into a rune offset (Go). */
export function runeOffset(text: string, utf16: number): number {
  return [...text.slice(0, utf16)].length
}

export interface FlatElement {
  node: PMNode
  pos: number
  /** Position of the enclosing dual node, for elements of a dual dialogue. */
  dualPos: number | null
  /** Set on the first element of the right column. */
  dual: boolean
}

/** Elements in document order, the same indexing as the Go side uses. */
export function flatElements(doc: PMNode): FlatElement[] {
  const out: FlatElement[] = []
  doc.forEach((node, offset) => {
    if (node.type !== schema.nodes.dual) {
      out.push({ node, pos: offset, dualPos: null, dual: false })
      return
    }
    node.forEach((col, colOffset, colIndex) => {
      col.forEach((el, elOffset, elIndex) => {
        const pos = offset + 1 + colOffset + 1 + elOffset
        out.push({ node: el, pos, dualPos: offset, dual: colIndex === 1 && elIndex === 0 })
      })
    })
  })
  return out
}

const sameStyle = (a: Run, b: Run) =>
  !!a.b === !!b.b && !!a.i === !!b.i && !!a.u === !!b.u && !!a.n === !!b.n && (a.rev ?? 0) === (b.rev ?? 0)

function runsOf(node: PMNode): Run[] {
  const runs: Run[] = []
  node.forEach((child) => {
    const run: Run = { text: child.isText ? child.text! : '\n' }
    for (const m of child.marks) {
      if (m.type === schema.marks.bold) run.b = true
      if (m.type === schema.marks.italic) run.i = true
      if (m.type === schema.marks.underline) run.u = true
      if (m.type === schema.marks.note) run.n = true
      if (m.type === schema.marks.revision) run.rev = m.attrs.level
    }
    const last = runs[runs.length - 1]
    if (last && sameStyle(last, run)) {
      last.text += run.text
    } else {
      runs.push(run)
    }
  })
  return runs
}

export function toElement(node: PMNode): Element {
  const el: Element = { type: elementType(node), runs: runsOf(node) }
  if (node.attrs.sceneNumber) el.sceneNumber = node.attrs.sceneNumber
  if (el.type === 'section') el.level = node.attrs.level
  if (node.attrs.newPage) el.newPage = true
  const locked = node.attrs.locked as LockAttr[] | null
  if (locked?.length) {
    const text = blockText(node)
    el.locked = locked.map((l): PageLock => ({ offset: runeOffset(text, l.offset), page: l.page }))
  }
  return el
}

export function toScript(doc: PMNode, titlePage: TitlePage, settings: Settings): ScriptDocument {
  const elements = flatElements(doc).map(({ node, dual }) => {
    const el = toElement(node)
    if (dual) el.dual = true
    return el
  })
  return { titlePage, elements, settings }
}

/** revisions names the colour of revised text (Run.rev). */
export function fromElement(el: Element, revisions: Revision[] = []): PMNode {
  const inline: PMNode[] = []
  let text = ''
  for (const run of el.runs ?? []) {
    const marks: Mark[] = []
    if (run.b) marks.push(schema.marks.bold.create())
    if (run.i) marks.push(schema.marks.italic.create())
    if (run.u) marks.push(schema.marks.underline.create())
    if (run.n) marks.push(schema.marks.note.create())
    if (run.rev) marks.push(schema.marks.revision.create({ level: run.rev, color: revisions[run.rev - 1]?.color ?? 'blue' }))
    // A line break keeps the note or revision it belongs to, not emphasis.
    const breakMarks = marks.filter((m) => m.type === schema.marks.note || m.type === schema.marks.revision)
    run.text.split('\n').forEach((part, i) => {
      if (i > 0) inline.push(schema.nodes.hard_break.create(null, null, breakMarks))
      if (part) inline.push(schema.text(part, marks))
    })
    text += run.text
  }
  const type = ELEMENT_TYPES.includes(el.type) ? el.type : 'action'
  const locked = el.locked?.length
    ? el.locked.map((l): LockAttr => ({ offset: utf16Offset(text, l.offset), page: l.page }))
    : null
  return schema.nodes.element.create(
    { type, sceneNumber: el.sceneNumber ?? '', level: Math.max(1, el.level ?? 1), newPage: !!el.newPage, locked },
    inline,
  )
}

export function dualNode(left: PMNode[], right: PMNode[]): PMNode {
  return schema.nodes.dual.create(null, [
    schema.nodes.column.create(null, left),
    schema.nodes.column.create(null, right),
  ])
}

const inDialogue = (t: ElementType) => t === 'parenthetical' || t === 'dialogue'

export function fromScript(script: ScriptDocument): PMNode {
  const els = script.elements ?? []
  const revisions = script.settings?.revisions ?? []
  const node = (el: Element) => fromElement(el, revisions)
  const blocks: PMNode[] = []
  let blockStart = -1 // index in blocks of the last top-level dialogue block
  for (let i = 0; i < els.length; i++) {
    const el = els[i]
    if (el.type === 'character' && el.dual && blockStart >= 0) {
      const right = [node(el)]
      while (i + 1 < els.length && inDialogue(els[i + 1].type)) right.push(node(els[++i]))
      blocks.push(dualNode(blocks.splice(blockStart), right))
      blockStart = -1
      continue
    }
    if (el.type === 'character') blockStart = blocks.length
    else if (!inDialogue(el.type)) blockStart = -1
    blocks.push(node(el))
  }
  if (blocks.length === 0) blocks.push(schema.nodes.element.create({ type: 'scene' }))
  return schema.nodes.doc.create(null, Fragment.fromArray(blocks))
}
