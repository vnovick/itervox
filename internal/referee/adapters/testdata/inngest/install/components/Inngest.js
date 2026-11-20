// A fake, minimal stand-in for an installed SDK (test fixture).
"use strict";
const env = process.env;
const signingKey = env.INNGEST_SIGNING_KEY;
const fallback = env["INNGEST_SIGNING_KEY_FALLBACK"];
const { INNGEST_EVENT_KEY: eventKey, INNGEST_ENV: branch } = process.env;
const baseUrl = env.INNGEST_BASE_URL || env.INNGEST_DEV;
const notAVar = "MYINNGEST_PREFIXED";
const lower = env.inngest_lowercase;
const node = env.NODE_ENV;
if (!signingKey) { console.warn("set INNGEST_SIGNING_KEY"); }
module.exports = { signingKey, fallback, eventKey, branch, baseUrl, notAVar, lower, node };
