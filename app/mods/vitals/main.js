// How a machine is doing, live, in a window that stays beside your work: its
// CPU as a graphic equalizer (a band a core, lit segment by segment, green,
// then yellow, then red, with a peak that holds and falls), memory, load and
// network as meters, and its disks. The machine the agent you're in runs on,
// first; the others a click away in the top bar.
//
// The readings are herdr-vitals' (`app`), the TUI plugin's own code, run on
// the machine shown. The mod fetches the herdr-vitals release it needs onto
// each machine it asks, the first time.

import { View, div } from "gpui-kit";
import { v_flex, h_flex, Popup } from "gpui-base";
import { agents, machines, spaces, exec, theme, props, closeOverlay, nextCommand } from "herdr";
import { tones, tint, levelColor, bytes, bytesOf, icon, MONO } from "./look.js";

// The herdr-vitals this mod is part of (its release; the repo's tests hold it
// to herdr-plugin.toml's version).
export const VITALS_VERSION = "0.3.0";

// Puts that release on a machine, once: into a folder of its own version, so
// it never touches a herdr-vitals installed for the TUI (it shares its
// sampler and history). Checked against the release's checksums before it's
// used. Prints the binary's path.
const PROVISION = String.raw`
v=$1
dest="$HOME/.local/share/herdr-vitals/app/$v"
bin="$dest/herdr-vitals"
[ -x "$bin" ] && { printf '%s' "$bin"; exit 0; }
case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) echo "no herdr-vitals for $(uname -s)" >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "no herdr-vitals for $(uname -m)" >&2; exit 1 ;; esac
name="herdr-vitals_$v""_$os""_$arch.tar.gz"
url="https://github.com/mrolafsson/herdr-vitals/releases/download/v$v"
tmp=$(mktemp -d) || exit 1
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$url/$name" -o "$tmp/$name" && curl -fsSL "$url/checksums.txt" -o "$tmp/sums" || { echo "couldn't download herdr-vitals $v" >&2; exit 1; }
want=$(grep " $name\$" "$tmp/sums" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then have=$(sha256sum "$tmp/$name" | cut -d' ' -f1); else have=$(shasum -a 256 "$tmp/$name" | cut -d' ' -f1); fi
[ -n "$want" ] && [ "$want" = "$have" ] || { echo "herdr-vitals $v didn't match its checksum" >&2; exit 1; }
tar -xzf "$tmp/$name" -C "$tmp" herdr-vitals || exit 1
mkdir -p "$dest" && mv "$tmp/herdr-vitals" "$bin.new" && chmod 755 "$bin.new" && mv "$bin.new" "$bin" || exit 1
printf '%s' "$bin"
`;

const binaries = new Map();
function binary(machine) {
  const k = machine ?? "";
  if (!binaries.has(k)) {
    const p = exec("sh", ["-c", PROVISION, "sh", VITALS_VERSION], { on: machine }).then((r) => {
      if (r.code !== 0 || !r.stdout.trim()) throw new Error(r.stderr.trim().split("\n").pop() || `herdr-vitals: exit ${r.code}`);
      return r.stdout.trim();
    });
    p.catch(() => binaries.delete(k));
    binaries.set(k, p);
  }
  return binaries.get(k);
}

// One request to herdr-vitals on a machine (null: this one).
async function ask(req, machine) {
  const bin = await binary(machine);
  const r = await exec("sh", ["-c", 'exec "$0" app "$1"', bin, JSON.stringify(req)], { on: machine });
  let res;
  try {
    res = JSON.parse(r.stdout);
  } catch {
    throw new Error(r.stderr.trim().split("\n").pop() || `herdr-vitals: exit ${r.code}`);
  }
  if (res?.error) throw new Error(res.error);
  return res;
}

// How often a machine is read while it's shown.
const EVERY_MS = 2000;

// The equalizer: as many bands as fit (a core each; more cores than bands
// share one), each this many segments tall; where yellow and red start.
const MAX_BANDS = 24;
// A Mac's bands, the last readings of all its cores: fewer, so wider.
const HISTORY_BANDS = 20;
const SEGMENTS = 16;
const WARN_AT = 10;
const HOT_AT = 14;
// A peak holds this many readings, then falls a segment a reading.
const PEAK_HOLD = 2;
// The window: as wide either way; normal this tall (and scrolls), compact as
// tall as what's in it.
const WINDOW_W = 480;
const NORMAL_H = 720;
// The meters: as many segments, each this wide.
const METER_SEGMENTS = 22;
const METER_SEG_W = 7;

