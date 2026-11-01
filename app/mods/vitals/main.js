// How a machine is doing, live, in a window that stays beside your work: its
// CPU as a graphic equalizer (a band a core, lit segment by segment, green,
// then yellow, then red, with a peak that holds and falls), memory, load and
// network as meters, its disks and when each will be full, and how near your
// AI accounts are to their limits. The machine the agent you're in runs on,
// first; the others a click away in the top bar.
//
// The readings are herdr-vitals' (`app`), the TUI plugin's own code, run on
// the machine shown. The mod fetches the herdr-vitals release it needs onto
// each machine it asks, the first time.

import { View, div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { agents, machines, spaces, exec, theme, props, closeOverlay, nextCommand } from "herdr";
import { tones, tint, levelColor, bytes, inTime, icon, MONO } from "./look.js";

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

// How often a machine is read while it's shown, and its AI accounts.
const EVERY_MS = 1000;
const AI_EVERY_MS = 60000;

// The equalizer: as many bands as fit (a core each; more cores than bands
// share one), each this many segments tall; where yellow and red start.
const MAX_BANDS = 32;
const SEGMENTS = 16;
const WARN_AT = 10;
const HOT_AT = 14;
// A peak holds this many readings, then falls a segment a reading.
const PEAK_HOLD = 3;

const machineOf = (address) => (address?.includes("/") ? address.split("/")[0] : null);

export default class Vitals extends View {
  init(_props, cx) {
    const p = props() ?? {};
    this.demo = Boolean(p.demo);
    this.reading = null;
    this.err = "";
    this.peaks = [];
    this.cpuHist = []; // this machine's CPU, a reading at a time, for a Mac's bands
    this.ai = null;
    this.demoMachines = [];
    // The machine the agent you're in runs on; else the shown space's.
    const focused = agents().find((a) => a.focused);
    const space = spaces().find((s) => s.active);
    this.machine = focused ? machineOf(focused.address) : space?.machine ?? null;

    cx.spawn(async (cx) => {
      if (this.demo) await this.startDemo(cx);
      this.read(cx);
      this.readAI(cx);
    });
    cx.timer.every(EVERY_MS, (cx) => this.read(cx));
    cx.timer.every(AI_EVERY_MS, (cx) => this.readAI(cx));

    // Key bindings and `herdr-app do`: vitals.close, vitals.demo,
    // vitals.next, vitals.prev.
    cx.spawn(async (cx) => {
      for (;;) {
        const c = await nextCommand();
        if (c === "close") this.close();
        else if (c === "demo") {
          this.demo = !this.demo;
          this.reset();
          if (this.demo) await this.startDemo(cx);
          else this.machine = null;
          this.read(cx);
          this.readAI(cx);
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
    this.ai = null;
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
    this.readAI(cx);
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

  readAI(cx) {
    const machine = this.machine;
    cx.spawn(async (cx) => {
      try {
        const r = await ask(this.demo ? { op: "ai", demo: true } : { op: "ai" }, this.demo ? null : machine);
        if (machine === this.machine) this.ai = r.accounts;
      } catch {
        if (machine === this.machine) this.ai = [];
      }
      cx.notify();
    });
  }

  // A reading in: the bands' levels, and each band's peak, held a moment
  // then falling.
  take(r) {
    if (!this.cpuHist.length && r.history?.length) this.cpuHist = r.history.map((p) => p.c);
    if (!r.warming) this.cpuHist.push(r.cpu);
    if (this.cpuHist.length > MAX_BANDS) this.cpuHist = this.cpuHist.slice(-MAX_BANDS);
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
    const n = Math.max(1, bands.length || 16);
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
            const cap = !on && peak > 0 && s === peak - 1;
            return div().w_full().h(segH).rounded(1.5)
              .bg(on ? colorAt(s) : cap ? tint(colorAt(s), 0.85) : tint(o.line, 0.45));
          }),
        );
      }),
    );
  }

  // A horizontal meter: segments lit left to right, by the reading's level.
  meter(o, pct, { segments = 28, color = null } = {}) {
    const lit = Math.round((Math.max(0, Math.min(100, pct ?? 0)) / 100) * segments);
    const at = (i) => color ?? (i >= segments * 0.9 ? o.fix : i >= segments * 0.8 ? o.work : o.review);
    return h_flex().flex_1().min_w(0).gap(2).h(10).children(
      Array.from({ length: segments }, (_, i) => div().flex_1().h_full().rounded(1.5).bg(i < lit ? at(i) : tint(o.line, 0.45))),
    );
  }

  row(o, label, meter, value, sub) {
    return h_flex().flex_none().gap(10).items_center().h(22)
      .child(div().flex_none().w(62).text_size(11.5).text_color(o.dim).child(label))
      .child(meter)
      .child(div().flex_none().w(120).text_size(11.5).font_family(MONO).text_color(o.fg).child(value))
      .when(Boolean(sub), (el) => el.child(div().flex_none().text_size(11).text_color(o.dim).child(sub)));
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

  // The title bar (what you drag the window by) and under it the machines,
  // as many rows as they take: every one in sight, however many.
  topBar(t, o) {
    const stop = (el) => el.on_mouse_down("left", (_e, cx) => cx.stop_propagation());
    return v_flex().flex_none().border_b_1().border_color(o.line).bg(tint(o.sunk, 0.6))
      .child(h_flex().h(32).px(12).gap(8).items_center()
        .child(icon("pulse", o.accent, 14))
        .child(div().flex_none().text_size(12.5).font_semibold().text_color(o.fg).child("Vitals"))
        .child(div().flex_1())
        .when(this.demo, (el) => el.child(div().flex_none().text_size(11).text_color(o.dim).child("demo · made-up machines")))
        .child(stop(h_flex().id("close").flex_none().w(22).h(22).items_center().justify_center().rounded(5).cursor_pointer()
          .hover((s) => s.bg(t.hover)).on_click(() => this.close()))
          .child(icon("cross", o.muted, 12))))
      .child(h_flex().px(10).pb(6).gap(4).flex_wrap().items_center().children(this.machineList().map((m) => {
        const on = m.key === this.machine;
        return stop(h_flex().id(`m-${m.key ?? "here"}`).flex_none().h(22).px(7).gap(5).items_center().rounded(5).cursor_pointer()
          .when(on, (el) => el.bg(o.raised).shadow_sm())
          .when(!on, (el) => el.hover((s) => s.bg(t.hover)))
          .on_click((_e, cx) => this.pick(m.key, cx)))
          .child(icon(m.remote ? "server" : "laptop", on ? o.fg : o.dim, 11))
          .child(div().font_family(MONO).text_size(11).text_color(m.down ? o.dim : on ? o.fg : o.muted).child(m.label));
      })));
  }

  render() {
    const t = theme();
    const o = tones(t);
    const r = this.reading;
    const w = (this.width ?? 460) - 32;
    const mem = r ? `${bytes(r.memUsed)} / ${bytes(r.memTotal)}` : "";
    const netMax = 125e6; // a gigabit, full
    const netPct = (b) => (b > 0 ? Math.min(100, (Math.log10(b + 1) / Math.log10(netMax)) * 100) : 0);
    const levelCol = levelColor(o, r?.level);
    const cpuSub = r && r.os === "linux" ? `user ${Math.round(r.user)} · sys ${Math.round(r.system)}${r.iowait >= 1 ? ` · io ${Math.round(r.iowait)}` : ""}` : "";

    return v_flex()
      .id("vitals")
      .size_full()
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
      // the disks and the AI limits.
      .child(
        v_flex().id("vitals-body").flex_1().min_h(0).overflow_y_scrollbar().px(16).py(12).gap(8)
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
          .child(this.section(o, r?.perCore?.length ? `CPU · ${r.perCore.length} cores` : "CPU · the last half minute", "pulse"))
          .child(h_flex().flex_none().gap(14).items_end()
            .child(this.equalizer(o, w - 92))
            .child(v_flex().flex_none().w(78).gap(2).items_end()
              .child(div().text_size(30).font_semibold().font_family(MONO).text_color(r ? levelColor(o, r.cpu >= 90 ? "hot" : r.cpu >= 80 ? "warn" : "ok") : o.dim)
                .child(r && !r.warming ? `${Math.round(r.cpu)}` : "–"))
              .child(div().text_size(11).text_color(o.dim).child("% busy"))
              .child(div().text_size(10.5).text_color(o.dim).child(cpuSub))))
          .when(Boolean(r?.history?.length), (el) => el.child(this.strip(o, w)))
          // Memory, load, network: meters.
          .child(this.section(o, "Memory and load", "spark"))
          .child(this.row(o, "Memory", this.meter(o, r?.mem), r ? `${Math.round(r.mem)}%` : "", mem))
          .when(Boolean(r?.swapTotal), (el) => el.child(this.row(o, "Swap", this.meter(o, r.swap), `${Math.round(r.swap)}%`, bytes(r.swapTotal))))
          .child(this.row(o, "Load", this.meter(o, Math.min(100, (r?.loadPerCore ?? 0) * 100)), r ? `${r.load[0].toFixed(2)}` : "", r ? `${r.loadPerCore.toFixed(2)} a core` : ""))
          .child(this.section(o, "Network", "net"))
          .child(this.row(o, "In", this.meter(o, netPct(r?.netRx), { color: o.accent }), r ? bytes(r.netRx, true) : "", ""))
          .child(this.row(o, "Out", this.meter(o, netPct(r?.netTx), { color: o.merged }), r ? bytes(r.netTx, true) : "", ""))
          // Disks, and when each will be full.
          .when(Boolean(r?.disks?.length), (el) => el
            .child(this.section(o, "Disks", "disk"))
            .children(r.disks.slice(0, 6).map((d) => this.row(o, d.mount.length > 9 ? "…" + d.mount.slice(-8) : d.mount,
              this.meter(o, d.pct), `${Math.round(d.pct)}% of ${bytes(d.size)}`,
              d.fill ? div().text_color(levelColor(o, d.level)).font_semibold().child(`full in ${d.fill}`) : null))))
          // AI accounts, and how near their limits.
          .when(Boolean(this.ai?.length), (el) => el
            .child(this.section(o, "AI limits", "spark"))
            .children(this.ai.slice(0, 4).flatMap((a) => [
              h_flex().flex_none().gap(6).items_center().pt(2)
                .child(div().text_size(12).font_semibold().text_color(o.fg).child(a.name || a.key))
                .child(div().text_size(11).text_color(o.dim).child([a.kind, a.plan].filter(Boolean).join(" · ")))
                .when(Boolean(a.err), (el) => el.child(div().min_w(0).truncate().text_size(11).text_color(o.dim).child(a.err))),
              ...a.limits.map((l) => this.row(o, l.label, this.meter(o, l.percent), `${Math.round(l.percent)}%`, `resets ${inTime(l.resets_at)}`)),
            ]))),
      );
  }
}
