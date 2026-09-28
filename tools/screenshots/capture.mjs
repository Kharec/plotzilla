// Drives Firefox (WebDriver BiDi, through puppeteer-core) to capture the
// README screenshots. Run by main.go, which serves the app at $URL and
// passes $OUT (where to write the PNGs), $PROFILE and $FIREFOX.
import puppeteer from 'puppeteer-core'

const { URL: base, OUT, PROFILE, FIREFOX } = process.env
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const browser = await puppeteer.launch({ browser: 'firefox', executablePath: FIREFOX, headless: true, userDataDir: PROFILE })

/** Opens the app on a script variant, with fresh preferences. */
async function open(variant = '') {
  const page = await browser.newPage()
  await page.setViewport({ width: 1440, height: 900, deviceScaleFactor: 2 })
  page.on('pageerror', (e) => console.error('page error:', e.message))
  const url = base + (variant ? `?v=${variant}` : '')
  await page.goto(url)
  await page.evaluate(() => localStorage.clear())
  await page.goto(url)
  await sleep(1200) // pagination and spelling
  await page.evaluate(() => { document.getElementById('scroller').style.scrollBehavior = 'auto' })
  return page
}

const ctrl = async (page, key, shift = false) => {
  const k = page.keyboard
  await k.down('Control')
  if (shift) await k.down('Shift')
  await k.press(key)
  if (shift) await k.up('Shift')
  await k.up('Control')
}

/** Clicks the first element matching sel whose text contains text. */
async function clickText(page, sel, text) {
  for (const el of await page.$$(sel)) if ((await el.evaluate((e) => e.textContent)).includes(text)) return el.click()
  throw new Error(`no ${sel} containing "${text}"`)
}

/** Puts the cursor at the end of the first element containing text. */
async function caretIn(page, text) {
  await page.evaluate((text) => {
    const el = [...document.querySelectorAll('.ProseMirror .el')].find((e) => e.textContent.includes(text))
    const r = document.createRange()
    r.selectNodeContents(el)
    r.collapse(false)
    getSelection().removeAllRanges()
    getSelection().addRange(r)
    document.querySelector('.ProseMirror').focus()
  }, text)
  await sleep(100)
  await page.keyboard.press('End')
}

/** Selects the first occurrence of text in the script. */
async function select(page, text) {
  await page.evaluate((text) => {
    const walker = document.createTreeWalker(document.querySelector('.ProseMirror'), NodeFilter.SHOW_TEXT)
    for (let n; (n = walker.nextNode()); ) {
      const i = n.textContent.indexOf(text)
      if (i < 0) continue
      const r = document.createRange()
      r.setStart(n, i)
      r.setEnd(n, i + text.length)
      getSelection().removeAllRanges()
      getSelection().addRange(r)
      return
    }
  }, text)
}

const scrollTo = (page, y) => page.evaluate((y) => { document.getElementById('scroller').scrollTop = y }, y)

/** Captures the window, the pointer off the header (tooltips). */
async function shot(page, name, pointer = true) {
  if (pointer) await page.mouse.move(1430, 600)
  await sleep(400)
  await page.screenshot({ path: `${OUT}/${name}.png` })
  console.log('captured', name)
}

let page = await open()
await caretIn(page, 'It always ends')
await scrollTo(page, 0)
await shot(page, 'editor')

await ctrl(page, 'F', true)
await shot(page, 'focus-mode')
await ctrl(page, 'F', true)

await caretIn(page, 'FADE IN:')
await scrollTo(page, 0)
await page.evaluate(() => document.getElementById('type-button').dispatchEvent(new MouseEvent('mousedown', { bubbles: true })))
await shot(page, 'element-menu')
await page.keyboard.press('Escape')

await caretIn(page, 'The film snaps')
await page.keyboard.press('Enter')
await page.keyboard.press('Tab')
await page.keyboard.type('EL')
await scrollTo(page, 0)
await shot(page, 'autocomplete')
await page.keyboard.press('Escape')
for (let i = 0; i < 4; i++) await ctrl(page, 'z')
await sleep(1500)

await caretIn(page, 'THE END')
await page.keyboard.press('Enter')
await page.keyboard.type('INT. ')
await page.keyboard.type('PROJECTION BOOTH - ')
await sleep(1600) // autosave: "Saved · auto"
await page.evaluate(() => { const s = document.getElementById('scroller'); s.scrollTop = s.scrollHeight })
await shot(page, 'scene-autocomplete')
await page.close()

page = await open()
await caretIn(page, 'FADE IN:')
await scrollTo(page, 0)
await ctrl(page, 'h')
await sleep(100)
await page.keyboard.type('reel')
await page.keyboard.press('Enter')
await page.click('#replace-input')
await page.keyboard.type('spool')
await scrollTo(page, 0)
await shot(page, 'find-replace')
await page.close()

page = await open()
await caretIn(page, 'It always ends')
await page.evaluate(() => {
  const s = document.getElementById('scroller')
  s.scrollTop += document.querySelector('.page-break').getBoundingClientRect().top - 390
})
await shot(page, 'page-break')

await caretIn(page, 'FADE IN:')
await scrollTo(page, 0)
await ctrl(page, 'T', true)
await shot(page, 'title-page')
await page.keyboard.press('Escape')
await sleep(100)

await caretIn(page, 'FADE IN:')
await scrollTo(page, 0)
await ctrl(page, 'p')
await shot(page, 'export-pdf')
await page.keyboard.press('Escape')
await sleep(100)

await page.click('.switch') // autosave off, so the change stays unsaved
await caretIn(page, 'line every shelf.')
await page.keyboard.type(' Then silence.')
await scrollTo(page, 0)
await ctrl(page, 'n')
await shot(page, 'unsaved-changes')
await page.close()

page = await open('typos')
await caretIn(page, 'FADE IN:')
await scrollTo(page, 0)
await sleep(700)
const typo = await page.evaluate(() => {
  const r = document.querySelector('.misspelled').getBoundingClientRect()
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 }
})
await page.mouse.click(typo.x, typo.y, { button: 'right' })
await shot(page, 'spelling', false)
await page.close()

page = await open('outline')
await caretIn(page, 'Warmer light')
await scrollTo(page, 0)
await shot(page, 'outline')

// Locked pages, a Blue revision, then a Pink one, with the menu open.
await page.click('[data-action="revisions"]')
await sleep(150)
await clickText(page, 'ul.menu.popup li', 'Lock Pages')
await sleep(500)
await page.click('[data-action="revisions"]')
await sleep(150)
await clickText(page, 'ul.menu.popup li', 'Start Revisions')
await sleep(300)
await caretIn(page, 'line every shelf.')
await page.keyboard.type(' The lamp buzzes.')
await caretIn(page, 'every single night.')
await page.keyboard.press('Enter')
await page.keyboard.type('Then it starts again.')
await page.click('[data-action="revisions"]')
await sleep(150)
await clickText(page, 'ul.menu.popup li', 'New Revision Set')
await sleep(300)
await select(page, 'sleeves rolled up')
await page.keyboard.type('coat still on')
await sleep(900)
await scrollTo(page, 0)
await page.click('[data-action="revisions"]')
await sleep(250)
await shot(page, 'revisions')

await browser.close()