// A disk by its last part: "/", "Data", "OrbStack".
const diskName = (mount) => (mount === "/" ? "/" : mount.split("/").filter(Boolean).pop() ?? mount);

const machineOf = (address) => (address?.includes("/") ? address.split("/")[0] : null);

export default class Vitals extends View {
  init(_props, cx) {
    const p = props() ?? {};
    this.demo = Boolean(p.demo);
    this.reading = null;
    this.err = "";
    this.peaks = [];
    // Compact: the CPU, memory and every disk together; normal: all of it.
    this.compact = localStorage.getItem("compact") === "1";
    this.cpuHist = []; // this machine's CPU, a reading at a time, for a Mac's bands
    this.demoMachines = [];
    // The machine the agent you're in runs on; else the shown space's.
    const focused = agents().find((a) => a.focused);
    const space = spaces().find((s) => s.active);
    this.machine = focused ? machineOf(focused.address) : space?.machine ?? null;

    cx.spawn(async (cx) => {
      if (this.demo) await this.startDemo(cx);
      this.read(cx);
    });
    cx.timer.every(EVERY_MS, (cx) => this.read(cx));

    // Key bindings and `herdr-app do`: vitals.close, vitals.demo, and
    // vitals.next / vitals.prev for the machine shown.
    cx.spawn(async (cx) => {
      for (;;) {
        const c = await nextCommand();
        if (c === "close") this.picking ? (this.picking = false) : this.close();
        else if (c === "demo") {
          this.demo = !this.demo;
          this.reset();
          if (this.demo) await this.startDemo(cx);
          else this.machine = null;
          this.read(cx);
        } else if (c === "next" || c === "prev") this.step(c === "next" ? 1 : -1, cx);
        cx.notify();
      }
    });
  }

  async startDemo(cx) {
    this.demoMachines = (await ask({ op: "machines" }, null).catch(() => ({ machines: [] }))).machines;
    this.machine = this.demoMachines[0] ?? null;
    cx.notify();
  }

  reset() {
    this.reading = null;
    this.peaks = [];
    this.cpuHist = [];
    this.err = "";
  }

  close() {
    closeOverlay(props().overlay);
  }

  // The machines in the top bar: this one and every connected one; the
  // demo's made-up ones in the demo.
  machineList() {
    if (this.demo) return this.demoMachines.map((name) => ({ key: name, label: name, remote: true }));
    return [{ key: null, label: "this computer", remote: false },
      ...machines().filter((m) => m.name).map((m) => ({ key: m.name, label: m.name, remote: true, down: m.layout === "unavailable" }))];
  }

  pick(key, cx) {
    if (key === this.machine) return;
    this.machine = key;
    this.reset();
    this.read(cx);
    cx.notify();
  }

  step(d, cx) {
    const list = this.machineList();
    const i = Math.max(0, list.findIndex((m) => m.key === this.machine));
    this.pick(list[(i + d + list.length) % list.length].key, cx);
  }

  read(cx) {
    if (this.reading_) return;
    this.reading_ = true;
    const machine = this.machine;
    const req = this.demo ? { op: "now", demo: true, machine } : { op: "now" };
    cx.spawn(async (cx) => {
      try {
        const r = await ask(req, this.demo ? null : machine);
        if (machine !== this.machine) return;
        this.take(r);
        this.err = "";
      } catch (e) {
        if (machine === this.machine) this.err = String(e?.message ?? e);
      } finally {
        this.reading_ = false;
      }
      cx.notify();
    });
  }

  // A reading in: the bands' levels, and each band's peak, held a moment
  // then falling.
  take(r) {
    if (!this.cpuHist.length && r.history?.length) this.cpuHist = r.history.map((p) => p.c).slice(-HISTORY_BANDS);
    if (!r.warming) this.cpuHist.push(r.cpu);
    if (this.cpuHist.length > HISTORY_BANDS) this.cpuHist = this.cpuHist.slice(-HISTORY_BANDS);
    this.reading = r;
    const levels = this.bands().map((v) => Math.round((Math.max(0, Math.min(100, v)) / 100) * SEGMENTS));
    this.peaks = levels.map((lit, i) => {
      const pk = this.peaks[i] ?? { at: 0, hold: 0 };
      if (lit >= pk.at) return { at: lit, hold: PEAK_HOLD };
      if (pk.hold > 0) return { at: pk.at, hold: pk.hold - 1 };
      return { at: Math.max(lit, pk.at - 1), hold: 0 };
    });
  }

