import { describe, expect, it } from "vitest";

import { cueAt, parseThumbnails } from "./thumbnails";

const url = "http://127.0.0.1:5173/media/47d8916f-6f0e-43dc-b10a-888b884791b6/thumbnails.vtt";

// Exactly what internal/ffmpeg thumbnailsVTT writes (10x10 sheets, 160x90).
const vtt = `WEBVTT

00:00:00.000 --> 00:00:10.000
thumbs/sprite_000.jpg#xywh=0,0,160,90

00:00:10.000 --> 00:00:20.000
thumbs/sprite_000.jpg#xywh=160,0,160,90

00:16:30.000 --> 00:16:40.000
thumbs/sprite_000.jpg#xywh=1440,810,160,90

00:16:40.000 --> 00:16:45.500
thumbs/sprite_001.jpg#xywh=0,0,160,90
`;

describe("parseThumbnails", () => {
  const cues = parseThumbnails(vtt, url);

  it("reads every cue with times, tile and absolute sprite URL", () => {
    expect(cues).toHaveLength(4);
    expect(cues[1]).toEqual({
      start: 10,
      end: 20,
      image: "http://127.0.0.1:5173/media/47d8916f-6f0e-43dc-b10a-888b884791b6/thumbs/sprite_000.jpg",
      x: 160,
      y: 0,
      width: 160,
      height: 90,
    });
    expect(cues[3]?.image.endsWith("/thumbs/sprite_001.jpg")).toBe(true);
    expect(cues[3]?.end).toBe(1005.5);
  });

  it("accepts CRLF, a BOM, MM:SS timestamps and cue settings", () => {
    const odd = "\uFEFFWEBVTT\r\n\r\n1\r\n00:05.000 --> 00:10.000 align:start\r\nthumbs/a.jpg#xywh=1,2,3,4\r\n";
    expect(parseThumbnails(odd, url)).toEqual([
      expect.objectContaining({ start: 5, end: 10, x: 1, y: 2, width: 3, height: 4 }),
    ]);
  });

  it("skips malformed cues instead of failing", () => {
    const bad = `WEBVTT

00:00:00.000 --> 00:00:10.000
thumbs/no-fragment.jpg

00:00:10.000 --> 00:00:05.000
thumbs/backwards.jpg#xywh=0,0,1,1

not a timing line
thumbs/x.jpg#xywh=0,0,1,1

00:00:20.000 --> 00:00:30.000
thumbs/good.jpg#xywh=0,0,1,1
`;
    const got = parseThumbnails(bad, url);
    expect(got.map((c) => c.image.split("/").pop())).toEqual(["good.jpg"]);
  });

  it("returns nothing for a file that is not WebVTT", () => {
    expect(parseThumbnails("<html>404</html>", url)).toEqual([]);
  });
});

describe("cueAt", () => {
  const cues = parseThumbnails(vtt, url);

  it.each([
    [0, 0],
    [9.99, 0],
    [10, 160],
    [995, 1440],
    [1004, 0],
  ])("t=%s picks the tile at x=%s", (t, x) => {
    expect(cueAt(cues, t)?.x).toBe(x);
  });

  it("is null in a gap, before the start and after the end", () => {
    expect(cueAt(cues, 500)).toBeNull();
    expect(cueAt(cues, -1)).toBeNull();
    expect(cueAt(cues, 1005.5)).toBeNull();
    expect(cueAt([], 1)).toBeNull();
  });
});
