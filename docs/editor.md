# Editor design (presentations)

Route `/presentations/:id` (fullscreen: sidebar collapses to icons). Code in
`web/apps/admin/src/features/presentations/editor/`. Uses `@opensignage/render` for every
pixel of slide content (canvas, slide rail thumbnails, preview) – never a second renderer.

## State

- `EditorStore` (plain class + `useSyncExternalStore`): `doc: Presentation` (draft),
  `revision`, `slideId`, `selection: string[]` (element ids), `history` (undo/redo stacks of
  doc snapshots, max 200), `clipboard` (also mirrored to `localStorage` key `os.clipboard`
  so copy/paste works between presentations/tabs), view flags (zoom, grid, snap, guides,
  rulers).
- Commands: `apply(label, mutate)` = clone → mutate → validate cheap invariants → push
  undo. Continuous interactions (drag, resize, rotate, slider): `begin()` → `preview(mutate)`
  (no history) → `commit(label)` (one undo step) or `cancel()`.
- Unique z-order = array order; new/duplicated elements get unique ids (UUID from
  `crypto.randomUUID`, HTTPS is a secure context) and are appended at the top.
- Groups: `groupId` on elements; clicking selects the whole group, double-click enters it.

## Saving

Autosave 1.5 s after the last change: `PUT /api/v1/presentations/{id}/draft {revision, doc}`.
Status chip: Saved / Saving… / Unsaved / Error (retry). 409 conflict → own dialog: reload
(discard local) or overwrite (re-PUT with the server revision). Publishing:
`POST …/publish` after confirm dialog; Discard → `POST …/discard` after confirm (danger).
Leaving with unsaved changes flushes the save first (in-app navigation waits for it through
a router blocker; the app's own confirm dialog appears only if saving failed). Closing or
reloading the tab sends the pending save with `fetch(…, {keepalive: true})` – never the
browser's "leave page?" prompt. Saving never publishes.

## Layout

```
┌ Header: ← name  size  [status] [published]        [History] [Preview] [Discard] [Publish] ┐
│ Toolbar: one row of symbol buttons (names in tooltips)                                     │
├ Slide rail ┬──────────── Canvas (rulers, zoom, guides) ────────┬ Presentation│Slide│Element│Layers ┤
│ thumbnails │                                                    │ context panel                      │
└────────────┴────────────────────────────────────────────────────┴────────────────────────────────────┘
```

No ribbon with tabs and captions: everything that edits the selection lives in the panel,
the toolbar keeps the actions.

- **Toolbar** (one 42 px row, symbols with tooltips and screen reader labels): undo/redo ·
  cut/copy/paste/duplicate/delete · insert text, image, video, audio, logo, shapes ▾
  (rectangle, rounded, ellipse, line, arrow) · product list, product, clock, ticker · front,
  back, align ▾ (left/center/right/top/middle/bottom, distribute H/V), group, ungroup, copy to
  all slides · right: grid (+ size), snapping, rulers, zoom ▾ (fit, 25–200 %). Preview is in
  the header.
- **Panel tabs follow the selection:** nothing selected (click beside the slide, Esc on a
  slide) → **Presentation** (format/resolution, product typography); slide (click on its
  thumbnail or on an empty spot of it, Esc on an element) → **Slide** (name, duration,
  background, transition); element(s) → **Element**. **Layers** stays open until another tab
  is chosen. Choosing Presentation or Slide clears the element selection. The current slide
  in the rail is outlined; it is highlighted when its properties are shown.
- **Element:** name; for text, clock and ticker the text block – font (each name in its
  own font, coverage warning), size, B/I/U/S, case, color, highlight, paragraph (align,
  bullets/numbering while editing), line height, letter spacing, outline, shadow; then the
  type specific fields (vertical alignment, fill, padding, auto-shrink, fit, loop/muted,
  volume, shape fill/stroke/dash/radius, product list source/columns/fields/headings/gaps/
  price alignment/overrides, clock format/date, ticker source/text/speed/direction); last
  position, size, rotation, opacity, pinned. While a text is being edited the text block
  formats the marked characters and shows their state (e.g. bold only for a bold word);
  otherwise it formats the whole element. Several elements: the text block applies to all
  text elements among them.
- **Layers:** list (top first), drag to reorder, lock, hide, rename inline.
- **Context menu** (right click, context menu key, Shift+F10) on canvas/rail/layers/guides
  with the same commands; own design, keyboard operable, never the browser menu.
- **Rulers and guides:** rulers in design pixels follow zoom and scrolling; guides are dragged
  out of a ruler, moved, and removed by dropping them on a ruler or off the slide (or via the
  context menu). Elements snap to them. Guides are an editing aid, not part of the document:
  kept per presentation in this browser (`localStorage` key `os.editor.guides.<id>`).
- **Keyboard:** Ctrl+Z/Y, Ctrl+C/X/V/D, Del, arrows (1 px, Shift = 10 px), Ctrl+G /
  Ctrl+Shift+G, Esc (leave text → element → slide → presentation), Ctrl+A.

## Canvas interaction

The slide is rendered with `renderElement(..., mode 'edit')` into a canvas scaled with
`--s`; an overlay (absolute, same size) draws selection boxes, 8 resize handles + rotate
handle, marquee selection, snap guides. Text selection on the page is disabled while
dragging. Clicking an empty spot of the slide selects the slide, clicking beside it selects
nothing (the presentation).

## Text editing

Double-click a text element → a ProseMirror view replaces the element's DOM in place with
the same box and base style. Schema: `doc > paragraph+ (attrs align, bullet) > text*`,
marks: bold, italic, underline, strike, color, highlight, font, size, letterSpacing.
Conversion PM ↔ `RichDoc` is lossless (unit tested). Toolbar formatting applies to the
selected text when editing, otherwise to the whole element (`style`).

## Pi rules in the UI

Only bundled/uploaded fonts are selectable. No blur/soft shadow controls exist. Inserting
a second video element on a slide is refused with an explanation (format limit).
