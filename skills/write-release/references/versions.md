# Version files by ecosystem

Step 2 of the release uses this table to know where to look first. `git grep -nF` on the current
version still decides the bump list: a project often carries the version in more than one file.

| Ecosystem        | Version files                                  |
| ---------------- | ---------------------------------------------- |
| Node             | `package.json`, a `version.ts` constant        |
| Python (PEP 621) | `pyproject.toml` (`[project]` → `version`)     |
| Python (legacy)  | `setup.cfg`, `setup.py`                        |
| Python (runtime) | `__init__.py` or `_version.py` (`__version__`) |
| Rust             | `Cargo.toml` (`[package]` → `version`)         |
| Ruby             | `*.gemspec`, `lib/**/version.rb`               |
| Go               | — (tags are the version)                       |
| Generic          | `VERSION`, `.version`                          |
