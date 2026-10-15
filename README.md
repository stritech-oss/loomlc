# loomlc

> **loomlc** — *loom lifecycle*.

**loomlc** is a provider-agnostic orchestrator that runs **tasks** through a configurable **lifecycle of
agent steps**, where each step can be executed by a *different* AI provider's CLI — Anthropic
`claude`, Google `gemini`, OpenAI `codex`, etc. — over pluggable **task sources** (GitHub, GitLab,
Jira, Linear, local files) and **outputs** (PRs, MRs, comments, files).

It is **not a model harness.** It does not call model APIs or reimplement agent loops. It installs and
**shells out to each provider's own CLI**, and orchestrates them: pickup → run steps → verify → emit
output, with isolation, scoped credentials, backpressure, and resume.

## Origin

loomlc generalizes the SDLC agent flow built in the sibling repo `strive-ui.io`
(`plan → engineer ↔ QA → PR`, driven by a local runner + `.claude/**` agents/commands). That flow
proved the pattern; loomlc lifts it into a **provider-portable, source-agnostic, generic task-lifecycle
engine** — the engineering (SDLC) flow becomes just one shipped preset.

## Status

**Phase 0 in progress.** loomlc is being built in Go, starting with the `sdlc` lifecycle. `loomlc run`
takes one GitHub issue through it with `claude`, `loomlc feedback` answers the review on a pull request
it opened, and `loomlc tidy` folds the fixups that leaves before a merge; the scheduler and the other
provider adapters are still to come. See **[PLAN.md](./PLAN.md)** for the design and roadmap and
**[docs/examples/loomlc.yml](./docs/examples/loomlc.yml)** for a sample config.

## Install

Tagged releases carry prebuilt binaries for Linux and macOS, on amd64 and arm64:

```sh
v=v0.1.0 os=linux arch=amd64
curl -fsSLO "https://github.com/stritech-oss/loomlc/releases/download/$v/loomlc_${v}_${os}_${arch}.tar.gz"
tar -xzf "loomlc_${v}_${os}_${arch}.tar.gz"
mkdir -p ~/.local/bin && install -m755 "loomlc_${v}_${os}_${arch}/loomlc" ~/.local/bin/loomlc
```

`checksums.txt` beside them covers every archive, and `loomlc version` reports the tag it was built
from. With a Go toolchain, `go install github.com/stritech-oss/loomlc/cmd/loomlc@latest` works too.

loomlc orchestrates other programs rather than replacing them, so `git`, `gh`, and the provider CLI
each step names have to be on `PATH`. There are no Windows binaries: cancelling a step there can't
stop the processes it started, so an interrupted run would leave them behind.

## License

loomlc is licensed under the [Apache License 2.0](./LICENSE).
