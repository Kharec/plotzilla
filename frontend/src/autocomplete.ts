// Inline suggestions for character cues, scene headings and transitions,
// drawn from what the script already contains.
import { EditorState, Plugin } from 'prosemirror-state'
import { EditorView } from 'prosemirror-view'
import { blockText, elementType, flatElements, printedText } from './schema'

const SCENE_PREFIXES = ['INT. ', 'EXT. ', 'INT./EXT. ', 'EST. ']
const TIMES = ['DAY', 'NIGHT', 'CONTINUOUS', 'LATER', 'MOMENTS LATER', 'MORNING', 'EVENING', 'DUSK', 'DAWN', 'SAME']
const EXTENSIONS = ["V.O.", 'O.S.', 'O.C.', "CONT'D"]
const TRANSITIONS = ['CUT TO:', 'FADE OUT.', 'FADE TO:', 'DISSOLVE TO:', 'SMASH CUT TO:', 'MATCH CUT TO:', 'JUMP CUT TO:', 'FADE TO BLACK.']
const SHOTS = ['INSERT - ', 'CLOSE ON ', 'ANGLE ON ', 'POV - ', 'WIDE ON ', 'EXTREME CLOSE UP ON ', 'BACK TO SCENE']
const PREFIX_RE = /^(INT\.\/EXT\.|INT\/EXT|I\/E\.?|INT\.|EXT\.|EST\.)\s*/

interface Suggestions {
  items: string[]
  from: number
  to: number
  key: string
}

function byFrequency(values: string[], exclude: string): string[] {
  const counts = new Map<string, number>()
  for (const v of values) if (v && v !== exclude) counts.set(v, (counts.get(v) ?? 0) + 1)
  return [...counts.entries()].sort((a, b) => b[1] - a[1]).map(([v]) => v)
}

function collect(state: EditorState, skip: number): { names: string[]; locations: string[]; times: string[] } {
  const names: string[] = []
  const locations: string[] = []
  const times: string[] = []
  flatElements(state.doc).forEach(({ node, pos }) => {
    if (pos === skip) return
    const text = printedText(node).toUpperCase().trim()
    const type = elementType(node)
    if (type === 'character') {
      names.push(text.replace(/\^$/, '').replace(/\(.*$/, '').trim())
    } else if (type === 'scene') {
      const rest = text.replace(PREFIX_RE, '')
      const dash = rest.lastIndexOf(' - ')
      locations.push((dash >= 0 ? rest.slice(0, dash) : rest).trim())
      if (dash >= 0) times.push(rest.slice(dash + 3).trim())
    }
  })
  return { names, locations, times }
}

function compute(state: EditorState): Suggestions | null {
  const { $from, empty } = state.selection
  const node = $from.parent
  if (!empty || $from.parentOffset !== node.content.size) return null
  const type = elementType(node)
  const raw = blockText(node)
  const text = raw.toUpperCase()
  const start = $from.start()
  const skip = $from.before()
  const make = (items: string[], offset: number): Suggestions | null =>
    items.length ? { items: items.slice(0, 8), from: start + offset, to: start + raw.length, key: `${skip}:${raw}` } : null

  if (type === 'character' && text.trim()) {
    const paren = text.lastIndexOf('(')
    if (paren >= 0 && !text.includes(')', paren)) {
      const typed = text.slice(paren + 1)
      return make(
        EXTENSIONS.filter((e) => e.startsWith(typed) && e !== typed).map((e) => e + ')'),
        paren + 1,
      )
    }
    const names = byFrequency(collect(state, skip).names, '')
    return make(names.filter((n) => n.startsWith(text) && n !== text), 0)
  }

  if (type === 'scene' && text.trim()) {
    const prefix = text.match(PREFIX_RE)
    if (!prefix) {
      return make(SCENE_PREFIXES.filter((p) => p.startsWith(text) && p !== text), 0)
    }
    const rest = text.slice(prefix[0].length)
    const dash = rest.lastIndexOf(' - ')
    const { locations, times } = collect(state, skip)
    if (dash < 0) {
      if (!rest) return null
      const locs = byFrequency(locations, '').filter((l) => l.startsWith(rest) && l !== rest)
      return make(locs.map((l) => l + ' - '), prefix[0].length)
    }
    const typed = rest.slice(dash + 3)
    const all = [...new Set([...TIMES, ...byFrequency(times, '')])]
    return make(all.filter((t) => t.startsWith(typed) && t !== typed), prefix[0].length + dash + 3)
  }

  if (type === 'shot' && text.trim()) {
    return make(SHOTS.filter((s) => s.startsWith(text) && s !== text), 0)
  }

  if (type === 'transition' && text.trim()) {
    return make(TRANSITIONS.filter((t) => t.startsWith(text) && t !== text), 0)
  }
  return null
}

export function autocompletePlugin(): Plugin {
  const popup = document.createElement('ul')
  popup.className = 'autocomplete'
  popup.hidden = true
  let current: Suggestions | null = null
  let index = 0
  let dismissed = ''

  const render = (view: EditorView) => {
    if (!current || !view.hasFocus()) {
      popup.hidden = true
      return
    }
    popup.replaceChildren(
      ...current.items.map((item, i) => {
        const li = document.createElement('li')
        li.textContent = item
        li.classList.toggle('active', i === index)
        li.addEventListener('mousedown', (e) => {
          e.preventDefault()
          index = i
          accept(view)
        })
        return li
      }),
    )
    const coords = view.coordsAtPos(current.from)
    popup.style.left = `${coords.left}px`
    popup.style.top = `${coords.bottom + 4}px`
    popup.hidden = false
  }

  const accept = (view: EditorView) => {
    if (!current) return
    const { from, to, items } = current
    view.dispatch(view.state.tr.insertText(items[index], from, to).scrollIntoView())
  }

  // The popup is position: fixed, so it must follow the script when it scrolls.
  let currentView: EditorView | null = null
  const reposition = () => {
    if (currentView && !popup.hidden) render(currentView)
  }

  return new Plugin({
    view(view) {
      currentView = view
      document.body.appendChild(popup)
      document.addEventListener('scroll', reposition, true)
      return {
        update(view) {
          const next = compute(view.state)
          if (next && next.key === dismissed) current = null
          else {
            if (!current || !next || next.key !== current.key) index = 0
            current = next
          }
          render(view)
        },
        destroy() {
          document.removeEventListener('scroll', reposition, true)
          currentView = null
          popup.remove()
        },
      }
    },
    props: {
      handleKeyDown(view, event) {
        if (!current || popup.hidden) return false
        switch (event.key) {
          case 'ArrowDown':
            index = (index + 1) % current.items.length
            render(view)
            return true
          case 'ArrowUp':
            index = (index - 1 + current.items.length) % current.items.length
            render(view)
            return true
          case 'Enter':
          case 'Tab':
            if (event.shiftKey) return false
            accept(view)
            return true
          case 'Escape':
            dismissed = current.key
            current = null
            render(view)
            return true
        }
        return false
      },
      handleDOMEvents: {
        blur() {
          popup.hidden = true
          return false
        },
      },
    },
  })
}
