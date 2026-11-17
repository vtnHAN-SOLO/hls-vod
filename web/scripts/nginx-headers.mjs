// Prints security-headers.json as nginx add_header lines (src/lib/nginxHeaders.ts).
// Run by docker/web.Dockerfile: node scripts/nginx-headers.mjs > security-headers.conf
// Node runs the .ts module directly (type stripping, Node 22.18+).
import { readFileSync } from "node:fs";

import { nginxHeaders } from "../src/lib/nginxHeaders.ts";

const headers = JSON.parse(readFileSync(new URL("../security-headers.json", import.meta.url), "utf8"));
process.stdout.write(nginxHeaders(headers));
