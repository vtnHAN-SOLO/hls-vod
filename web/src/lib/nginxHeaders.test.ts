import { describe, expect, it } from "vitest";

import headers from "../../security-headers.json";
import { nginxHeaders } from "./nginxHeaders";

describe("nginxHeaders", () => {
  it("writes one add_header ... always line per header of security-headers.json", () => {
    const out = nginxHeaders(headers);
    const lines = out.split("\n").filter((l) => l.startsWith("add_header"));
    expect(lines).toHaveLength(Object.keys(headers).length);
    expect(lines).toContain(`add_header Content-Security-Policy "${headers["Content-Security-Policy"]}" always;`);
    expect(lines).toContain('add_header X-Content-Type-Options "nosniff" always;');
  });

  it.each([
    ["a double quote", { "X-A": 'a"; return 200 "x' }],
    ["an nginx variable", { "X-A": "a $uri" }],
    ["a backslash", { "X-A": "a\\" }],
    ["a newline", { "X-A": "a\nadd_header X-B b;" }],
    ["an empty value", { "X-A": "" }],
    ["a bad name", { "X A": "a" }],
  ])("refuses %s", (_name, h) => {
    expect(() => nginxHeaders(h)).toThrow();
  });

  it("refuses no headers at all", () => {
    expect(() => nginxHeaders({})).toThrow();
  });
});
