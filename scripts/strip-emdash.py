#!/usr/bin/env python3
"""Strip em dashes from markdown, mechanically and losslessly.

Rules, in order:
  1. A table cell holding only an em dash becomes "-" (the n/a marker).
  2. An em dash alone at the end of a line becomes a comma on that line.
  3. An em dash starting a line moves back as a comma on the previous line.
  4. Inside a box-drawing line, " — " becomes " - " (same width).
  5. In a heading, " — " becomes ": ".
  6. Any other inline " — " becomes ", ".
Anything left over is reported, not guessed at.
"""
import re
import sys

CELL = re.compile(r"(?<=\|)(\s*)—(\s*)(?=\|)")


def strip(text: str) -> str:
    prev = None
    while prev != text:
        prev = text
        text = CELL.sub(lambda m: m.group(1) + "-" + m.group(2), text)

    lines = text.split("\n")
    for i, line in enumerate(lines):
        # em dash left dangling at end of a wrapped line
        lines[i] = re.sub(r"\s+—$", ",", lines[i])
    for i, line in enumerate(lines):
        m = re.match(r"^(\s*)—\s+", line)
        if m and i > 0 and lines[i - 1].strip():
            lines[i] = m.group(1) + line[m.end():]
            if not lines[i - 1].rstrip().endswith((",", ":", ";")):
                lines[i - 1] = re.sub(r"\s*$", ",", lines[i - 1])
    text = "\n".join(lines)

    lines = text.split("\n")
    for i, line in enumerate(lines):
        if "—" not in line:
            continue
        if re.search(r"[\u2500-\u257f]", line):        # box drawing: keep width
            lines[i] = re.sub(r" +— +", " - ", line)
        elif re.match(r"\s*#{1,6} ", line):             # heading: colon reads better
            lines[i] = re.sub(r" +— +", ": ", line)
        else:
            lines[i] = re.sub(r" +— +", ", ", line)
    text = "\n".join(lines)
    text = re.sub(r",[ \t]*,", ",", text)
    return text


def main(paths):
    left = 0
    changed = 0
    for p in paths:
        with open(p, encoding="utf-8") as f:
            src = f.read()
        if "—" not in src:
            continue
        out = strip(src)
        if out != src:
            with open(p, "w", encoding="utf-8") as f:
                f.write(out)
            changed += 1
        for n, line in enumerate(out.split("\n"), 1):
            if "—" in line:
                left += 1
                print(f"LEFTOVER {p}:{n}: {line.strip()[:100]}", file=sys.stderr)
    print(f"{changed} files rewritten, {left} em dashes left")
    return 1 if left else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
