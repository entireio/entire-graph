"""A strict reader for the YAML subset that `.github/workflows/test.yml` uses.

The workflow contract test must assert on the parsed workflow, per job and per
step, and it runs on CI runners where no YAML library is installed (the Python
that `actions/setup-python` provides has only the standard library). Installing
one from the network inside a required job is not worth it, so this reads the
subset the workflow is written in and REFUSES anything else: a construct it
does not understand raises `UnsupportedYaml` with the line number rather than
being guessed at. `test_workflow_yaml.py` runs it over the real file.

Supported:
  - block mappings with plain keys, block sequences (`- item`), nesting by
    indentation with spaces;
  - plain, single-quoted and double-quoted scalars on one line; plain `true`,
    `false`, `null`/`~` and integers are typed, everything else is a string;
  - literal block scalars (`|`), clipped to one trailing newline;
  - one-line flow sequences `[a, b]` and flow mappings `{ a: b }` of scalars,
    also alone on the line after their key;
  - full-line comments and ` # comment` after a value.

Not supported (raises): tabs, anchors, aliases, tags, merge keys, document
markers, folded scalars and chomping indicators, multi-line plain or quoted
scalars, nested flow collections, complex or quoted keys, duplicate keys.
Mapping keys are always strings, so the workflow's `on` key is the string "on".
"""

from __future__ import annotations

import re
from typing import Any


class UnsupportedYaml(ValueError):
    """The text uses a construct outside the supported subset."""


_KEY = re.compile(r"^([A-Za-z_][A-Za-z0-9_.-]*):(?: (.*)|)$")
_INTEGER = re.compile(r"^-?(0|[1-9][0-9]*)$")
_PLAIN_FORBIDDEN_START = tuple("&*!|>%@`\"'#,[]{}")


class _Line:
    __slots__ = ("number", "indent", "text")

    def __init__(self, number: int, indent: int, text: str) -> None:
        self.number = number
        self.indent = indent
        self.text = text


def _fail(number: int, message: str) -> UnsupportedYaml:
    return UnsupportedYaml(f"line {number}: {message}")


def _strip_comment(text: str, number: int) -> str:
    """Remove a trailing ` # comment` that is outside quotes."""
    quote = ""
    for position, character in enumerate(text):
        if quote:
            if character == quote:
                quote = ""
        elif character in "\"'":
            # A quote only opens a quoted scalar at the start of a value or flow item.
            if position == 0 or text[position - 1] in " [{,":
                quote = character
        elif character == "#" and (position == 0 or text[position - 1] == " "):
            return text[:position].rstrip()
    if quote:
        raise _fail(number, "unterminated or multi-line quoted scalar")
    return text.rstrip()


def _scalar(text: str, number: int) -> Any:
    """Type one already comment-stripped, single-line scalar."""
    if text == "":
        raise _fail(number, "empty scalar")
    if text[0] == '"':
        if len(text) < 2 or text[-1] != '"' or '"' in text[1:-1]:
            raise _fail(number, "malformed double-quoted scalar")
        if "\\" in text:
            raise _fail(number, "escape sequences in double-quoted scalars are not supported")
        return text[1:-1]
    if text[0] == "'":
        if len(text) < 2 or text[-1] != "'":
            raise _fail(number, "malformed single-quoted scalar")
        body = text[1:-1]
        if "'" in body.replace("''", ""):
            raise _fail(number, "malformed single-quoted scalar")
        return body.replace("''", "'")
    if text.startswith(_PLAIN_FORBIDDEN_START) or text.startswith(("- ", "? ", ": ")) or text in "-?:":
        raise _fail(number, f"unsupported plain scalar {text!r}")
    if ": " in text or text.endswith(":"):
        raise _fail(number, f"plain scalar {text!r} contains a mapping indicator")
    if text in ("true", "false"):
        return text == "true"
    if text in ("null", "~"):
        return None
    if _INTEGER.match(text):
        return int(text)
    if re.match(r"^[-+.0-9]", text) and re.match(r"^[-+]?(\.[0-9]|[0-9])[0-9_.eE+-]*$", text):
        raise _fail(number, f"non-integer number {text!r} must be quoted")
    if text.lower() in ("yes", "no", "on", "off", "y", "n", "true", "false", "null"):
        raise _fail(number, f"ambiguous boolean-like scalar {text!r} must be quoted")
    return text


