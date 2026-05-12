# `GET /version`

```json
{
  "version": "0.2.0",
  "git_sha": "abc1234",
  "build_time": "2026-05-12T10:00:00Z"
}
```

Set at link time via `-ldflags "-X .../internal/buildinfo.Version=…"`. The
release pipeline injects all three; local `make build` returns `dev` /
`unknown` / `unknown`.

No auth required.
