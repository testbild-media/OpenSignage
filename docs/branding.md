# Branding and design

## Name

**OpenSignage** – one word, capital "O" and capital "S". In the word mark "Open" is dark
(`#a63d00`) and "Signage" light orange (`#ff8a00`).

## Logo files (`branding/`)

| File | Use |
| --- | --- |
| `logo_horizontal.svg` / `.png` | symbol and word mark side by side: sidebar header, login, PIN screen |
| `Logo.svg` / `Logo.png` | main logo |
| `Icon.png` | symbol alone: collapsed sidebar, app icon |
| `Icon.ico` | favicon |

The symbol shows two staggered screens: orange at the back, dark orange-brown in front,
with stylised content (picture block, lines of text). Prefer SVG; do not distort or recolour
the logo; use it on dark backgrounds.

## Colours

Taken from the logo artwork.

| Role | Value |
| --- | --- |
| Primary (orange) | `#ff8a00` |
| Primary hover / active | `#ffa733` / `#e67a00` |
| Primary soft (areas, focus) | `rgba(255, 138, 0, 0.3)` |
| Accent | `#ff9f40` |
| Brand dark (logo "Open", accent lines) | `#a63d00` |
| Background / surface | `#201c18` / `#2a251f` |
| Surface sunken / raised | `#332d26` |
| Border / strong border | `#453e35` / `#5a5147` |
| Text / muted / faint | `#f2ede6` / `#a99e90` / `#746b60` |
| Success / warning / error | `#2f9e5c` / `#d99a1f` / `#d64545` |

All neutrals are warm (brownish), never a cold grey. Radii: 6 px small, 10 px medium,
16 px large. Shadows are subtle.

## Typography

The interface font is **Montserrat** (SIL OFL 1.1), shipped with the software and never
loaded from a CDN. It is also the default font of new text, clock and product list elements.

## Design principles

- Dark design throughout: app frame, editor, dialogs, login, operator view.
- Sidebar: horizontal logo with the version below, entries with icons, the active entry
  orange with an orange bar on the left, sub-entries indented with a line in brand dark,
  user name and "Log out" at the bottom; collapsible to icons. A red marker above the user
  area when devices have problems.
- Primary actions filled orange, secondary ones outlined or neutral, destructive ones red
  and always confirmed in a dialog that says what will happen.
- Thumbnails are shown complete (letterboxed), never cropped.
- Operator view (`/control`): large touch targets, clear state colours (available, sold out,
  not available), nothing that depends on hover.
- Player overlays (PIN, IP addresses, update and version notices): brand colours and logo,
  readable from a distance, calm, never blinking.

## PIN screen of the player

Dark background, logo, the PIN in large orange digits in outlined boxes, the IP address(es)
and the address of the player's own web interface, and a short hint: take the device over in
the administration under Devices.

## Tone

Clear, short and friendly. Error messages say what happened and what to do. Terms are used
consistently (see the [translation glossary](i18n/glossar.md)). The default language is
English; all 24 official EU languages are available.
