// Popup menus opened at a point (context menu) or under a button, with
// keyboard navigation. Only one is open at a time.
import { icon } from './icons'

export interface MenuItem {
  label?: string
  action?: () => void
  checked?: boolean // a check mark column is drawn when any item sets it
  disabled?: boolean
  hint?: string // right-aligned, like a shortcut
  strong?: boolean
  separator?: boolean
}

const menu = document.createElement('ul')
menu.className = 'menu popup'
menu.hidden = true
document.body.appendChild(menu)

let entries: { li: HTMLLIElement; item: MenuItem }[] = []
let index = -1
let anchor: Element | null = null
let onClose: (() => void) | null = null

export function menuOpen(): boolean {
  return !menu.hidden
}

export function closeMenu() {
  if (menu.hidden) return
  menu.hidden = true
  anchor = null
  const done = onClose
  onClose = null
  done?.()
}

function highlight(i: number) {
  index = i
  entries.forEach((e, k) => e.li.classList.toggle('active', k === i))
}

function choose(item: MenuItem) {
  closeMenu()
  item.action?.()
}

/**
 * Opens a menu with its top-left corner at (x, y), kept inside the window.
 * A click on `from` (the button that opened it) is left to that button,
 * so it can close the menu again.
 */
export function openMenu(items: MenuItem[], x: number, y: number, opts: { from?: Element; onClose?: () => void } = {}) {
  closeMenu()
  const checks = items.some((i) => i.checked !== undefined)
  entries = []
  menu.replaceChildren(
    ...items.map((item) => {
      const li = document.createElement('li')
      if (item.separator) {
        li.className = 'sep'
        return li
      }
      li.classList.toggle('disabled', !!item.disabled)
      li.classList.toggle('strong', !!item.strong)
      if (checks) {
        const mark = document.createElement('span')
        mark.className = 'check'
        if (item.checked) mark.innerHTML = icon('check')
        li.appendChild(mark)
      }
      li.append(item.label ?? '')
      if (item.hint) {
        const kbd = document.createElement('kbd')
        kbd.textContent = item.hint
        li.appendChild(kbd)
      }
      if (!item.disabled) {
        const k = entries.length
        entries.push({ li, item })
        li.addEventListener('mousedown', (e) => {
          e.preventDefault()
          choose(item)
        })
        li.addEventListener('mousemove', () => highlight(k))
      }
      return li
    }),
  )
  anchor = opts.from ?? null
  onClose = opts.onClose ?? null
  index = -1
  menu.hidden = false
  const rect = menu.getBoundingClientRect()
  menu.style.left = `${Math.max(8, Math.min(x, innerWidth - rect.width - 8))}px`
  menu.style.top = `${y + rect.height > innerHeight - 8 ? Math.max(8, y - rect.height) : y}px`
}

document.addEventListener('mousedown', (e) => {
  const target = e.target as Node
  if (!menu.hidden && !menu.contains(target) && !anchor?.contains(target)) closeMenu()
})

window.addEventListener('blur', closeMenu)
document.addEventListener('scroll', closeMenu, true)

window.addEventListener(
  'keydown',
  (e) => {
    if (menu.hidden) return
    const n = entries.length
    const keys: Record<string, () => void> = {
      ArrowDown: () => n && highlight((index + 1) % n),
      ArrowUp: () => n && highlight((index - 1 + n) % n),
      Enter: () => (index >= 0 ? choose(entries[index].item) : closeMenu()),
      Escape: closeMenu,
    }
    if (!keys[e.key]) return
    e.preventDefault()
    e.stopPropagation()
    keys[e.key]()
  },
  true,
)
