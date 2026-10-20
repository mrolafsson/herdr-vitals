# Changelog

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
