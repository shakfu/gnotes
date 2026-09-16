# Modal reader

The read screen becomes one vim buffer beside the page tree. The rendered
reader and the separate editor screen merge into it. Decided 2026-09-16; see
REVIEW.md for the visual redesign this follows.

## Decided

- **Content pane is the page source in `internal/vim`.** Styled as the editor
  styles it today. The rendered page is `:preview`.
- **Modes are vim's.** NORMAL, INSERT, VISUAL, and the `:` and `/` lines.
- **`tab` / `shift-tab` move between panes** in NORMAL: tree, content, and the
  backlinks panel when it is shown.
- **Tree: `enter` opens the page** in the content pane.
- **`<` `>` jump the cursor to the previous / next link.** They replace vim's
  indent operators in NORMAL. VISUAL `<` `>` and INSERT `ctrl-t` `ctrl-d`
  still indent.
- **Arrows move the cursor**, as in vim.
- **`[` `]` move half a screen**, as `ctrl-u` `ctrl-d`.
- **`/` and `?` search the page**, with `n` `N`. Wiki-wide search is
  `:search <text>`; `ctrl-p` stays quick open.
- **Wiki actions are `:` commands in the content pane.** The tree keeps its
  single letters (`n c t O R`), since it holds no text.

## Defaults, not yet confirmed

| | Default | Alternative |
|-|-|-|
| Leaving a modified page (tree `enter`, following a link, `ctrl-o`) | refused: `:w` writes, `:e!` discards (vim without `hidden`) | write it automatically |
| `:q` | quits gwiki, refused with unsaved changes; `:wq` writes and quits | returns to the tree |
| Back through visited pages | `ctrl-o`, vim's jump back; `backspace` stays a motion | `backspace` |
| `enter` in NORMAL | follows the link under the cursor; elsewhere moves down a line | always follows, or nothing off a link |
| `/` in the tree, overview and lists | wiki search, as before: there is no page text to search | unbound |
| `tab` in INSERT | inserts, as in vim; panes switch only in NORMAL | always switches |

## Commands

Handled for the content pane through `vim.Hooks.Command`, and for the tree and
list screens through a `:` line of their own. One table serves both, and the
help.

| Command | Replaces |
|-|-|
| `:new [title]` | `n` |
| `:mv [path]` | `r` |
| `:broken` | `c` |
| `:tasks` | `t` |
| `:overview` | `O` |
| `:backlinks` | `b` |
| `:reload` | `R` |
| `:search <text>` | `/` (wiki-wide) |
| `:fix` | `f`, for the link under the cursor |
| `:external` | `E` |
| `:preview`, `:check` | unchanged |
| `:help` | `?` |
| `:w :q :wq :x :q! :e!` | vim's; `:q` now quits gwiki |

## Removed

- The rendered reader on the read screen, its link selection and `tab` link cycling.
- The editor screen (`screenEdit`) and `e`.
- `space` as half-page-down.

## Steps

1. **Buffer in the content pane.** Merge `screenEdit` into `screenRead`: a page
   opens into a `vim.Editor`; focus cycles with `tab`; the status label is the
   vim mode; drafts, outside-change handling and link underlining carry over.
2. **Wiki keys in NORMAL.** `<` `>` link jumps, `[` `]`, `enter` follow,
   `ctrl-o` back; the status bar names the link under the cursor.
3. **Commands.** The shared table, a `:` line for the tree and lists, help and
   hints from the table, README and CHANGELOG.
4. **REVIEW.md step 6.** OSC 52 for `"+y` (D5); broken links in `:preview` (D8).

Most reader tests in `internal/tui/wiki_test.go` press `tab` for links, `e`
for the editor or single letters in the reader, so steps 1-3 rewrite them.

## Progress

All four steps are done.

- The page pane is the buffer (`openBuffer`, `keyContent`, `viewBuffer`); the
  rendered reader, `screenEdit` and the reader keymap are gone. A link is
  matched by its whole source span, so the cursor may sit on a markdown link's
  label. Undoing back to the text as read clears the unsaved mark, which
  `internal/vim` does not do.
- `<` `>`, `[` `]`, `enter` and `ctrl-o` as decided; `<` `>` wrap at either end
  of the page with a message.
- `internal/tui/commands.go` holds the command table for the buffer and for a
  `:` line on the other screens, which also takes `:w`, `:q`, `:wq` and `:x`.
  The help lists the table.
- `"+y` writes OSC 52 through termenv; `:preview` marks broken links.
- The front matter line shows only over `:preview`, since the buffer shows the
  front matter as text.
- `/` in the tree, overview and lists stays wiki search (see the defaults).
