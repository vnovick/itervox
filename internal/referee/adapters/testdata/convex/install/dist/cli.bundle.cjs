// A fake, minimal stand-in for an installed deploy CLI bundle (test fixture).
"use strict";
const deployment = process.env.CONVEX_DEPLOYMENT;
const { CONVEX_DEPLOY_KEY: key } = process.env;
const url = process.env["CONVEX_SELF_HOSTED_URL"];
const home = process.env.HOME;
const notAVar = "XCONVEX_PREFIXED";
const lower = process.env.convex_lowercase;
if (!deployment) { console.log("set CONVEX_DEPLOYMENT"); }
