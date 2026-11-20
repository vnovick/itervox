// A fake, minimal stand-in for an installed CLI's JS files (test fixture).
"use strict";
const token = process.env.SUPABASE_ACCESS_TOKEN;
const password = process.env["SUPABASE_DB_PASSWORD"];
const project = process.env.SUPABASE_PROJECT_ID || process.env.SUPABASE_ACCESS_TOKEN;
const home = process.env.HOME;
const notAVar = "XSUPABASE_PREFIXED";
module.exports = { token, password, project, home, notAVar };
