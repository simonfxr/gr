{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        fs = pkgs.lib.fileset;
        src = fs.toSource {
          root = ./.;
          fileset = fs.unions [
            ./go.mod
            ./go.sum
            (fs.fileFilter (f: f.hasExt "go") ./.)
          ];
        };
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "gr";
          version = "0.1.0";
          inherit src;
          vendorHash = "sha256-4H1WiE3bYRn8Bw3UYSaFi/Kz/HXyFynTg0D8TPHuIb4=";
        };

        devShells.default = pkgs.mkShell {
          buildInputs = [ pkgs.go ];
        };
      }
    );
}
