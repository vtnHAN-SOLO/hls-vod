// The SPA's response headers (security-headers.json) as nginx add_header
// lines, generated when the web image is built (scripts/nginx-headers.mjs),
// so the one JSON file stays the only place they are written: vite preview
// serves the same object.
//
// Every line ends in "always", so a 404 carries the headers too. A value is
// written inside double quotes, so a quote, a backslash, a "$" (an nginx
// variable), ";" outside the quotes or a control character would change the
// config: such a value throws and the image build fails.

const NAME = /^[A-Za-z][A-Za-z0-9-]*$/;
// eslint-disable-next-line no-control-regex -- control characters are exactly what is refused
const UNSAFE_VALUE = /["\\$\u0000-\u001f\u007f]/;

export function nginxHeaders(headers: Record<string, string>): string {
  const names = Object.keys(headers);
  if (names.length === 0) throw new Error("no headers");
  const lines = names.map((name) => {
    const value = headers[name] ?? "";
    if (!NAME.test(name)) throw new Error(`header name ${JSON.stringify(name)} is not a token`);
    if (value === "" || UNSAFE_VALUE.test(value)) {
      throw new Error(`header ${name}: the value is empty or holds a quote, backslash, $ or control character`);
    }
    return `add_header ${name} "${value}" always;`;
  });
  return (
    "# Generated from web/security-headers.json when the image is built; do not edit.\n" +
    "# Included in every location that sets its own headers: nginx drops the\n" +
    "# parent's add_header lines in a location that adds any.\n" +
    lines.join("\n") +
    "\n"
  );
}
