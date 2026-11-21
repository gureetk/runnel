# Working in this repository

Runnel is a lean media server for a home library, with its own clients. This file is for anyone working here, person or Claude session.

Read `docs/00-start-here.md` first. Then read the document for the part you are working on. `docs/01-roadmap.md` says which stage is being built, what it builds, and what it must prove.

## Scope

- Build what the current stage lists, and no more. Nothing is built before its stage.
- The list of things that are not being built is in `docs/01-roadmap.md`. Adding one needs an entry in `docs/08-decisions.md` first.
- A third-party Go module needs an entry in `docs/08-decisions.md` first. So does a runtime dependency of the web interface.
- After a change to the server, look at its memory. The budget is in `docs/02-architecture.md`, section 8.

## Documents

- A change in behaviour updates the document that describes it, in the same pull request.
- A decision that changes a document's premise is written into `docs/08-decisions.md` first.
- If code and a document disagree, one of them is wrong. The same pull request fixes it.
- Say where a number comes from. Mark what is untested as untested. Do not state from memory how ffmpeg, mpv, a browser, Android, iOS or a provider behaves when it can be checked.

## Git

- Work on a branch. Open a pull request. The maintainer merges it with Squash and merge. Never push to `main`.
- Commits are authored as `gureet <noreply@git.gureet.ca>`.
- A commit made with Claude's help may end with one trailer: `Co-Authored-By: Claude <model name> <noreply@anthropic.com>`.
- Never add a `Claude-Session:` trailer. Never put a link to a Claude chat, session or project in a commit, a pull request title or description, an issue or a comment. After opening a pull request, read its description back and remove any such link.
- Write about the work, not about who asked for it. Commits, pull requests, issues and documents state the problem, the decision or the change.

## Never in this repository

- Anything about a particular setup: hardware models, what a library holds, how its files are named, addresses, device names, or any measurement taken on someone's own machines. A document that needs a measurement uses one from a development sandbox and says so.
- Research notes, and the scripts and test rigs written during the research. The repository's tests are written for it.
- Real file names, release names, tokens or API keys. Test data is synthetic.

## Code

- Every source file starts with `// SPDX-License-Identifier: GPL-3.0-or-later`, in the file's own comment style.
- The layout and the rules for the code are in `docs/02-architecture.md`, section 4.
- The SQLite driver is C: building needs cgo (`CGO_ENABLED=1`) and a C compiler, and the `sqlite_fts5` build tag. The integration tests need `ffmpeg` and `ffprobe` on `PATH`.
- Before pushing: `gofmt -l .` prints nothing, and `go vet -tags sqlite_fts5 ./...`, `go test -tags sqlite_fts5 ./...` and `go build -tags sqlite_fts5 ./cmd/runnel` pass. CI runs the same.
