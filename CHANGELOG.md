# Changelog

All notable changes to Plotzilla are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The current
version is in the `VERSION` file.

## [Unreleased]

### Added

- The PDF export warns when the script contains characters the standard PDF fonts
  cannot draw: they are written as `?` in the file, and are now listed in a dialog
  instead of being silently lost.

### Fixed

- The unsaved-changes marker is now updated atomically: an edit landing while a save
  finished could make the close confirmation unreliable.
- Saving is now durable: the file is synced to disk before it replaces the old one (a
  power cut just after a save could leave an empty script). An existing file keeps its
  permissions, and saving through a symlink replaces the file it points to instead of
  replacing the link.
- Translation is faster and uses much less memory: the beam search no longer copies
  every beam's decoder state for every candidate at every step, and reuses its
  work buffers instead of allocating new ones per layer and step.

## [1.4] - 2026-10-15

### Fixed

- The Cancel button responds while the translation model is loading.
- The translation model download resumes where it stopped after an interruption, skips files
  already downloaded, and reports a stalled connection instead of hanging. A model directory
  with missing or truncated files is offered for download again instead of failing to load.

## [1.3] - 2026-10-01

### Added

- Translation of a script, or of the selection, from French to English (`Ctrl+Shift+L`),
  offline: the OPUS-MT model runs in Go, downloaded once on demand. Sluglines, character
  extensions, parentheticals, transitions and shots follow English script conventions;
  character names and text already in English are left as they are.

## [1.2] - 2026-09-29

### Added

- Arch Linux PKGBUILD (`packaging/arch`).

### Fixed

- Align VERSION on tag

## [1.1] - 2026-09-29

### Added

- Application icon, used for the window, the launcher, the favicon and the header bar logo.
- `make install` / `make uninstall`: the binary, a desktop entry, the icon, and MIME types
  so file managers open `.fountain` and `.fdx` files with Plotzilla.

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
