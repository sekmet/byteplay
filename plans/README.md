# BytePlay v0.1.1 implementation leaves

Source of truth: attached L3 specification BP-L3-2026-10-29 rev 0.1.1. Baseline `bb366d5102085477b7270d7982e60712cc23fb41`.

Execute M0→M8 serially because main.go and the frontend are shared integration points. Each leaf must show failing test → small implementation → passing focused and full checks → independent review. Do not declare unexecuted gates green.
