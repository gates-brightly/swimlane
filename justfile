binary   := "swim"
pkg      := "./cmd/swim"
bin_dir  := "bin"
link_dir := env("LINK_DIR", home_directory() / ".local/bin")
module   := "github.com/gates-brightly/swimlane"
# Where `go install` puts binaries: $GOBIN, else $GOPATH/bin.
go_bin_dir := `d=$(go env GOBIN 2>/dev/null); echo "${d:-$(go env GOPATH 2>/dev/null)/bin}"`
commit     := env("COMMIT", `git rev-parse --short HEAD 2>/dev/null || true`)
build_date := env("BUILD_DATE", `date -u +%Y%m%d`)
# The release tag (v0.<YYYYMMDD>.<index>) when HEAD is one and the tree is clean.
build_tag  := env("BUILD_TAG", `test -z "$(git status --porcelain 2>/dev/null)" && git tag --points-at HEAD 2>/dev/null | grep -E '^v0\.[0-9]{8}\.[1-9][0-9]*$' | sort -t. -k3,3n | tail -1 || true`)
version_pkg := module / "internal/version"
ldflags := "-s -w -X " + version_pkg + ".BuildDate=" + build_date + " -X " + version_pkg + ".Commit=" + commit + " -X " + version_pkg + ".BuildTag=" + build_tag

# Show available recipes
default:
    @{{just_executable()}} --list --unsorted

# Build the swim binary into ./bin
build:
    go build -ldflags "{{ldflags}}" -o {{bin_dir}}/{{binary}} {{pkg}}

# Install swim from this checkout into $GOBIN (or $GOPATH/bin)
install:
    go install -ldflags "{{ldflags}}" {{pkg}}
    @{{just_executable()}} which

# Tag a release after checking its date and index (against origin's tags), CHANGELOG.md and a clean tree (v0.<YYYYMMDD>.<index>); never pushes
release v:
    @bash ./scripts/release.sh {{v}}

# Tag HEAD (clean, already on origin/main) with the next release tag, v0.<today>.<index>; push it to origin (GitHub, authoritative), then fj (Forgejo) (DRY=1: print only)
next-tag:
    @bash ./scripts/next-tag.sh

# Push every release tag origin (GitHub) has that fj (Forgejo) lacks (DRY=1: print only)
mirror-tags:
    @bash ./scripts/mirror-tags.sh

# go install the published swim (v0.20261014.1 to pin; default latest)
install-latest v="latest":
    GOPRIVATE="$(p=$(go env GOPRIVATE); echo "${p:+$p,}github.com/gates-brightly/*")" go install {{module}}/cmd/swim@{{v}}
    @{{just_executable()}} which

# Remove the go-installed swim from $GOBIN (or $GOPATH/bin)
uninstall:
    #!/usr/bin/env bash
    f="{{go_bin_dir}}/{{binary}}"
    if [ -f "$f" ] && [ ! -L "$f" ]; then rm "$f" && echo "removed $f"; else echo "no go-installed swim at $f"; fi
    {{just_executable()}} which

# Symlink bin/swim into LINK_DIR (default ~/.local/bin) so it's on your PATH
link: build
    @mkdir -p "{{link_dir}}"
    ln -sf "{{justfile_directory()}}/{{bin_dir}}/{{binary}}" "{{link_dir}}/{{binary}}"
    @case ":$PATH:" in *":{{link_dir}}:"*) ;; *) echo "note: {{link_dir}} is not on your PATH; add: export PATH=\"{{link_dir}}:\$PATH\"";; esac

# Remove the symlink created by 'just link'
unlink:
    @if [ -L "{{link_dir}}/{{binary}}" ]; then rm "{{link_dir}}/{{binary}}" && echo "removed {{link_dir}}/{{binary}}"; else echo "no symlink at {{link_dir}}/{{binary}}"; fi
    @{{just_executable()}} which

# Show every swim on your PATH, which one runs, and where each came from
which:
    #!/usr/bin/env bash
    found=0; seen=":"; first=1
    IFS=:; for d in $PATH; do
      f="$d/{{binary}}"; [ -x "$f" ] && [ ! -d "$f" ] || continue
      case "$seen" in *":$f:"*) continue;; esac; seen="$seen$f:"; found=1
      if [ -L "$f" ] && [ "$(readlink "$f")" = "{{justfile_directory()}}/{{bin_dir}}/{{binary}}" ]; then src="just link (this checkout)"
      elif [ "$d" = "{{go_bin_dir}}" ]; then src="go install"
      else src="other"; fi
      ver=$("$f" --version 2>/dev/null | head -1)
      if [ $first = 1 ]; then mark="* runs"; first=0; else mark="  hidden"; fi
      printf '%s  %-40s %-26s %s\n' "$mark" "$f" "$src" "${ver:-?}"
    done
    unset IFS
    [ $found = 1 ] || echo "no swim on your PATH (just link, just install or just install-latest)"
    for d in "{{go_bin_dir}}" "{{link_dir}}"; do
      [ -x "$d/{{binary}}" ] || continue
      case ":$PATH:" in *":$d:"*) ;; *)
        echo "note: $d/{{binary}} is installed, but $d is not on your PATH. Add it (e.g. to ~/.zshrc):"
        echo "        export PATH=\"$d:\$PATH\"";;
      esac
    done

# Build and run swim (just run <args>)
run *args: build
    ./{{bin_dir}}/{{binary}} {{args}}

# Run the unit tests (fast; no e2e)
test:
    go test $(go list ./... | grep -v /internal/e2e)

# Run the end-to-end tests: Go e2e suite + e2e/ scenarios; `just e2e dag99 ...` runs just those scenarios
e2e *scenarios:
    {{ if scenarios == "" { "go test -count=1 ./internal/e2e/..." } else { "./e2e/run.sh " + scenarios } }}

# Run tests with a coverage report
cover:
    go test -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | tail -1

# Format Go sources
fmt:
    gofmt -s -w .

# Run go vet
vet:
    go vet ./...

# Tidy go.mod / go.sum
tidy:
    go mod tidy

# Run this repo's CI rounds (lanes 1-4 and 6: checks, then e2e, fault, security, fuzz side by side) through swim ci, as GitHub Actions does
ci: build
    {{bin_dir}}/{{binary}} ci --rerun 1 2 3 4 6

# gofmt, vet, unit and e2e tests (the same checks as the CI rounds, without swim)
check:
    @test -z "$(gofmt -s -l .)" || { echo "gofmt needed on:"; gofmt -s -l .; exit 1; }
    go vet ./...
    go test ./...

# Remove build and coverage artifacts
clean:
    rm -rf {{bin_dir}} coverage.out
