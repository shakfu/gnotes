# TODO

## Critical

## High

## Medium

## Low

### Global notes

- [ ] Nothing marks the global notes in the terminal interface or browser view beyond the workspace name. A new project is named `global`; an adopted clone keeps its own name.
- [ ] `-g` is read only before the command. `gnotes ls -g` fails with "flag provided but not defined: -g" and no hint.
- [ ] A quoted `~` is not expanded: `gnotes -g init "~/notes"` creates a directory named `~`.

## Ideas

- `-C <dir>`, as in `git -C`: open the project at a path instead of discovering one. Useful for scripts and per-project MCP registration.
- A view across projects: open tasks from every known project and the global notes. It needs a list of known projects, which nothing records today.