  // What the bands show: each core (several to a band past MAX_BANDS); on a
  // Mac, which doesn't say a core's share, the last readings of all of them.
  bands() {
    const r = this.reading;
    if (!r) return [];
    const cores = r.perCore ?? [];
    if (cores.length) {
      const per = Math.ceil(cores.length / MAX_BANDS);
      const out = [];
      for (let i = 0; i < cores.length; i += per) {
        const g = cores.slice(i, i + per);
        out.push(g.reduce((a, b) => a + b, 0) / g.length);
      }
      return out;
    }
    return this.cpuHist.slice();
  }

  // ── drawing ────────────────────────────────────────────────────────────

  // The equalizer: segments lit from the bottom, coloured by height, the
  // unlit ones faint; a band's peak cap a brighter segment above.
  equalizer(o, w) {
    const bands = this.bands();
    const n = Math.max(1, bands.length || HISTORY_BANDS);
    const gap = n > 24 ? 2 : 3;
    const bw = Math.max(4, Math.floor((w - gap * (n - 1)) / n));
    const segH = 5;
    const segGap = 2;
    const colorAt = (s) => (s >= HOT_AT ? o.fix : s >= WARN_AT ? o.work : o.review);
    return h_flex().flex_none().items_end().gap(gap).h(SEGMENTS * (segH + segGap)).children(
      Array.from({ length: n }, (_, b) => {
        const v = bands[b];
        const lit = v == null ? 0 : Math.round((Math.max(0, Math.min(100, v)) / 100) * SEGMENTS);
        const peak = this.peaks[b]?.at ?? 0;
        return v_flex().flex_none().w(bw).gap(segGap).children(
          Array.from({ length: SEGMENTS }, (_, k) => {
            const s = SEGMENTS - 1 - k; // top first
            const on = s < lit;
            // The peak: where it got to a moment ago, a faded segment that
            // holds, then falls back to the band.
            const cap = !on && peak > 0 && s === peak - 1;
            return div().w_full().h(segH).rounded(1.5).bg(on ? colorAt(s) : cap ? tint(colorAt(s), 0.4) : tint(o.line, 0.45));
          }),
        );
      }),
    );
  }

  // A horizontal meter: segments lit left to right, by the reading's level.
  // Every meter the same: as many segments, each as wide, so rows line up.
  meter(o, pct, { color = null } = {}) {
    const lit = Math.round((Math.max(0, Math.min(100, pct ?? 0)) / 100) * METER_SEGMENTS);
    const at = (i) => color ?? (i >= METER_SEGMENTS * 0.9 ? o.fix : i >= METER_SEGMENTS * 0.8 ? o.work : o.review);
    return h_flex().flex_none().gap(2).h(10).children(
      Array.from({ length: METER_SEGMENTS }, (_, i) => div().flex_none().w(METER_SEG_W).h_full().rounded(1.5).bg(i < lit ? at(i) : tint(o.line, 0.45))),
    );
  }

  // A row: what it is, its meter, its reading, and a dim note.
  row(o, label, meter, value, sub) {
    return h_flex().flex_none().gap(10).items_center().h(22)
      .child(div().flex_none().w(70).truncate().text_size(11.5).text_color(o.dim).child(label))
      .child(meter)
      .child(div().flex_none().w(72).text_size(11.5).font_family(MONO).text_color(o.fg).child(value))
      .when(Boolean(sub), (el) => el.child(div().flex_1().min_w(0).truncate().text_size(11).text_color(o.dim).child(sub)));
  }

  section(o, title, ic) {
    return h_flex().flex_none().gap(6).items_center().pt(6)
      .child(icon(ic, o.dim, 12))
      .child(div().text_size(10.5).font_semibold().text_color(o.dim).child(title.toUpperCase()))
      .child(div().flex_1().h(1).bg(o.line));
  }

  // The history strip: the sampler's last minutes of CPU, a thin column a
  // reading, coloured by level.
  strip(o, w) {
    const hs = (this.reading?.history ?? []).slice(-Math.floor(w / 4));
    return h_flex().flex_none().h(28).items_end().gap(1).children(hs.map((p) => {
      const v = Math.max(0, Math.min(100, p.c));
      return div().flex_none().w(3).h(Math.max(1, Math.round(0.28 * v))).rounded(1)
        .bg(v >= 90 ? o.fix : v >= 80 ? o.work : tint(o.review, 0.75));
    }));
  }

