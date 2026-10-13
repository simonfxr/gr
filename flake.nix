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
        packages.default = pkgs.buildGo127Module {
          pname = "gr";
          version = "0.1.0";
          inherit src;
          vendorHash = "sha256-TfitwuwFA3qoOX01Cdtv8DOYVR/ItlQ515rUbyw1haE=";
        };

        devShells.default = pkgs.mkShell {
          buildInputs = [ pkgs.go_1_27 ];
        };
      }
    );
}
