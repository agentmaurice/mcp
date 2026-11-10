# Contract fixtures

Copied unchanged from pilote-maurice `contracts/template/v1/golden` (AGENT-1586,
commit 2f94c37667d43a7a2a5e9c7ee03c28f156f8741c of the worktree `pilote-maurice-agent-1586`,
not yet merged when copied). See `internal/template/contract/PIN`.
Injection cases are ported from `tests/test_contract.py` (`INJECTION_CASES`).

`golden/valid/dashboard` (six chart/v1 charts, AGENT-1582 lot 2) is copied unchanged from
commit ebbf0855f70eac610aca6f8d0307e583c671453e of the worktree `pilote-maurice-agent-1582`.

`golden/valid/support-reply` (message profile, mjml engine, AGENT-1614 lot 3) and the three
`message_*` strings added to `golden/context/i18n.fr.json` are copied unchanged from commit
9f3a8adbcdabbc57bebf7cc944f49ce8839efec4 of the worktree `pilote-maurice-agent-1614` (not yet merged
when copied). `golden/context/company.example.json` is not recopied (see `internal/template/contract/PIN`).

`golden/valid/qr-badge` (qr/v1 QR codes, AGENT-1919) is copied unchanged from commit
466083631794bc87b4d25d60a0a1052fb333938e of pilote-maurice main (merge of !1017).

`golden/valid/print-brochure` (print option, AGENT-1921) is copied unchanged from commit
dbfa465a9ee4a7e07e7f51659c91b517817066e0 of pilote-maurice main (merge of !1019).
