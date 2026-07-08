Paste an image from the Mac's clipboard into this session.

The `ccimgd-unix` daemon runs on the Mac (Mac Studio) and only *reads* whatever
image is currently on that Mac's clipboard (e.g. from a screenshot taken there)
— it does not accept images pushed to it. Take/copy the screenshot on the Mac
first, then run this.

Instructions:
1. Run `ccimg-unix` (no arguments) using the Bash tool. It connects to the
   ccimgd-unix daemon over a Unix-domain socket, receives the clipboard image
   as base64-encoded PNG, writes it to a temporary file, and prints that file's
   path on stdout.
2. Use the Read tool to read the printed file path. This will display the
   image to Claude.

```bash
ccimg-unix
```

## Socket discovery

`ccimg-unix` finds the daemon's socket in this order:
1. `$CCIMG_SOCK` env var, if set — used verbatim.
2. Otherwise it globs `/tmp/ccimg-*.sock` and picks the most recently modified
   match.

The socket this session normally talks to is exposed via an SSH
`RemoteForward` from the Mac and shows up here as
`/tmp/ccimg-studio-mathias.sock`. If `ccimg-unix` reports no socket found or
connection refused, that forward is not currently up (check the SSH session
carrying it).

## Raw protocol (for debugging without the client binary)

The daemon speaks a one-line JSON request/response over the Unix socket:

```bash
printf '{}\n' | nc -U /tmp/ccimg-studio-mathias.sock
```

returns `{"ok":true,"image":"<base64-png>"}` (or `{"ok":false,"error":"..."}`
if the clipboard has no image).
