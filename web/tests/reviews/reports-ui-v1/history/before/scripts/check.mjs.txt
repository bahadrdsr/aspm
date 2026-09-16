import { run, npm, root } from "./process.mjs";
import { join, sep } from "node:path";
import { oracleEnvironment, withOracleGuard } from "./oracle-lib.mjs";

withOracleGuard(oracleEnvironment(root), () => {
  run(process.execPath, ["--test", join("scripts", "tests", "oracles.test.mjs")]);
  run(process.execPath, [join("scripts", "check-config.mjs")]);
  npm("check:lock");
  npm("typecheck");
  npm("test");
  npm("build");
  run("go", ["test", "-count=1", "-mod=readonly", `.${sep}...`]);
  run("go", ["build", "-mod=readonly", `.${sep}...`]);
});
console.log("M01 source/browser/build checks passed. Deployment and independent acceptance are separate.");
