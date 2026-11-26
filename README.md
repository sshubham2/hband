# hband

hband is a small web server for publishing HTML reports and hearing back from the people who read them.
The owner signs in with GitHub and uploads self-contained `.html` files from an admin area.
Anyone can read a published report. A reader who signs in with GitHub can give a thumbs up or down,
comment and reply (in plain text, to any depth) and send the owner private feedback.
The owner can reply to feedback, remove any comment, and block a GitHub account.
A public "All reports" page lists every published report with its likes, visible comments and times opened.
Every page (reader pages, admin pages, Blocked users and the "Not allowed" page) is drawn from one shared layout and
one stylesheet, which is light or dark according to the visitor's own browser setting.

It is one Go program (`cmd/hband`) with an embedded SQLite database; there is no other service to run.

## What you need

| To | You need |
|---|---|
| Build and test | Go 1.27.4 (the `go` line in `go.mod`). No C compiler: the SQLite driver (`modernc.org/sqlite`) is pure Go. Dependencies are downloaded by `go` the first time. The tests run `go build` themselves, so `go` must be on the `PATH`. |
| Run | The built program, a writable folder for its data, and a GitHub OAuth app (see [Sign-in with GitHub](#sign-in-with-github)). |
| Run the browser-based tests | Optional: Google Chrome, named by the `CHROME_BIN` variable (see [Tests](#tests)). Without it those tests are skipped. |

## Build

Linux or macOS:

```sh
go build -o hband ./cmd/hband
```

Windows:

```powershell
go build -o hband.exe ./cmd/hband
```

Either writes the program in the current folder. On Windows, `-o hband` would write a file named exactly `hband`
with no `.exe`, so give the name with its extension (or leave `-o` out: `go build ./cmd/hband` writes `hband.exe`
on Windows and `hband` elsewhere). Both names are in `.gitignore`.
`go build ./...` compiles every package without writing a program.

## Run

The program takes **no command-line flags and no configuration file**. Everything is read from environment
variables whose names start with `HBAND_` (see [Settings](#settings)). Four are required; without them it prints
every problem it found and exits with status 2.

Linux or macOS shell:

```sh
export HBAND_OWNER_GITHUB_ID=12345678
export HBAND_GITHUB_CLIENT_ID=your-oauth-client-id
export HBAND_GITHUB_CLIENT_SECRET=your-oauth-client-secret
export HBAND_BASE_URL=http://127.0.0.1:8080
./hband
```

PowerShell:

```powershell
$env:HBAND_OWNER_GITHUB_ID = "12345678"
$env:HBAND_GITHUB_CLIENT_ID = "your-oauth-client-id"
$env:HBAND_GITHUB_CLIENT_SECRET = "your-oauth-client-secret"
$env:HBAND_BASE_URL = "http://127.0.0.1:8080"
.\hband.exe
```

It listens on `127.0.0.1:8080` unless `HBAND_ADDR` says otherwise, logs one `listening` line to standard error and
runs until it gets Ctrl-C or SIGTERM, then stops accepting requests and gives running ones up to 10 seconds to finish.

Exit status: `0` after a clean shutdown, `2` for invalid configuration, `1` for any other failure to start
(data folder, database, report library or listening address) or if the server stops with an error.

`GET /healthz` answers `200` with the text `ok` when the database answers within 2 seconds, otherwise `503`.

## Tests

```
go test -count=1 ./...
```

This runs every package's tests (about 50 seconds here with `CHROME_BIN` set, all passing). Most of them build the
real program with `go build` into a folder under the operating system's temporary folder, start it on a loopback port
with a throwaway data folder and a stand-in for GitHub (`internal/testutil/fakegithub`), and talk to it over HTTP. They
need no network, no GitHub account and none of the `HBAND_*` settings below, and they leave nothing in the repository.

Some tests drive a real browser: in `internal/server`, `TestReportScriptsNeverRun`, `TestEveryScreenIsDesigned` (it
measures every screen in light and dark, at several widths) and the three look and avatar probe tests beside it
(`TestLookProbeSeesTheStylesheetBeforeTheDesign`, `TestAvatarProbeSeesABrokenImageGlyph`,
`TestALoadedAvatarIsLeftAlone`); and `TestE004FoldAndFitInBrowser` in
`tests/acceptance/e004_2_deep_threads_and_toggle`. The browser is the one named by `CHROME_BIN`, and only that:
set it to the path of a Chrome executable, for example

```powershell
$env:CHROME_BIN = "C:\Program Files\Google\Chrome\Application\chrome.exe"
go test -count=1 ./...
```

If `CHROME_BIN` is unset, or names no existing file, those tests are skipped with a message that names `CHROME_BIN`,
and the rest of the suite still passes. Microsoft Edge is refused (those tests are skipped) whatever `CHROME_BIN`
says, and nothing falls back to another browser or to a built-in install path. Set `HBAND_TEST_ARTIFACTS` to a
folder to keep a copy of every screenshot the browser checks take. Neither variable is read by the program itself.

A second, opt-in check lives behind the build tag `sc042suite` in `tests/acceptance/suite`: it runs the whole suite
again, once without `CHROME_BIN` and once with it, and judges which tests skipped. It is not part of
`go test ./...`; the two commands that run it are in the comment at the top of `suite_test.go`.

## Looking at it with sample data

To open a populated site in a browser without registering a GitHub OAuth app, run this from the repository root:

```
go run ./internal/testutil/seeded -port 8081
```

`-port` is required: a decimal number from 1 to 65535. The helper builds the real program, seeds a throwaway data
folder and serves it on `http://127.0.0.1:<port>/`. The seed is one published report (`e004-look`) carrying a
nine-level comment thread; a draft report (`e004-draft`); a private message from a reader (`octocat`) with the author's
reply; and an account (`noisy-reader`) that commented, had a comment removed, sent a private message and was then
blocked. It prints `serving http://127.0.0.1:<port>/` once the site answers, then runs until Ctrl-C, which stops the
program and removes its folders.

Sign-in on that site goes to a stand-in for GitHub, not to GitHub. The stand-in shows a page with two links instead of
approving at once: "Continue as the owner" (the account the program pins as owner, so `/admin` opens) and "Continue as
a reader" (`octocat`, who gets the "Not allowed" page at `/admin`). A visitor stays anonymous until a link is followed.
It is a test helper under `internal/testutil`, not part of the product; if it is killed instead of interrupted, its
folders (named `hband-*`, under the temporary folder) are left behind.

## Settings

Every setting is an environment variable. Names are exact and case-sensitive. A set value is never trimmed
except where stated. All problems are reported together, and no message repeats a value (the client secret is never
printed or logged). An empty value counts as unset. Any other `HBAND_*` name is ignored.

| Variable | Required | Default | What it does |
|---|---|---|---|
| `HBAND_OWNER_GITHUB_ID` | **yes** | – | The numeric GitHub user ID of the owner (not the login name). Whoever signs in with this account is the owner and can reach `/admin`; nobody else can. Must be a positive decimal integer of up to 19 digits that fits in a signed 64-bit integer. Ownership is decided from this value on every request and is never stored in the database. |
| `HBAND_GITHUB_CLIENT_ID` | **yes** | – | Client ID of the GitHub OAuth app used for sign-in. |
| `HBAND_GITHUB_CLIENT_SECRET` | **yes** | – | Client secret of that OAuth app. |
| `HBAND_BASE_URL` | **yes** | – | The public origin of the site, e.g. `https://hband.example`. Must be an absolute `http` or `https` URL with a host and no path (other than `/`), query, fragment or user information; `http` is accepted only for `127.0.0.1`, `localhost` or `[::1]`. A trailing slash is dropped. The OAuth callback address, the trusted origin for form posts, the report frame's `frame-ancestors`, and whether cookies are `Secure` all come from this value and never from request headers. |
| `HBAND_ADDR` | no | `127.0.0.1:8080` | The address the program listens on, as `host:port` (the port must be present). |
| `HBAND_DATA_DIR` | no | `./data` | Folder for the database and report files (see [What is kept on disk](#what-is-kept-on-disk)). Created if missing. A relative path is resolved against the working directory the program is started in. |
| `HBAND_MAX_UPLOAD_BYTES` | no | `26214400` (25 MiB) | Largest report file the admin upload accepts, in bytes. A positive decimal integer, at most `209715200` (200 MiB). |
| `HBAND_TRUSTED_PROXIES` | no | empty: no proxy is trusted | Comma-separated IP addresses and/or CIDR prefixes (IPv4 or IPv6, e.g. `127.0.0.1,10.0.0.0/8`) of reverse proxies whose `X-Forwarded-For` header may be believed. Used only to tell visitors apart for counting opens. Names, empty items, IPv6 zones and a `/0` prefix are refused, and one bad item refuses the whole list. See [Behind a reverse proxy](#behind-a-reverse-proxy). |
| `HBAND_GITHUB_URL` | no | `https://github.com` | Base URL of GitHub's sign-in pages (`/login/oauth/authorize` and `/login/oauth/access_token` are added). Intended for pointing sign-in at a stand-in during testing. Must be `https`, or `http` on a loopback host, with no path, query or fragment. When it differs from the default the program logs a warning at start. |
| `HBAND_GITHUB_API_URL` | no | `https://api.github.com` | Base URL of GitHub's API (`/user` is added). Same rules and same purpose as `HBAND_GITHUB_URL`. |

Test-only variables, read by the tests and not by the program: `CHROME_BIN`, `HBAND_TEST_ARTIFACTS`
(see [Tests](#tests)).

## Sign-in with GitHub

Sign-in is the only way to identify anyone. You need a GitHub OAuth app (GitHub account settings, Developer
settings, OAuth Apps) with:

- **Authorization callback URL:** `HBAND_BASE_URL` followed by `/auth/github/callback`, exactly, for example
  `https://hband.example/auth/github/callback`.
- Its client ID and secret in `HBAND_GITHUB_CLIENT_ID` and `HBAND_GITHUB_CLIENT_SECRET`.

The program requests no OAuth scopes: it reads only the signed-in account's public profile (numeric ID, login and
avatar URL). The numeric ID of an account is what `HBAND_OWNER_GITHUB_ID` must hold; the login name is not accepted.

Sessions last 30 days. The session cookie is `hband_sid` (`__Host-hband_sid` when `HBAND_BASE_URL` is `https`),
`HttpOnly`, `SameSite=Lax`, and `Secure` when the base URL is `https`. Only a SHA-256 hash of the session token is stored.
A short-lived (10 minute) cookie `hband_oauth` carries the sign-in state while a visitor is at GitHub.

## What the site serves

Public pages (no sign-in):

| Path | What |
|---|---|
| `GET /` | The newest published report, laid out like its own page ("No reports yet." when there is none). |
| `GET /reports` | The "All reports" page: every published report, newest first, with likes, visible comments and times opened. |
| `GET /reports/<slug>` | A published report, with its likes, comments and feedback box. Under the title is a byline with the owner's GitHub login and avatar (as of the owner's last sign-in; left out if the owner has never signed in) and the first publish date. The report itself is shown in an iframe loaded from `/raw/<slug>`. |
| `GET /reports/<slug>/comments/<id>` | One branch of a deep comment thread on its own page (the "continue this thread" link; a page draws five levels). |
| `GET /raw/<slug>` | The report's bytes, served only while published, with a policy that blocks all scripts. |
| `GET /healthz` | Health check (see above). |
| `GET /static/...` | The site's stylesheet (`site.css`) and its one script (`thread.js`, loaded only on pages that draw a report's thread). |

Actions that need a signed-in reader (a visitor who is not signed in is sent back to the report and nothing is stored):
`POST /reports/<slug>/vote` (field `vote`: `up`, `down` or `none`), `POST /reports/<slug>/comments` (fields `body`, and
`parent` for a reply), `POST /reports/<slug>/feedback` (field `body`). Sign-in and sign-out are
`GET /auth/github/login` (optional `?next=` local path to return to), `GET /auth/github/callback` and `POST /auth/logout`.
A comment or feedback body is plain text of 1 to 5000 characters (after trimming white space at both ends); control
characters other than newline and tab are refused. Anything else typed is shown as typed, never as markup.

Owner pages (everything under `/admin`): an anonymous `GET` or `HEAD` is redirected to sign-in (any other method from an
anonymous visitor gets a 403), a signed-in non-owner gets a 403 "Not allowed" page, and only the owner gets through.
The admin pages share a small navigation bar: Reports, Blocked users and View site.

| Path | What |
|---|---|
| `GET /admin` | The report list and the upload form. |
| `POST /admin/reports` | Upload (multipart: `title`, `slug`, `status` = `published` or `draft`, and one `file`). A new slug creates a report; an existing slug replaces its file, title and publish state and keeps its ID, first publish date, votes and comments. |
| `GET /admin/reports/<slug>` | One report: likes and dislikes, preview, publish state, replace form, every visible comment (with remove and block), private feedback with reply forms. |
| `GET /admin/reports/<slug>/raw` | Preview of the file, drafts included. |
| `POST /admin/reports/<slug>/publish`, `/unpublish`, `/delete` | Change the publish state, or delete the report with its file, votes, comments and opens. |
| `POST /admin/feedback/<n>/reply` | Reply privately to a feedback message. |
| `POST /admin/comments/<n>/remove` | Remove a comment at any depth. |
| `POST /admin/users/<github-id>/block`, `/unblock` and `GET /admin/blocked` | Block or unblock a GitHub account and review what a blocked account posted. |

Rules the upload enforces: the file name must end in `.html` or `.htm`; the file must be non-empty and, after an
optional UTF-8 byte-order mark, look like UTF-8 HTML or text in its first 512 bytes; it may not exceed
`HBAND_MAX_UPLOAD_BYTES`; the slug is 1 to 80 characters, lower-case letters and digits with single hyphens between them;
the title is 1 to 200 characters with no control characters. A refused upload changes nothing.

On a Windows host, a report file that a reader is fetching at that moment cannot be swapped: the replace is refused
with a 409 and nothing changes, and trying again a moment later works. (The code's own note says the intended host is a
Linux container, where this does not arise.)

How opens are counted: a report page that is answered with a 200 counts one open per visitor, per report, per UTC day.
Not counted: the owner's own visits, requests that are not `GET`, a `User-Agent` that is empty, does not contain
`Mozilla/` or contains a crawler/preview/command-line token (e.g. `bot`, `crawl`, `spider`, `preview`, `headless`, `curl`,
`wget`, `python`, `go-http`), the branch page, the All reports page, `/raw`, and admin pages.

## What is kept on disk

Everything lives under `HBAND_DATA_DIR` (default `./data`, relative to where the program is started):

| Path | Contents |
|---|---|
| `hband.db` (plus `hband.db-wal` and `hband.db-shm` while it runs) | The SQLite database, in WAL mode. Holds signed-in users (GitHub ID, login, avatar URL), sessions (hashed tokens), report records (slug, title, status, dates, size, SHA-256), votes, comments, blocked accounts, private feedback and replies, and per-day opens. The schema is created and upgraded automatically at start. |
| `reports/<id>.html` | The uploaded report files, named by the report's database ID. |
| `tmp/` | Scratch space for uploads and replacements in progress. Emptied at every start. |

**Back up** the whole data folder: `hband.db` and `reports/` belong together (the database says which reports exist,
the files are their content). Take the copy with the program stopped, or copy `hband.db` together with its `-wal` and
`-shm` files. `tmp/` need not be kept. Nothing else is stored: the configuration lives in the environment, so keep
the OAuth client secret wherever you keep your other secrets.

Things worth knowing about the data folder:

- At start the program removes any `reports/<n>.html` that has no database row, and restores or discards a file a
  replace had set aside. If it cannot tell which is right it refuses to start and names the file; it tries again at the next start.
- A published report whose file is missing is not hidden: `/raw/<slug>` answers 500 and logs it.
- A database whose schema version is newer than the program understands (or negative) is refused at start.
- Opens are stored as a 16-byte keyed hash of report, UTC day and client address (the whole IPv4 address, or the /64
  of an IPv6 one). The key is random, drawn at each start and kept only in memory, so a stored value cannot be turned back
  into an address; the cost is that a restart part-way through a UTC day lets a returning visitor count once more.

## Running it for real

What the code assumes:

- **Plain HTTP only.** The program does not terminate TLS. For a public `https://` site, put a reverse proxy that
  does in front and set `HBAND_BASE_URL` to the public `https://` origin; cookies then become `Secure` and
  `__Host-` prefixed.
- **`HBAND_BASE_URL` must be the exact origin visitors use.** The program takes scheme and host only from it, never from
  `Host`, `X-Forwarded-Proto` or similar headers.
- **A GitHub OAuth app** whose callback URL is `HBAND_BASE_URL` + `/auth/github/callback`, and the owner's numeric GitHub ID.
- **A data folder that survives restarts** and is writable by the program.
- **Default listening address is loopback** (`127.0.0.1:8080`); set `HBAND_ADDR` to listen elsewhere.
- **Readers' browsers must be able to reach `avatars.githubusercontent.com`.** The site's content policy allows images
  only from the site itself and `https://avatars.githubusercontent.com`, where GitHub serves the avatars shown beside
  comments and in the byline; the program does not fetch or proxy them.

### Behind a reverse proxy

- Pass through `Origin` and `Sec-Fetch-Site` on form posts. The program refuses (403) browser POSTs whose origin is
  neither the same origin nor the origin of `HBAND_BASE_URL`.
- Forward the report frame's responses unchanged: `/raw/<slug>` carries its own `Content-Security-Policy`
  (`sandbox`, no scripts, `frame-ancestors` set to the base origin), and every other page carries the site policy,
  `X-Content-Type-Options: nosniff` and `Referrer-Policy`.
- To count opens per visitor, the proxy must send the visitor's address in `X-Forwarded-For` (each proxy appending the
  address it received the request from), and every proxy address in the chain must be listed in `HBAND_TRUSTED_PROXIES`.
  The header is read from the right, stepping over trusted proxies; the first address not listed is the visitor.
  `X-Real-IP` and `Forwarded` are never read. If the variable is missing or wrong, every visitor behind the proxy
  looks like the proxy and counts as one visitor per report per day; the program logs a warning once when it sees
  `X-Forwarded-For` from a peer that is not listed. If the forwarded address is private/loopback/shared (CGNAT) space,
  or a trusted peer sent none, those requests are not counted and a warning is logged once.
- Large uploads: the admin upload streams a file up to `HBAND_MAX_UPLOAD_BYTES` (plus about 64 KiB of form overhead);
  any body-size limit on the proxy must be at least that.

## What it deliberately does not do

- It does not offer any sign-in other than GitHub, has no passwords, and asks GitHub for no scopes.
- It does not run a report's scripts. A report is shown in a sandboxed iframe that grants only `allow-popups` and `allow-popups-to-escape-sandbox` (never `allow-scripts`), and with a policy
  that forbids scripts, external resources and forms; it may use inline styles and `data:` images and fonts only. The upload
  accepts one UTF-8 `.html` file, not a zip or a folder of assets.
- Dislikes are never shown to readers: they appear only on the owner's admin page. Likes are public.
- Comments and feedback are plain text. There is no markup, editing or deleting by their writer.
- Removing a comment cannot be undone, and the program has no way to un-remove one. A removed comment (or one by a
  blocked account) that has visible replies stays as a "comment removed" placeholder; one with none is dropped from view.
- A block hides the account's comments everywhere, and stops it commenting, sending feedback or voting. Its existing
  vote stays as it was, and unblocking restores its comments (not ones removed in the meantime). The owner's own
  account cannot be blocked.
- The owner is the one pinned account; there are no other admins, roles or accounts to manage.
- It does not store visitors' addresses, and it does not count the owner's visits or bots as opens.
- It does not serve HTTPS, send email, or call any service except GitHub's sign-in and profile endpoints.
- The admin lists (comments, feedback, blocked users) are not paged or limited; they grow with what was posted.

## Licence

MIT, see `LICENSE`.
