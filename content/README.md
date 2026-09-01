<!-- SPDX-License-Identifier: GPL-3.0-or-later -->

# Content directory

Static remote config served to the client, rooted at `OPG3D_CONTENT_DIR`
(default `content`).

A request for `https://secure.pixelgun3dserver.com/pixelgun3d-config/<rest>` is
rewritten by the client shim and served from `content/pixelgun3d-config/<rest>`.

Rules:

- A file that exists is served verbatim, with `Content-Type: application/json`
  for `.json`.
- A file that does not exist is answered with an empty document (`[]` for the
  rating maps, `{}` otherwise) and logged as `content.fallback_served`. The
  client keeps loading instead of waiting out an HTTP timeout on a 404.
- Paths that try to escape this directory are rejected with 400.
- `rating-minigame/rating-minigame-count-all.json` is an authored local
  zero-vote baseline. The 23.1.3 client expects an object keyed by numeric game
  mode id; each value has numeric `rating` and integer `count` fields. Both the
  direct ids 0-42 and the alternate ids 1000-1042 are present.

Grep the server log for `content.fallback_served` to see which files the game
asked for and still need to be authored.