  // The title bar, what you drag the window by: the name, and the machine
  // shown as a dropdown: a click lists them all, a pick shows it.
  topBar(t, o) {
    const stop = (el) => el.on_mouse_down("left", (_e, cx) => cx.stop_propagation());
    const list = this.machineList();
    const cur = list.find((m) => m.key === this.machine) ?? list[0];
    const trigger = stop(h_flex().id("machine").flex_none().h(24).pl(8).pr(6).gap(6).items_center().rounded(6).cursor_pointer()
      .border_1().border_color(this.picking ? o.accent : o.line).when(Boolean(this.picking), (el) => el.bg(o.raised))
      .hover((s) => s.bg(o.raised))
      .on_click((_e, cx) => {
        this.picking = !this.picking;
        cx.notify();
      }))
      .child(icon(cur?.remote ? "server" : "laptop", o.muted, 12))
      .child(div().font_family(MONO).text_size(11.5).text_color(o.fg).child(cur?.label ?? "…"))
      .child(icon("chevron", o.dim, 11));
    const options = v_flex().w(220).p(4).gap(1).rounded(8).border_1().border_color(o.line).bg(o.surface).shadow_md()
      .on_mouse_down_out((_e, cx) => {
        this.picking = false;
        cx.notify();
      })
      .children(list.map((m) => {
        const on = m.key === this.machine;
        return stop(h_flex().id(`m-${m.key ?? "here"}`).h(28).px(8).gap(8).items_center().rounded(5).cursor_pointer()
          .when(on, (el) => el.bg(tint(o.accent, 0.16)))
          .when(!on, (el) => el.hover((s) => s.bg(o.raised)))
          .on_click((_e, cx) => {
            this.picking = false;
            this.pick(m.key, cx);
            cx.notify();
          }))
          .child(icon(m.remote ? "server" : "laptop", on ? o.fg : o.dim, 12))
          .child(div().flex_1().min_w(0).truncate().font_family(MONO).text_size(11.5).text_color(m.down ? o.dim : o.fg).child(m.label))
          .when(Boolean(m.down), (el) => el.child(div().text_size(10.5).text_color(o.dim).child("offline")))
          .when(on, (el) => el.child(icon("check", o.accent, 12)));
      }));
    return h_flex().flex_none().h(38).px(12).gap(8).items_center().border_b_1().border_color(o.line).bg(tint(o.sunk, 0.6))
      .child(icon("pulse", o.accent, 14))
      .child(div().flex_none().text_size(12.5).font_semibold().text_color(o.fg).child("Vitals"))
      .child(Popup.new("machines", trigger).anchor("top_left").when(Boolean(this.picking), (el) => el.content(options)))
      .child(div().flex_1())
      .when(this.demo, (el) => el.child(div().flex_none().text_size(11).text_color(o.dim).child("demo")))
      // Compact or normal: a click switches, and it's kept.
      .child(stop(h_flex().id("size").flex_none().h(22).px(6).gap(4).items_center().rounded(5).cursor_pointer()
        .hover((s) => s.bg(t.hover)).on_click((_e, cx) => {
          this.compact = !this.compact;
          localStorage.setItem("compact", this.compact ? "1" : "0");
          cx.notify();
        }))
        .child(icon(this.compact ? "expand" : "collapse", o.muted, 12))
        .child(div().text_size(11).text_color(o.muted).child(this.compact ? "More" : "Less")))
      .child(stop(h_flex().id("close").flex_none().w(22).h(22).items_center().justify_center().rounded(5).cursor_pointer()
        .hover((s) => s.bg(t.hover)).on_click(() => this.close()))
        .child(icon("cross", o.muted, 12)));
  }

