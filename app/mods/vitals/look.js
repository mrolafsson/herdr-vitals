// How the Vitals mod draws a machine: its colours, its icons and its meters,
// in the app's theme. The same look as Recap's, GitHub's and Linear's:
// surfaces neutral, colour only on what a reading means, green while it's
// calm, yellow when it wants a look, red when it wants you now.

import { div, svg } from "gpui-kit";
import { h_flex } from "gpui-base";

// The mod's own icons, from the app's home folder, tinted with the text colour.
const ICONS = "mods/vitals/icons";

// The faces: the app's own for words, mono for what you'd type (branches,
// machines, keys). JetBrains Mono ships with Herdr.
export const MONO = "JetBrains Mono";

// A colour at an alpha: the theme's colours are #rrggbb.
export const tint = (color, alpha) => (/^#[0-9a-f]{6}$/i.test(color ?? "") ? color + Math.round(alpha * 255).toString(16).padStart(2, "0") : color);

// Colour roles, from the theme's own names for them, so every theme reads:
// ready (green), waiting (yellow), a draft, merged, something broken, and the
// accent for links, focus and what's asked of you.
export function tones(t) {
  return {
    review: t.ansi2,
    work: t.working ?? t.ansi3,
    draft: t.ansi8 ?? t.dim,
    merged: t.ansi5,
    fix: t.ansi1,
    need: t.blocked ?? t.ansi1,
    accent: t.accent ?? t.ansi4,
    fg: t.foreground,
    muted: t.muted,
    dim: t.dim,
    surface: t.popover,
    raised: t.raised ?? t.hover,
    sunk: t.input ?? t.background,
    line: t.border,
  };
}

// A level's colour: ok, warn, hot.
export const levelColor = (o, l) => ({ hot: o.fix, warn: o.work })[l] ?? o.review;

// Bytes, briefly: 812 B, 14 KB, 3.2 MB, 1.4 GB.
export function bytes(n, perSec = false) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n ?? 0;
  while (v >= 1000 && i < u.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${u[i]}${perSec ? "/s" : ""}`;
}

// Two amounts, briefly: "207 of 275 GB" when they share a unit, else
// "812 MB of 1.2 GB".
export function bytesOf(a, b) {
  const [x, y] = [bytes(a), bytes(b)];
  const [xu, yu] = [x.split(" ")[1], y.split(" ")[1]];
  return xu === yu ? `${x.split(" ")[0]} of ${y}` : `${x} of ${y}`;
}

// When something resets, from now: "in 3h 12m", "in 2d".
export function inTime(at) {
  const ms = Date.parse(at) - Date.now();
  if (!(ms > 0)) return "now";
  const m = Math.round(ms / 60000);
  if (m < 60) return `in ${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `in ${h}h ${m % 60}m`;
  return `in ${Math.round(h / 24)}d`;
}

// One icon, `size` px square, in `color`.
export function icon(name, color, size = 16) {
  return svg(`${ICONS}/${name}.svg`).flex_none().w(size).h(size).text_color(color);
}

export function duration(ms) {
  const m = Math.floor(ms / 60000);
  if (m < 1) return "<1m";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  return h < 24 ? `${h}h` : `${Math.floor(h / 24)}d`;
}

// A chip: a small rounded label, neutral unless it carries a state's colour
// (then tinted with it). `parts` are elements or strings; `click` makes it a
// link.
export function chip(o, id, parts, { color = null, mono = false, click = null } = {}) {
  return h_flex()
    .id(id)
    .flex_none()
    .h(22)
    .px(8)
    .gap(5)
    .items_center()
    .rounded(6)
    .bg(color ? tint(color, 0.12) : o.sunk)
    .text_size(mono ? 11 : 11.5)
    .text_color(color ?? o.muted)
    .when(!mono, (el) => el.font_medium())
    .when(mono, (el) => el.font_family(MONO))
    .when(Boolean(click), (el) =>
      el.cursor_pointer()
        .hover((s) => s.bg(color ? tint(color, 0.22) : o.raised))
        .on_click((_e, cx) => {
          cx.stop_propagation();
          click(cx);
        }))
    .children(parts.filter((p) => p !== null && p !== undefined && p !== "").map((p) => (typeof p === "string" ? div().child(p) : p)));
}

// Words in a row, each in its colour: [text, color, onClick?], joined by
// dim dots between groups.
export function words(o, groups, idBase) {
  const out = [];
  groups.filter((g) => g.length).forEach((g, i) => {
    if (i > 0) out.push(div().text_color(o.dim).child("·"));
    g.forEach(([text, color, click, under], j) => {
      const w = div().text_color(color).child(text);
      if (!click) return out.push(w);
      out.push(
        div()
          .id(`${idBase}-${i}-${j}`)
          .text_color(color)
          .font_medium()
          .when(Boolean(under), (el) => el.underline())
          .cursor_pointer()
          .hover((s) => s.underline())
          .on_click((_e, cx) => {
            cx.stop_propagation();
            click(cx);
          })
          .child(text),
      );
    });
  });
  return h_flex().gap(8).items_center().flex_wrap().children(out);
}

// What's drawn on a keycap: the keys with a symbol of their own as icons
// (enter as an arrow down and left, ⌃, ⌘, tab, the arrows), letters
// capitalised as on the key; "^g" or "ctrl-g" is ⌃G, "cmd-g" ⌘G.
const KEY_ICONS = {
  enter: "key-enter", "⏎": "key-enter", "↵": "key-enter",
  tab: "key-tab", "⇥": "key-tab",
  "↑": "key-up", up: "key-up", "↓": "key-down", down: "key-down",
  "←": "key-left", left: "key-left", "→": "key-right", right: "key-right",
  ctrl: "key-ctrl", "^": "key-ctrl", "⌃": "key-ctrl",
  cmd: "key-cmd", "⌘": "key-cmd",
};
function keyParts(k) {
  const m = /^(\^|ctrl[-+]|cmd[-+])(.+)$/i.exec(k);
  if (m) return [m[1][0] === "^" ? "ctrl" : m[1].slice(0, -1).toLowerCase(), ...keyParts(m[2])];
  return [KEY_ICONS[k.toLowerCase()] ? k.toLowerCase() : k.length === 1 ? k.toUpperCase() : k];
}

// One keycap.
export function cap(o, k) {
  return h_flex()
    .flex_none()
    .min_w(20)
    .h(20)
    .px(5)
    .gap(1)
    .items_center()
    .justify_center()
    .rounded(5)
    .border_1()
    .border_b_2()
    .border_color(o.line)
    .bg(o.surface)
    .font_family(MONO)
    .text_size(11)
    .text_color(o.muted)
    .children(keyParts(k).map((p) => (KEY_ICONS[p] ? icon(KEY_ICONS[p], o.muted, 11) : div().child(p))));
}

// A key in the footer: its keycaps and what it does. Clickable.
export function key(o, keys, label, click, id) {
  return h_flex()
    .id(id)
    .flex_none()
    .gap(6)
    .items_center()
    .cursor_pointer()
    .text_size(12)
    .text_color(o.dim)
    .hover((s) => s.text_color(o.fg))
    .on_click((_e, cx) => click(cx))
    .children(keys.map((k) => cap(o, k)))
    .child(div().child(label));
}

// A tab's count, in a pill wide enough for it from the start: "–" until
// it's known, so the tabs don't move when it comes.
export function countPill(o, n, on) {
  return div()
    .flex_none()
    .min_w(24)
    .h(18)
    .px(6)
    .flex()
    .items_center()
    .justify_center()
    .rounded(9)
    .bg(on ? o.sunk : tint(o.line, 0.6))
    .font_family(MONO)
    .text_size(11)
    .font_medium()
    .text_color(on ? o.fg : o.dim)
    .child(n == null || n === "" ? "–" : String(n));
}
