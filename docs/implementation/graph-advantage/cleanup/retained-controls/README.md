# Retained historical controls

This directory keeps one authored source for each distinct historical control
content group removed from scattered evidence directories. Files without a
redaction are byte-identical to the recorded original SHA-256. Files with
configuration-bound paths retain the source with explicit `${...}` placeholders;
`../historical-control-provenance.json` binds the original and retained digests
and records every redaction count.

These sources preserve control logic for reconstruction and review. They are not
current execution instructions. The separate completion diagnostic remains
paused and incomplete. Placeholder values must be supplied deliberately before
any future adaptation.
