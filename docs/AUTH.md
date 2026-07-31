# Access control: capabilities, codes, and the two-transport model

octo-doc documents are **private by default**. Access is granted by *capabilities*
— credentials that map to a level of access for a specific document. There is no
global public/private switch; privacy is per-document.

## Capability levels

`resolveCapability(request, slug) → None | Read | Comment | Edit | Manage`

Capabilities are **totally ordered** — `None < Read < Comment < Edit < Manage` —
so a route enforces a minimum with a plain `AtLeast` check. The two base
credentials below resolve to the bounds of that order; the four-role member
table (see *HTML four-role members* below) fills in the middle tiers.

| Level | Grants |
| --- | --- |
| **Manage** | read everything incl. drafts; publish, promote, delete; manage members/share; the doc creator, an octo `superAdmin`, and a `doc_member` **admin** resolve here |
| **Edit** | run AI edits, save drafts, publish, resolve/reopen threads (a `doc_member` **writer**) |
| **Comment** | create/reply/react and edit/delete OWN comments (a `doc_member` **commenter**) |
| **Read** | view published versions, comments, history, source and diff (a per-doc **share code**, or a `doc_member` **reader**) |
| **None** | nothing — the server returns **404** (never confirms the doc exists) |

### Share-code capability

A per-doc **share code** resolves to **Read only** (`CapRead`). It lets a holder
view published versions, comments, history, source and diff. It does **not** by
itself grant commenting — commenting requires **Comment** (a `doc_member`
commenter or higher). A share code never unlocks drafts, publishing, promotion,
deletion, or member management, so handing out a share link is safe: it cannot
be escalated into write or manage access. (This tightens the pre-redesign
read+comment share code to read-only under the four-role model; comment access
is now an explicit member grant.)

### HTML four-role members

Direct grants live in the docs-backend `doc_member` table (same MySQL database —
no separate store, no recoding, no migration marker). The `role` column is a
plain integer whose values are a **wire contract shared with docs-backend**:

| Label | `doc_member.role` | Capability |
| --- | --- | --- |
| `reader` | **1** | Read |
| `writer` | **2** | Edit |
| `admin` | **3** | Manage |
| `commenter` | **4** | Comment |

The integer encoding is **not** capability-ordered (admin is 3, not the largest
value). The capability order is derived **only** through the explicit
`CapabilityForDocRole` / `roleCodeToLabel` mappings — the code never compares the
stored `role` integer with `<`/`>`. Admin rows are guarded by **equality**
(`WHERE role<>3`), which is safe regardless of the numeric value. Because octo-doc
and docs-backend share the same table in the same database, a row written by
either side reads back with the same meaning on the other; **no startup gate,
version marker, or backfill is required** to interoperate.

The HTML grants API (`/v1/docs/{slug}/grants`) speaks the string labels
`reader | commenter | writer`. `admin` is **never** mintable through it — admin
identity is owned by the doc creator (`creator_uid`) and the owner-backfill path,
and both `AddGrant` and `RemoveGrant` refuse to touch a creator or admin row.

### Legacy metadata boundary (`meta.grants`)

Before the four-role redesign, direct grants lived in a `meta.grants` map inside
the doc's metadata (role stored as a **string label**, so the integer encoding
above never touches legacy data). That path is now a **bounded fallback**:

- **Wired + registered doc:** `doc_member` is the sole authority. A registered
  doc with no member row for a uid must **not** fall back to `meta.grants` — the
  fail-closed boundary that prevents a stale legacy entry from reviving revoked
  or downgraded access.
- **Unwired / unregistered doc** (single-node deploys, in-memory tests, or the
  brief post-publish registration gap): `meta.grants` is read and written so the
  four grant operations stay aligned; `admin` and unknown labels there fail
  closed to `None`.
- Every write that lands authoritatively in `doc_member` — both `AddGrant`
  (including a downgrade) and `RemoveGrant` — **sweeps** the matching
  `meta.grants[uid]` under the same slug lock, so a later unmount/soft-delete
  that flips a doc back to the unregistered fallback cannot revive the stale
  role.

## Per-doc share codes

Every document can have one share code (128-bit, stored **hashed** — a leaked
metadata dump can't reveal it). Mint or rotate it:

```bash
# mint/rotate a read code (Read capability) → { code, url: ".../d/<slug>/v/N?code=<code>" }
curl -sX POST -H "Authorization: Bearer $TOKEN" \
  https://docs.example.com/v1/docs/<slug>/share

# revoke the code — existing links stop working
curl -sX DELETE -H "Authorization: Bearer $TOKEN" \
  https://docs.example.com/v1/docs/<slug>/share
```

or click **Share** in the doc's toolbar. Rotating mints a new code and
invalidates the old one, so a leaked link can be cut off.

## One credential model, two transports

The same capability model is presented two ways, so humans and agents both work
with no special-casing on the server:

- **Browsers** carry the code as `?code=<code>` on the first visit. The server
  validates it, sets an **HttpOnly, SameSite=Lax** cookie scoped to that doc, and
  **302-redirects to the same URL without the query string** — so the secret never
  lingers in browser history, server/proxy logs, or the `Referer` header. Later
  reads and comments ride the cookie automatically.
- **API clients** send the credential as `Authorization: Bearer <cred>` —
  the write token for author operations, or a share code for reader operations
  (e.g. reading comments on a private doc via `GET /v1/comments`). API clients
  never touch cookies.

This is the same split GitHub uses (web session cookie vs. API/CLI token): the
authorization layer is one credential model; only the *transport* differs.

## Drafts

Authoring iterates on a **mutable draft slot** that lives outside the immutable
version numbering. The draft is **author-only** — a share code does not grant
access to it. An author saves the draft with `PUT /v1/docs/<slug>/draft`; a
browser opens it with `?code=<write-token>` → cookie exchange (the write token is
the author credential; it appears in the URL only for the one redirect that
strips it). Promoting the draft (`POST /v1/docs/<slug>/draft/promote`, or the
Publish button) creates an immutable version; that version is then readable by
anyone with the doc's share code.

## Operational notes

- Store codes are compared in constant time; only their SHA-256 hash is persisted.
- Serve over HTTPS in production (`COOKIE_SECURE=true`, the default) so codes and
  cookies are never sent in cleartext. The local docker stack runs plain HTTP for
  convenience.
- There is **no** `PRIVATE` env var anymore — it was a global switch, superseded
  by per-doc capabilities.
