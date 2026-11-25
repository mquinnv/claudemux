# claudemux glasses remote

An Even Hub app for the Even G2 glasses and R1 ring. It polls the claudemux lobby for the next session that
is waiting on you, shows the question on the glasses, and sends your pick back: scroll the ring to move,
tap to choose, double-tap for the session list.

The app only talks to the lobby's token-protected `/remote/` routes. The lobby types the answer into the
real tmux pane, after checking the pane still shows the same dialog.

## 1. Enable the remote in the lobby

In claudemux's `config.yml`, turn the remote on and make sure the web server listens on the tailnet:

```yaml
web:
  listen: tailscale:7474
remote:
  enabled: true
```

Put a long random token in the claudemux env file (the one `summary.api_key_file` points at, where the
API key lives) as `CLAUDEMUX_REMOTE_TOKEN`; a process environment variable of the same name wins:

```
CLAUDEMUX_REMOTE_TOKEN=<output of: openssl rand -hex 24>
```

The `/remote/` routes exist only when `remote.enabled` is true, `web.listen` is set and the token is
non-empty. Restart the lobby (or `go install` the head, which restarts it) and check from the Mac:

```sh
curl -H "Authorization: Bearer $TOKEN" http://<tailnet-ip>:7474/remote/next
```

Without the header the answer is 401.

## 2. Pack

The lobby origin is fixed at pack time: Even Hub's network whitelist in `app.json` cannot change after the
package is built, so the pack script bakes the origin in. Use the origin the phone reaches the lobby at
(a tailnet address; plain HTTP is fine):

```sh
cd glasses
npm ci
CLAUDEMUX_REMOTE_ORIGIN=http://100.64.0.15:7474 node scripts/pack.mjs
```

This writes `app.json` from `app.json.tmpl` (one whitelisted origin, package id `net.claudemux.glasses`),
runs `vite build` and produces `claudemux-glasses.ehpk`. The script stops with a message if
`CLAUDEMUX_REMOTE_ORIGIN` is unset or is not `http(s)://host[:port]`. The token is never put in the package.
If the lobby's address changes, pack again.

## 3. Install on the phone

Either upload a private build, or load a development build.

Private build (needs an interactive terminal for the login):

```sh
npx evenhub login
npx evenhub create claudemux-glasses.ehpk --name "claudemux remote" --tagline "answer waiting sessions"
npx evenhub upload claudemux-glasses.ehpk -m "first build"
```

`create` is needed once; later builds only need `upload`. Then, in the Even app on the iPhone:

1. Open the Even Hub tab.
2. Tap the glasses icon at the top right (the plugin list).
3. Tap "Developer hub".
4. Under "In development", find the uploaded private build and install it.

Development load: on the same "Developer hub" screen, the "prototype mode" QR scanner loads a build served
by `npm run dev`. The whitelist still comes from `app.json`, so run `scripts/pack.mjs` once first.

(Even's own documentation describes a different menu path; the one above is what the Even app shows.)

## 4. Enter the token

Open the app from the phone. Its page has a token field: paste `CLAUDEMUX_REMOTE_TOKEN` and tap
"Save token". It is kept in the app's local storage on the phone (`bridge.setLocalStorage('token', ...)`).
The page also shows the last ten log lines, which is where to look when the glasses show an error.

Once the token is saved, a header reading `bad token - open setup` means the lobby rejected it: re-enter it.
`lobby not reachable` means the phone could not connect (tailnet down or the lobby is not running).

## 5. Use it

Open the app from the glasses' app menu (a private or in-development build appears there once installed).

| Gesture | Action |
| --- | --- |
| Scroll down / up | Move the cursor |
| Tap | Pick the option under the cursor; broad options ask for a second tap |
| Double-tap | Switch between the question and the session list |
| Long press | Ignored |

A session opened from the session list stays on screen: the lobby pins it until it is skipped, so the
regular polls keep returning it.

The last two rows of the options list are `defer` and `skip`. A short message replaces the title for one
poll after an answer: `sent`, `not accepted - try again`, `prompt has a draft`, `sent? check desk` (the key
went out but could not be confirmed), `typed, not sent`, `reply refused`, `gone` or `failed`. `sent` is shown
only when the lobby confirms it.

## Development

```sh
npm test            # vitest
npm run typecheck   # tsc --noEmit
npm run build       # vite build, no origin baked in
```

`src/api.ts` is the HTTP client, `src/controller.ts` the poll/POST sequencing, `src/view.ts` and
`src/layout.ts` the screen, and `src/main.ts` the only file that touches the Even Hub SDK. The text width is
`COLS` in `src/layout.ts`.
