// View decorations: live page breaks computed by the Go layout engine, and a
// placeholder naming the type of the empty element under the cursor.
import { Plugin, PluginKey } from 'prosemirror-state'
import { Decoration, DecorationSet } from 'prosemirror-view'
import { Node as PMNode } from 'prosemirror-model'
import type { PageBreak } from './backend'
import { LABELS, blockText, elementType, flatElements, schema, utf16Offset } from './schema'

export { utf16Offset }

export const pagesKey = new PluginKey<DecorationSet>('pages')

function pageWidget(page: string, inline: boolean, type: string, forced = false): HTMLElement {
  const el = document.createElement(inline ? 'span' : 'div')
  el.className = `page-break${inline ? ` inline in-${type}` : ''}${forced ? ' forced' : ''}`
  el.contentEditable = 'false'
  if (forced) {
    const tag = document.createElement('span')
    tag.className = 'page-forced'
    tag.textContent = 'Page break'
    el.appendChild(tag)
  }
  const num = document.createElement('span')
  num.className = 'page-number'
  num.textContent = `${page}.`
  el.appendChild(num)
  return el
}

export function buildPageDecorations(doc: PMNode, breaks: PageBreak[]): DecorationSet {
  const flat = flatElements(doc)
  const decos: Decoration[] = []
  for (const br of breaks) {
    const entry = flat[br.element]
    if (!entry) continue
    const { node, pos, dualPos } = entry
    const type = elementType(node)
    const label = br.label || String(br.page)
    // A forced break (Ctrl+Enter) says so; the dual dialogue's is on its first element.
    const forced = br.offset === 0 && !!(dualPos !== null ? doc.nodeAt(dualPos + 2)?.attrs.newPage : node.attrs.newPage)
    const key = `pb${br.page}${forced ? 'f' : ''}`
    if (dualPos !== null) {
      // A dual dialogue is never split: the page starts above the whole block.
      decos.push(Decoration.widget(dualPos, () => pageWidget(label, false, type, forced), { side: -1, key }))
    } else if (br.offset === 0) {
      decos.push(Decoration.widget(pos, () => pageWidget(label, false, type, forced), { side: -1, key }))
    } else {
      const at = pos + 1 + utf16Offset(blockText(node), br.offset)
      decos.push(
        Decoration.widget(at, () => pageWidget(label, true, type), {
          side: -1,
          key: `pb${br.page}-${type}`,
          ignoreSelection: true,
        }),
      )
    }
  }
  return DecorationSet.create(doc, decos)
}

export function pagesPlugin(): Plugin<DecorationSet> {
  return new Plugin<DecorationSet>({
    key: pagesKey,
    state: {
      init: () => DecorationSet.empty,
      apply(tr, set) {
        const fresh = tr.getMeta(pagesKey) as DecorationSet | undefined
        return fresh ?? set.map(tr.mapping, tr.doc)
      },
    },
    props: {
      decorations: (state) => pagesKey.getState(state),
    },
  })
}

export function placeholderPlugin(): Plugin {
  return new Plugin({
    props: {
      decorations(state) {
        const { $from } = state.selection
        const node = $from.parent
        if (node.type !== schema.nodes.element || node.content.size > 0) return null
        const pos = $from.before()
        return DecorationSet.create(state.doc, [
          Decoration.node(pos, pos + node.nodeSize, {
            class: 'is-empty',
            'data-placeholder': LABELS[elementType(node)],
          }),
        ])
      },
    },
  })
}
