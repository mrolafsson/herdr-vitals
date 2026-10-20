# Changelog

## 0.2.0 - 2026-10-20

- **Your agents' AI usage and limits** (`a`): every account your Claude and
  Codex agents run on, as graphs on one clock: its session, week and
  per-model (Fable) limits and when they reset, its agents' tokens a
  minute, and how full each limit has been; then every agent's session on
  the same clock. The last three hours, or with `z` the day. `enter` goes
  to an agent. No clauth needed; with it, its names and its idle accounts.
- **On the machines' screen:** an AI card among the machines, or with one
  machine, the accounts in its details (every limit, or each week, as room
  allows). The header names the account nearest a limit.
- **A toast at 80% of an account's limit**, and at 95%, until it resets.
- **Screens scroll** when they don't fit: the details and the Agents screen
  (↑↓, PgUp/PgDn, Home/End, the wheel), saying what's above and below.
- **Easier to read in a pane:** small graphs, the name on its own line,
  gauges whose fill reads by shape as well as colour.
- **The pane's key closes it** again however it was opened.
- Usage graphs are smoothed over five minutes, and a rare burst (a session
  resumed, its context cached at once) runs off the top rather than
  flattening the rest.
- An account Anthropic turns away is asked again when it says, not later.

## 0.1.0 - 2026-10-20

First release.

- **A card for every machine:** this one, herdr's saved machines and any in
  `config.json`, with CPU, load a core, memory, swap, GPUs, disks, disk and
  network traffic and the busiest process. Details on Enter: graphs framed
  by what reaches their top, each core, every filesystem.
- **Disks before they're full:** when each filesystem will be, at the rate
  it's been filling.
- **Toasts when a machine runs low:** CPU above 90% for two minutes, memory
  above 90%, a disk 90% full or full within a day. Each once, until it eases
  off.
- **A sampler in the background**, started with herdr, one at a time, gone
  when herdr goes, so the monitor opens on full graphs; about 0.1% of a
  core for this machine, read straight from `/proc`.
- **The last day:** `z` in the details, a point a minute.
- **Every process, the whole screen:** `t`, by CPU or memory.
- **A popup or a pane** beside yours (`herdr-vitals.pane`, again to close);
  a pane a line high is one line of every machine's load.
- **With herdr-recap:** its header shows this machine's CPU and memory
  (`herdr-vitals stream`), and a click opens this popup and comes back.
- Linux and macOS, here and over ssh, with nothing to install on servers.
