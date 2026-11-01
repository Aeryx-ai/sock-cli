# Contributing

1. Fork the repo and branch from `main`.
2. Check out `sock-contract` and `sock-client` beside it as `../contract` and `../client` (see `AGENTS.md`).
3. Make the change in conventional Go style, with a test. A bug fix starts with a test that fails without it.
4. Run `make check`. It must pass; CI runs the same target.
5. Open a pull request against `main` saying what changed and why.

Report security issues privately as `SECURITY.md` describes, not in a pull request or issue.

By contributing you agree your contribution is licensed under the MIT license that covers this repository (inbound = outbound).
