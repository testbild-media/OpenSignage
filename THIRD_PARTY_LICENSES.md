# Third-party licenses

Everything shipped in OpenSignage packages that is not original work of the project.
Keep this file in sync when dependencies change (checked during release review).

Every component listed here is under a free license (MIT, ISC, BSD, Apache 2.0, SIL OFL 1.1
or public domain) that permits distributing it together with OpenSignage, which is licensed
under the AGPL-3.0. The components keep their own licenses. The copyright notices and license
texts that the licenses require to be passed on are reproduced in
[Copyright notices and license texts](#copyright-notices-and-license-texts).

## Fonts

All bundled fonts are listed with version, license, upstream source and copyright in
[fonts/LICENSES.md](fonts/LICENSES.md) (in release packages: `share/fonts/LICENSES.md`); each
family folder contains its license file (`OFL.txt` or `LICENSE.txt`). Families with a Reserved
Font Name are shipped unmodified.

Roboto Condensed is licensed under the SIL OFL 1.1 by its upstream source (`OFL.txt` and
`METADATA.pb` of google/fonts `ofl/robotocondensed`) and is shipped under that license. The
name table of the upstream font files still carries the earlier notice "Licensed under the
Apache License, Version 2.0", which is also a free license.

## Branding

`branding/` (OpenSignage logo and icon) is project artwork and part of OpenSignage.
`web/apps/admin/public/favicon.ico` is a copy of `branding/Icon.ico`. All other icons in the
web interfaces come from lucide-react (see below).

## Go modules (compiled into the binaries)

Checked with `go version -m` on the release binaries (linux/amd64, linux/arm64, linux/armv7).
The server contains all modules below; the agent contains only github.com/coder/websocket,
golang.org/x/crypto, golang.org/x/sys (amd64 only) and the Go standard library.

| Module | License |
| --- | --- |
| modernc.org/sqlite (contains SQLite translated to Go; SQLite is in the public domain) | BSD-3-Clause |
| modernc.org/libc (contains code translated from musl libc, MIT, and code from the Go project, BSD-3-Clause) | BSD-3-Clause / MIT |
| modernc.org/mathutil, modernc.org/memory (memory contains code from mmap-go and from the Go project, both BSD-3-Clause) | BSD-3-Clause |
| github.com/coder/websocket | ISC |
| golang.org/x/crypto, golang.org/x/sys, golang.org/x/term | BSD-3-Clause |
| github.com/google/uuid, github.com/remyoudompheng/bigfft (transitive dependencies of modernc.org/sqlite) | BSD-3-Clause |
| github.com/dustin/go-humanize (transitive dependency of modernc.org/sqlite) | MIT |
| github.com/mattn/go-isatty, github.com/ncruces/go-strftime (indirect requirements in go.mod, used by modernc.org/libc on other platforms only; not compiled into the Linux binaries) | MIT |
| Go standard library and runtime incl. `time/tzdata` (the IANA time zone database, public domain) | BSD-3-Clause |

## JavaScript packages (bundled into the web interfaces)

Checked against the source maps of a production build: only the admin WebGUI bundle contains
npm packages; the player is built from project code only.

| Package | License |
| --- | --- |
| react, react-dom, scheduler | MIT |
| react-router | MIT |
| lucide-react (icons) | ISC / MIT (icons derived from Feather) |
| prosemirror-model, prosemirror-state, prosemirror-view, prosemirror-commands, prosemirror-history, prosemirror-keymap, prosemirror-transform | MIT |
| orderedmap, rope-sequence, w3c-keyname (dependencies of the ProseMirror packages) | MIT |

The react-router dependencies cookie and set-cookie-parser are removed by tree shaking and are
not shipped. Build-only tools (Vite, TypeScript, ESLint, Prettier, Vitest, jsdom) are not
shipped.

## Code snippets

| Snippet | Where | License |
| --- | --- | --- |
| "Hash without Sine" (the `hash` function of the noise shader), Copyright (c) 2014 David Hoskins | `web/apps/player/src/noise.ts` (plasma regeneration) | MIT |

The MIT License text is reproduced below.

## System packages installed by the installers (not redistributed)

The installers install these packages from the operating system's package sources (Debian,
Ubuntu, Raspberry Pi OS). They are not part of the OpenSignage packages and remain under their
own licenses:

* Player (client installer): Chromium, the X server (xserver-xorg-core,
  xserver-xorg-video-fbdev, xserver-xorg-legacy, x11-xserver-utils, xcvt), xinit,
  fonts-noto-color-emoji, fonts-dejavu-core, sudo and their dependencies; on Intel and AMD
  graphics also the free VA-API and Vulkan drivers (intel-media-va-driver, i965-va-driver,
  mesa-va-drivers, mesa-vulkan-drivers).