def _split_flow(body: str, number: int) -> list[str]:
    items: list[str] = []
    current = ""
    quote = ""
    for character in body:
        if quote:
            current += character
            if character == quote:
                quote = ""
        elif character in "\"'":
            quote = character
            current += character
        elif character in "[]{}":
            raise _fail(number, "nested flow collections are not supported")
        elif character == ",":
            items.append(current.strip())
            current = ""
        else:
            current += character
    if quote:
        raise _fail(number, "unterminated quoted scalar in a flow collection")
    if current.strip() or items:
        items.append(current.strip())
    if any(item == "" for item in items):
        raise _fail(number, "empty item in a flow collection")
    return items


def _flow(text: str, number: int) -> Any:
    if text[0] == "[":
        if text[-1] != "]":
            raise _fail(number, "flow sequence must close on the same line")
        return [_scalar(item, number) for item in _split_flow(text[1:-1], number)]
    if text[-1] != "}":
        raise _fail(number, "flow mapping must close on the same line")
    result: dict[str, Any] = {}
    for item in _split_flow(text[1:-1], number):
        match = _KEY.match(item)
        if match is None or match.group(2) is None:
            raise _fail(number, f"unsupported flow mapping entry {item!r}")
        if match.group(1) in result:
            raise _fail(number, f"duplicate key {match.group(1)!r}")
        result[match.group(1)] = _scalar(match.group(2).strip(), number)
    return result


def _value(text: str, number: int) -> Any:
    return _flow(text, number) if text[0] in "[{" else _scalar(text, number)


