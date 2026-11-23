// Package assets provides embedded static assets for the PROBE web UI.
// The //go:embed directive must be in a package at the repo root to reach web/dist/.
package assets

import "embed"

// FS contains the embedded web/dist/ directory (production React build).
//
// web/dist is COMMITTED (P9-14): the //go:embed directive below makes the whole
// module unbuildable on a clean checkout when the directory is absent, so the
// build output is part of the tree rather than a local artefact. Rebuild and
// commit it with: cd web && npm ci && npm run build
// (toolchain used at P9-14: node v26.5.1, npm 11.17.0, vite 5.4.21, tsc 5.9.3).
//
//go:embed all:web/dist
var FS embed.FS