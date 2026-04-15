# Vendored OpenAPI specs

This directory holds the upstream Revolut OpenAPI specifications, vendored so
that the library builds reproducibly against a known input.

## Source

- Upstream: https://github.com/revolut-engineering/revolut-openapi
- Pinned tag: see [`VERSION`](./VERSION)

## Contents

| Local file          | Upstream file (under `yaml/`) | API                          |
| ------------------- | ----------------------------- | ---------------------------- |
| `business.yaml`     | `business.yaml`               | Revolut Business API         |
| `merchant.yaml`     | `merchant-<date>.yaml`        | Revolut Merchant API         |
| `open-banking.yaml` | `open-banking.yaml`           | Revolut Open Banking API     |
| `crypto-ramp.yaml`  | `crypto-ramp-<ver>.yaml`      | Revolut Crypto Ramp API      |
| `revolut-x.yaml`    | `revolut-x.yml`               | Revolut X API                |

For the versioned APIs (`merchant`, `crypto-ramp`), the vendored file is the
latest dated/numbered variant available at the pinned upstream tag.

## Updating

The pinned tag lives in the root `Taskfile.yml` (`UPSTREAM_TAG`). To bump:

1. Edit `UPSTREAM_TAG` in `Taskfile.yml`.
2. Run `task specs:update` to re-fetch all files and refresh `VERSION`.
3. Review the diff, regenerate any dependent code, update the library, commit.

## Verifying

`task specs:check` confirms local files match upstream at the pinned tag.
