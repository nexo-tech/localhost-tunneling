{
  description = "A reproducible Go development environment using flakes";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };
      in {
        devShells.default = pkgs.mkShell {
          buildInputs = [
            pkgs.go
            pkgs.gopls          # Go language server (for editors like VSCode)
            pkgs.go-tools       # Various go tooling
            pkgs.git            # Because you're a developer, allegedly
          ];
          shellHook = ''
              export GOPATH=$(pwd)/.gopath
              export GOBIN=$GOPATH/bin
              export PATH=$GOBIN:$PATH
          '';
        };
      }
    );
}