  render() {
    const t = theme();
    const o = tones(t);
    const r = this.reading;
    const w = WINDOW_W - 32;
    const mem = r ? bytesOf(r.memUsed, r.memTotal) : "";
    const netMax = 125e6; // a gigabit, full
    const netPct = (b) => (b > 0 ? Math.min(100, (Math.log10(b + 1) / Math.log10(netMax)) * 100) : 0);
    const levelCol = levelColor(o, r?.level);
    const cpuSub = r && r.os === "linux" ? `user ${Math.round(r.user)} · sys ${Math.round(r.system)}${r.iowait >= 1 ? ` · io ${Math.round(r.iowait)}` : ""}` : "";

    const c = this.compact;
    const used = (r?.disks ?? []).reduce((a, d) => a + d.used, 0);
    const size = (r?.disks ?? []).reduce((a, d) => a + d.size, 0);
    return v_flex()
      .id("vitals")
      .w(WINDOW_W)
      .when(!c, (el) => el.h(NORMAL_H))
      .bg(t.popover)
      .text_size(12.5)
      .on_size_change((size, cx) => {
        if (Math.abs(size.width - (this.width ?? 0)) > 8) {
          this.width = size.width;
          cx.notify();
        }
      })
      .child(this.topBar(t, o))
      // Everything under the top bar scrolls, so a short window still reaches
      // the disks.
      .child(
        v_flex().id("vitals-body").when(!c, (el) => el.flex_1().min_h(0).overflow_y_scrollbar()).px(16).py(12).gap(8)
          // What it is, and how it's doing in a word.
          .child(h_flex().flex_none().gap(8).items_center()
            .child(div().text_size(15).font_semibold().text_color(o.fg).child(r?.host ?? (this.err ? "Not answering" : "Reading…")))
            .when(Boolean(r), (el) => el.child(div().min_w(0).truncate().text_size(11.5).text_color(o.dim)
              .child(`${r.release || r.os} · ${r.cores} cores · up ${r.uptime}`)))
            .child(div().flex_1())
            .when(Boolean(r), (el) => el.child(h_flex().flex_none().h(20).px(7).gap(5).items_center().rounded(5).bg(tint(levelCol, 0.15))
              .child(div().w(6).h(6).rounded_full().bg(levelCol))
              .child(div().text_size(10.5).font_semibold().text_color(levelCol).child(r.level === "ok" ? "OK" : `${r.level.toUpperCase()} ${r.why}`)))))
          .when(Boolean(this.err), (el) => el.child(div().text_size(12).text_color(o.fix).child(this.err)))
          // The CPU, as an equalizer.
          .child(this.section(o, r?.perCore?.length ? `CPU · ${r.perCore.length} cores` : `CPU · the last ${(HISTORY_BANDS * EVERY_MS) / 1000} seconds`, "pulse"))
          .child(h_flex().flex_none().gap(14).items_end()
            .child(this.equalizer(o, w - 92))
            .child(v_flex().flex_none().w(78).gap(2).items_end()
              .child(div().text_size(30).font_semibold().font_family(MONO).text_color(r ? levelColor(o, r.cpu >= 90 ? "hot" : r.cpu >= 80 ? "warn" : "ok") : o.dim)
                .child(r && !r.warming ? `${Math.round(r.cpu)}` : "–"))
              .child(div().text_size(11).text_color(o.dim).child("% busy"))
              .child(div().text_size(10.5).text_color(o.dim).child(cpuSub))))
          // Compact: memory, and every disk together.
          .when(c, (el) => el
            .child(div().flex_none().h(6))
            .child(this.row(o, "Memory", this.meter(o, r?.mem), r ? `${Math.round(r.mem)}%` : "", mem))
            .when(size > 0, (el) => el.child(this.row(o, "Disk", this.meter(o, (100 * used) / size), `${Math.round((100 * used) / size)}%`,
              bytesOf(used, size)))))
          .when(!c, (el) => this.full(el, o, r, w, mem, netPct)),
      );
  }

  // Normal: the history strip, memory and load, network and each disk.
  full(el, o, r, w, mem, netPct) {
    return el
          .when(Boolean(r?.history?.length), (el) => el.child(this.strip(o, w)))
          // Memory, load, network: meters.
          .child(this.section(o, "Memory and load", "spark"))
          .child(this.row(o, "Memory", this.meter(o, r?.mem), r ? `${Math.round(r.mem)}%` : "", mem))
          .when(Boolean(r?.swapTotal), (el) => el.child(this.row(o, "Swap", this.meter(o, r.swap), `${Math.round(r.swap)}%`, bytes(r.swapTotal))))
          .child(this.row(o, "Load", this.meter(o, Math.min(100, (r?.loadPerCore ?? 0) * 100)), r ? `${r.load[0].toFixed(2)}` : "", r ? `${r.loadPerCore.toFixed(2)} a core` : ""))
          .child(this.section(o, "Network", "net"))
          .child(this.row(o, "In", this.meter(o, netPct(r?.netRx), { color: o.accent }), r ? bytes(r.netRx, true) : "", ""))
          .child(this.row(o, "Out", this.meter(o, netPct(r?.netTx), { color: o.merged }), r ? bytes(r.netTx, true) : "", ""))
          // Disks.
          .when(Boolean(r?.disks?.length), (el) => el
            .child(this.section(o, "Disks", "disk"))
            .children(r.disks.slice(0, 6).map((d) => this.row(o, diskName(d.mount),
              this.meter(o, d.pct), `${Math.round(d.pct)}%`, `of ${bytes(d.size)}`))));
  }
}
