# Releasing

**Do not use `make release`.** Trigger a release from CI:

```bash
# Stable release from main (bump patch, default)
gh workflow run tag-release.yml --field bump=patch

# Bump minor/major
gh workflow run tag-release.yml --field bump=minor
gh workflow run tag-release.yml --field bump=major

# Explicit version (skips auto-detection)
gh workflow run tag-release.yml --field version=v1.2.3

# Alpha release from a feature branch (--ref = branch to release)
gh workflow run tag-release.yml --ref feat/feature-x --field bump=patch
```

## Branch awareness

- Releasing from **main** → stable tag `vX.Y.Z`
- Releasing from **any other branch** (`--ref <branch>`) → pre-release tag `vX.Y.Z-alpha`
- Auto-bump ignores an existing `-alpha` suffix when computing the next version
- Explicit `version` input: `-alpha` appended automatically on non-main branches

## Flow

```
tag-release.yml  ──resolves tag──►  git tag vX.Y.Z[-alpha]  ──pushes──►  gh workflow run release.yml --field tag=vX.Y.Z[-alpha]
                                                                           │
                                   ┌───────────────────────────────────────┘
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
| `docker-image` | `ghcr.io/mystaline/tastastas:{ver,latest|alpha}` | `docker pull` (§3) |

`workflow_dispatch` on release.yml without `tag` = dry-run (builds but no push/upload).

## Docker image

Published to `ghcr.io/mystaline/tastastas` with tags:

- `vX.Y.Z` (semver, always)
- `X.Y` (major.minor, stable only)
- `latest` (stable only — main releases)
- `alpha` (pre-release only — non-main branch releases)
