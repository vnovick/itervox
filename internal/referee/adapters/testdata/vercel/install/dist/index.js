// A fake, minimal stand-in for an installed deploy CLI bundle (test fixture).
var e=process.env;var o=e.VERCEL_ORG_ID,p=e.VERCEL_PROJECT_ID;function t(){return e["VERCEL_TOKEN"]||e.CI}
module.exports={o,p,t,scope:process.env.VERCEL_ORG_ID};
