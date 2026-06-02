{
  lib,
  buildGo126Module,
  version ? "dev",
}:

buildGo126Module {
  pname = "kubectl-schedule";
  inherit version;

  src = lib.cleanSource ./.;

  vendorHash = "sha256-5Mi2Yg99KKQBCWzQKt+UeTpm/dBdmcXlp0fQwYLJV+A=";

  subPackages = [ "cmd/kubectl-schedule" ];

  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
  ];

  meta = {
    description = "kubectl plugin that simulates whether a Pod could be scheduled onto a specific Node";
    homepage = "https://github.com/alicek106/kubectl-scheduler";
    mainProgram = "kubectl-schedule";
    license = lib.licenses.unfree;
    platforms = lib.platforms.unix;
  };
}