* Server: ffmpeg and its dependencies.
* One-line installer: openssl, if it is missing (to check the release signature).

## Copyright notices and license texts

### MIT License

* react, react-dom, scheduler: Copyright (c) Meta Platforms, Inc. and affiliates.
* react-router: Copyright (c) React Training LLC 2015-2019, Copyright (c) Remix Software Inc.
  2020-2021, Copyright (c) Shopify Inc. 2022-2023
* prosemirror-model, prosemirror-state, prosemirror-view, prosemirror-commands,
  prosemirror-history, prosemirror-keymap, prosemirror-transform: Copyright (C) 2015-2017 by
  Marijn Haverbeke <marijn@haverbeke.berlin> and others
* orderedmap, w3c-keyname: Copyright (C) 2016 by Marijn Haverbeke <marijn@haverbeke.berlin>
  and others
* rope-sequence: Copyright (C) 2016 by Marijn Haverbeke <marijn@haverbeke.berlin>
* Icons in lucide-react derived from the Feather project: Copyright (c) 2013-present Cole Bemis
* github.com/dustin/go-humanize: Copyright (c) 2005-2008 Dustin Sallings <dustin@spy.net>
* musl libc (translated to Go in modernc.org/libc): Copyright © 2005-2020 Rich Felker, et al.
  Much of the musl math library is Copyright © 1993,2004 Sun Microsystems or Copyright ©
  2003-2011 David Schultz or Copyright © 2003-2009 Steven G. Kargl or Copyright © 2003-2009
  Bruce D. Evans or Copyright © 2008 Stephen L. Moshier or Copyright © 2017-2018 Arm Limited,
  all under permissive terms (as listed in musl's COPYRIGHT file, which modernc.org/libc
  reproduces in full in its `LICENSE-3RD-PARTY.md`).
* "Hash without Sine": Copyright (c) 2014 David Hoskins

Permission is hereby granted, free of charge, to any person obtaining a copy of this software
and associated documentation files (the "Software"), to deal in the Software without
restriction, including without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the
Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or
substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING
BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM,
DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

### ISC License

* lucide-react: Copyright (c) 2026 Lucide Icons and Contributors
* github.com/coder/websocket: Copyright (c) 2025 Coder

Permission to use, copy, modify, and/or distribute this software for any purpose with or
without fee is hereby granted, provided that the above copyright notice and this permission
notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH REGARD TO THIS
SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE
AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT,
NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR
PERFORMANCE OF THIS SOFTWARE.

### BSD 3-Clause License

The third condition names the party given in brackets.

* Go standard library and runtime, golang.org/x/crypto, golang.org/x/sys, golang.org/x/term:
  Copyright 2009 The Go Authors. [Google LLC]
* github.com/google/uuid: Copyright (c) 2009,2014 Google Inc. All rights reserved. [Google Inc.]
* github.com/remyoudompheng/bigfft: Copyright (c) 2012 The Go Authors. All rights reserved.
  [Google Inc.]
* modernc.org/libc: Copyright (c) 2017 The Libc Authors. All rights reserved. [the authors];
  code from Go: Copyright (c) 2009 The Go Authors. All rights reserved. [Google Inc.]
* modernc.org/mathutil: Copyright (c) 2014 The mathutil Authors. All rights reserved. [the
  authors]
* modernc.org/memory: Copyright (c) 2017 The Memory Authors. All rights reserved. [the
  authors]; code from mmap-go: Copyright (c) 2011, Evan Shaw <edsrzf@gmail.com>. All rights
  reserved. [the copyright holder]; code from Go: Copyright (c) 2009 The Go Authors. All rights
  reserved. [Google Inc.]
* modernc.org/sqlite: Copyright (c) 2017 The Sqlite Authors. All rights reserved. [the
  copyright holder]

Redistribution and use in source and binary forms, with or without modification, are
permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this list of
   conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice, this list of
   conditions and the following disclaimer in the documentation and/or other materials
   provided with the distribution.
3. Neither the name of [the party named above] nor the names of its contributors may be used
   to endorse or promote products derived from this software without specific prior written
   permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND ANY EXPRESS OR
IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY
AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR
CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR
OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.

### Public domain

* SQLite (translated to Go in modernc.org/sqlite): dedicated to the public domain by its
  authors (https://sqlite.org/copyright.html).
* The IANA time zone database embedded by `time/tzdata`.

### Fonts

The SIL Open Font License 1.1 and Apache License 2.0 texts are shipped next to the font files;
see [fonts/LICENSES.md](fonts/LICENSES.md).
