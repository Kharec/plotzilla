# Changelog

All notable changes to Plotzilla are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The current
version is in the `VERSION` file.

## [1.0] - 2026-09-28

### Added

- Screenplay editor on a page drawn at the real printed size, with scene heading, action,
  character, parenthetical, dialogue, transition, insert/shot, intertitle and centered elements
  (`Ctrl+1` to `Ctrl+9`).
- Final Draft-style typing: `Enter` creates the next logical element, `Tab` cycles the type,
  scene headings, inserts, parentheticals and transitions are recognized as you type.
- Dual dialogue (`Ctrl+D`), bold, italic and underline.
- Autocomplete for character names, extensions, locations, times of day, inserts and
  transitions.
- Live page breaks computed by the PDF layout engine, with `(MORE)` / `(CONT'D)` and the usual
  pagination rules.
- Scene navigator with scene numbers and pages; focus mode; zoom.
- Find and replace, with match case and whole words.
- Spell checking with the system's Hunspell dictionaries, including compound words (with
  `COMPOUNDRULE` patterns), a personal dictionary and screenplay-aware rules (character names,
  sluglines, abbreviations).
- Title page editor.
- Fountain as the native format; Final Draft (`.fdx`) import and export; PDF export in US Letter
  or A4, with optional title page and scene numbers.
- Fountain sections, synopses and notes, shown in the editor and in the navigator outline,
  never printed.
- Colored revisions (Blue, Pink, Yellow…) with revision headers and asterisks in the PDF.
- Locked pages, with A pages (`12A`) and merged pages (`12-13`).
- Forced page breaks (`Ctrl+Enter`), read and written as Fountain's `===` and Final Draft's
  `StartsNewPage`.
- Sections, synopses, notes and revisions in Final Draft files.
- Autosave, with recovery of untitled scripts, and a confirmation before discarding changes.
- Frameless window with an in-app header bar, English interface whatever the system language.

### Changed

- The window no longer depends on GNOME: window buttons follow the desktop's GTK button layout,
  and the light/dark preference is read from the XDG desktop portal (GNOME setting as fallback).

### Fixed

- File choosers no longer switch to light mode on KDE Plasma and other non-GNOME desktops.
- Window corners stay square instead of showing black wedges when no compositor is running.
