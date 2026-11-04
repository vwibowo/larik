---
name: larik-desktop
description: Control Larik Desktop tabs, terminal programs, sessions, splits, and notifications through the larik desktop CLI. Use when the user asks to open or manage something in Larik Desktop.
---

<!-- Managed by Larik Desktop. Install or remove it in Desktop Settings. -->

# Larik Desktop control

Use `larik desktop` for the user's visible Desktop window. Start with `larik desktop list --json` to see tab IDs and kinds. If the user asks to open something and Desktop is closed, run `larik desktop`, then retry. If no graphical session is available, explain the failure.

Inside a Larik Desktop session, use the native `desktop_tabs_list`, `desktop_terminal_open`, `desktop_terminal_read` and `desktop_terminal_send` tools for terminal tabs. They connect to Desktop directly and show the approval needed for terminal actions. Use `desktop_page_*` tools for browser pages. Use the CLI for other window actions or from agents without those native tools.

When calling the CLI through Larik's `bash` tool, set `"sandbox": false` and let the user approve the command. Desktop writes logs and state under Larik's user data directory and starts a graphical process, which the bash sandbox blocks. Run `larik desktop` in the foreground bash call; the launcher detaches the window itself. If another agent's sandbox denies a Desktop command, request that agent's equivalent unsandboxed approval and retry. Do not treat a sandbox denial as proof that Desktop cannot start.

For an existing tab, use its exact ID from `list --json`. In a Desktop terminal, `$LARIK_DESKTOP_TAB` is your own tab ID. Avoid `focused` when another tab may have the keyboard.

```sh
larik desktop open --shell .                    # terminal in this directory
larik desktop open --run 'opencode' .          # command in a terminal tab
larik desktop open --url localhost:3000        # browser tab
larik desktop open --files .                     # Files tab
larik desktop open --worktree --prompt 'Fix tests' .
larik desktop list --json
larik desktop send --tab TAB 'npm test'
larik desktop read --tab TAB --lines 50
larik desktop wait --tab TAB --state done,error,blocked --timeout 10m
larik desktop focus --tab TAB
larik desktop close --tab TAB
larik desktop notify --title 'Build' 'Ready to review'
larik desktop pane list --json
larik desktop pane focus --id PANE
larik desktop pane move --tab TAB --id PANE
larik desktop pane layout grid
larik desktop notifications list --json
larik desktop notifications clear
larik desktop project-layout save --project . --name Review
larik desktop project-layout open --project . --name Review
```

`open` prints the new tab ID. `--split` or `--below` places a new tab beside the current pane; `--background` leaves focus where it is. `send` presses Return unless `--no-enter` is given. `read` includes terminal scrollback. A live terminal's program keeps running when the hosted window closes.

`allow` and `deny` answer **Larik session** permission prompts only. Read what is being requested before answering; do not treat a blocked external agent's status as a Larik approval prompt. `close --force` can stop a working tab.

For browser-page interaction, load `larik-desktop-browser`. Run `larik desktop <command> -h` for current flag details.