class _Parser:
    def __init__(self, text: str) -> None:
        self.raw = text.split("\n")
        self.lines: list[_Line] = []
        self.position = 0
        in_block_below: int | None = None
        for number, raw in enumerate(self.raw, 1):
            if "\t" in raw:
                raise _fail(number, "tab characters are not supported")
            if raw.endswith("\r"):
                raise _fail(number, "carriage returns are not supported")
            stripped = raw.strip()
            indent = len(raw) - len(raw.lstrip(" "))
            if in_block_below is not None:
                # Inside a literal block scalar: everything deeper than its key, and
                # blank lines, belong to the scalar verbatim.
                if stripped == "" or indent > in_block_below:
                    continue
                in_block_below = None
            if stripped == "" or stripped.startswith("#"):
                continue
            if stripped in ("---", "...") or stripped.startswith(("--- ", "%")):
                raise _fail(number, "document markers and directives are not supported")
            line = _Line(number, indent, stripped)
            self.lines.append(line)
            # A key or sequence entry introducing a literal block scalar.
            content = stripped[2:].lstrip() if stripped.startswith("- ") else stripped
            match = _KEY.match(content)
            if match is not None and match.group(2) is not None:
                introducer = _strip_comment(match.group(2).strip(), number)
                if introducer == "|":
                    in_block_below = indent + (len(stripped) - len(content))
                elif introducer[:1] in "|>":
                    raise _fail(number, f"unsupported block scalar indicator {introducer!r}")

    def peek(self) -> _Line | None:
        return self.lines[self.position] if self.position < len(self.lines) else None

    def block_scalar(self, key_line: _Line, key_indent: int) -> str:
        """The literal block scalar whose introducing key sits at key_indent."""
        collected: list[str] = []
        index = key_line.number  # zero-based index of the line after the key line
        while index < len(self.raw):
            raw = self.raw[index]
            if raw.strip() != "" and len(raw) - len(raw.lstrip(" ")) <= key_indent:
                break
            collected.append(raw)
            index += 1
        while collected and collected[-1].strip() == "":
            collected.pop()
        if not collected:
            raise _fail(key_line.number, "empty literal block scalar")
        first = next(raw for raw in collected if raw.strip() != "")
        block_indent = len(first) - len(first.lstrip(" "))
        lines: list[str] = []
        for offset, raw in enumerate(collected):
            if raw.strip() == "":
                lines.append("")
                continue
            if len(raw) - len(raw.lstrip(" ")) < block_indent:
                raise _fail(key_line.number + 1 + offset, "block scalar line is under-indented")
            lines.append(raw[block_indent:])
        return "\n".join(lines) + "\n"

    def node(self, indent: int) -> Any:
        line = self.peek()
        assert line is not None and line.indent == indent
        if line.text == "-" or line.text.startswith("- "):
            return self.sequence(indent)
        return self.mapping(indent)

    def nested(self, parent: _Line, parent_indent: int) -> Any:
        """The value of a key that has nothing after its colon."""
        following = self.peek()
        if following is None or following.indent <= parent_indent:
            # A sequence may sit at its key's own indentation; nothing else may.
            if (
                following is not None
                and following.indent == parent_indent
                and following.text.startswith("- ")
                and not parent.text.startswith("- ")
            ):
                return self.sequence(parent_indent)
            return None
        if following.text[0] in "[{":
            self.position += 1
            after = self.peek()
            if after is not None and after.indent > parent_indent:
                raise _fail(after.number, "multi-line flow collections are not supported")
            return _flow(_strip_comment(following.text, following.number), following.number)
        if not (following.text.startswith("- ") or _KEY.match(following.text)):
            raise _fail(following.number, "multi-line plain scalars are not supported")
        return self.node(following.indent)

    def entry(self, line: _Line, content: str, key_indent: int, into: dict[str, Any]) -> None:
        match = _KEY.match(content)
        if match is None:
            raise _fail(line.number, f"expected `key: value`, found {content!r}")
        key, rest = match.group(1), match.group(2)
        if key in into:
            raise _fail(line.number, f"duplicate key {key!r}")
        if key == "<<":
            raise _fail(line.number, "merge keys are not supported")
        rest = "" if rest is None else _strip_comment(rest.strip(), line.number)
        if rest == "":
            into[key] = self.nested(line, key_indent)
        elif rest == "|":
            into[key] = self.block_scalar(line, key_indent)
        else:
            into[key] = _value(rest, line.number)

    def mapping(self, indent: int) -> dict[str, Any]:
        result: dict[str, Any] = {}
        while True:
            line = self.peek()
            if line is None or line.indent < indent:
                return result
            if line.indent > indent:
                raise _fail(line.number, "unexpected indentation")
            if line.text.startswith("- "):
                raise _fail(line.number, "sequence entry inside a mapping")
            self.position += 1
            self.entry(line, line.text, indent, result)

    def sequence(self, indent: int) -> list[Any]:
        result: list[Any] = []
        while True:
            line = self.peek()
            if line is None or line.indent < indent:
                return result
            if line.indent > indent:
                raise _fail(line.number, "unexpected indentation")
            if not line.text.startswith("- "):
                if line.text == "-":
                    raise _fail(line.number, "empty or nested sequence entries are not supported")
                return result
            self.position += 1
            content = line.text[2:].lstrip()
            inner_indent = indent + (len(line.text) - len(content))
            if content.startswith("- "):
                raise _fail(line.number, "nested sequences on one line are not supported")
            if _KEY.match(content):
                item: dict[str, Any] = {}
                self.entry(line, content, inner_indent, item)
                while True:
                    following = self.peek()
                    if following is None or following.indent < inner_indent:
                        break
                    if following.indent > inner_indent:
                        raise _fail(following.number, "unexpected indentation")
                    if following.text.startswith("- "):
                        raise _fail(following.number, "sequence entry inside a mapping")
                    self.position += 1
                    self.entry(following, following.text, inner_indent, item)
                result.append(item)
            else:
                result.append(_value(_strip_comment(content, line.number), line.number))


def load(text: str) -> Any:
    """Parse text, or raise UnsupportedYaml naming the first unsupported line."""
    parser = _Parser(text)
    first = parser.peek()
    if first is None:
        raise UnsupportedYaml("document is empty")
    if first.indent != 0:
        raise _fail(first.number, "document must start at column 0")
    result = parser.node(0)
    leftover = parser.peek()
    if leftover is not None:
        raise _fail(leftover.number, "content after the end of the document")
    return result
