{
  description = "Go Load Balancer";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in
    {
      devShells.${system}.default = pkgs.mkShell {
        name = "load-balancer";

        packages = with pkgs; [
          # Core Go toolchain
          go
          gopls # LSP
          gofumpt # Formatter

          # Development tools
          golangci-lint # Linter

          # Testing and debugging
          hey # HTTP load testing
          delve # Go debugger

          # Utilities for metrics/logs
          curl
          jq
        ];

        shellHook = ''
          # XDG-compliant Go directories
          export GOPATH="$HOME/.local/share/go"
          export GOMODCACHE="$HOME/.cache/go/mod"
          export GOCACHE="$HOME/.cache/go/build"

          # Add Go binaries to PATH
          export PATH="$GOPATH/bin:$PATH"

          echo "Load Balancer Development Environment"
          echo ""
          echo "Core Tools:"
          echo "  Go: $(go version)"
          echo "  gopls: $(gopls version)"
          echo ""
          echo "Testing:"
          echo "  hey -n 10000 -c 100 http://localhost:8080"
          echo "  curl http://localhost:9090/metrics | jq"
          echo ""
          echo "Go Paths:"
          echo "  GOPATH: $GOPATH"
          echo "  GOMODCACHE: $GOMODCACHE"
          echo ""
          exec fish
        '';
      };
    };
}
