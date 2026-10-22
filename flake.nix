{
  description = "swim: sane AI ops";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" "x86_64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      pkg = "github.com/gates-brightly/swimlane/internal/version";
    in
    {
      packages = forAll (pkgs: rec {
        # go.mod needs Go 1.27; nixpkgs' default buildGoModule may be older.
        swim = pkgs.buildGo127Module {
          pname = "swim";
          version = "0-unstable-" + builtins.substring 0 8 self.lastModifiedDate;
          src = self;
          # Hash of `go mod vendor`; when go.mod/go.sum change, the build fails
          # with "hash mismatch" and the "got:" line is the new value.
          vendorHash = "sha256-+xo4PDsZF8nUdbeVfCiaoP+Pxlbz+iwn60KTI4ZN0DY=";
          # Only the binary; `go test ./...` would run the e2e suite (~7 min).
          subPackages = [ "cmd/swim" ];
          env.CGO_ENABLED = 0;
          # The Makefile's version stamps (internal/version/version.go:27).
          ldflags = [
            "-s"
            "-w"
            "-X ${pkg}.BuildDate=${builtins.substring 0 8 self.lastModifiedDate}"
            "-X ${pkg}.Commit=${self.shortRev or self.dirtyShortRev or "unknown"}"
          ];
          meta = {
            description = "Run AI-written operational changes safely, with a human in the loop";
            mainProgram = "swim";
          };
        };
        default = swim;
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go_1_27 pkgs.gnumake pkgs.jq ]; };
      });
    };
}
