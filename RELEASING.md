# Releasing

**Do not use `make release`.** Trigger a release from CI:

```bash
# Bump patch (default)
gh workflow run tag-release.yml --field bump=patch

# Bump minor/major
gh workflow run tag-release.yml --field bump=minor
gh workflow run tag-release.yml --field bump=major

# Explicit version (skips auto-detection)
gh workflow run tag-release.yml --field version=v1.2.3
```

## Flow

```
tag-release.yml  ──resolves tag──►  git tag vX.Y.Z  ──pushes──►  gh workflow run release.yml --field tag=vX.Y.Z
                                                                       │
                                  ┌────────────────────────────────────┘
                                  ▼
                           release.yml
                     ┌───────┼────────┐
                     ▼       ▼        ▼
               binary-with-  go-spa-  docker-
               sidecar       binary   image
                     │         │        │
                     └──► GitHub Release + ghcr.io
```

[tag-release.yml](.github/workflows/tag-release.yml) reads latest semver tag, increments, pushes. Then explicitly triggers [release.yml](.github/workflows/release.yml) with the resolved tag — no race on tag ref.

## CI/CD

Triggered by `tag-release.yml` (`workflow_dispatch` + `tag`) or by pushing a `v*` tag directly:

| Job | Produces | For consumer |
|-----|----------|--------------|
| `test` | Go tests + frontend build | Gate |
| `binary-with-sidecar` | Per-platform all-in-one binary | `make all` (§1) |
| `go-spa-binary` | Multi-platform Go+SPA binary, no sidecar | `make build` (§2) |
| `docker-image` | `ghcr.io/mystaline/tastastas:{ver,latest}` | `docker pull` (§3) |

`workflow_dispatch` on release.yml without `tag` = dry-run (builds but no push/upload).

## Docker image

Published to `ghcr.io/mystaline/tastastas` with tags:
- `vX.Y.Z` (semver)
- `X.Y` (major.minor)
- `latest`
