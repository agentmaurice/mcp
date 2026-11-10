// Fragments partages : graphiques deterministes en Typst pur (contrat `chart/v1`,
// README § Graphiques). Aucun paquet, aucun plugin, aucune image : du vectoriel.
// Couleurs : uniquement `brand.colors` (et le blanc, papier et liseres).
//
// Chaque fragment recoit ses donnees sous trois formes equivalentes :
//   - un dictionnaire Typst ;
//   - une chaine JSON ;
//   - un contenu `[...]` portant cette chaine (cas d'un gabarit : la fonction `json` appliquee a la donnee, entre
//     crochets ; la valeur passe par reference, jamais dans le code).
// Toute entree mal formee ou hors bornes echoue par une erreur de compilation dont le message commence par `chart/v1:`.
// Un JSON syntaxiquement invalide echoue avec l'erreur de `json()` de Typst : le
// moteur ne transmet que du JSON valide, valide aussi contre `chart.schema.json`.
#import "/brand.typ": brand

// Bornes du contrat. `chart.schema.json` porte les memes valeurs (test du contrat).
#let limits = (
  max-categories: 24,
  max-series: 5,
  max-parts: 10,
  max-tasks: 25,
  max-nodes: 30,
  max-steps: 12,
  max-phases: 5,
  max-weeks: 104,
  max-org-leaves: 8,
  max-org-depth: 6,
  max-org-height: 240,
  max-process-height: 180,
  max-label: 60,
  max-id: 32,
  max-abs-value: 1000000000000,
  min-abs-value: 0.01,
)

#let c = brand.colors
// Palette de series : les cinq couleurs de la charte qui contrastent avec le papier.
#let series-colors = (c.primary, c.accent, c.secondary, c.muted, c.text)

// ---------------------------------------------------------------- validation

#let fail(msg) = panic("chart/v1: " + msg)
#let ensure(ok, msg) = if not ok { fail(msg) }
#let is-num(v) = type(v) == int or type(v) == float

#let plain(x) = {
  if type(x) == str { x } else if type(x) == content {
    if x.has("text") { x.text } else if x.has("children") { x.children.map(plain).join() } else if x.func() == [ ].func() { " " } else {
      fail("expected the JSON text of the data (use `| json` inside [ ])")
    }
  } else { fail("expected a dictionary, a JSON string or its content, got " + str(type(x))) }
}

#let load(x, kind, who) = {
  let d = if type(x) == dictionary { x } else { json(bytes(plain(x))) }
  ensure(type(d) == dictionary, who + ": the data must be an object")
  ensure(d.at("type", default: none) == kind, who + ": type must be \"" + kind + "\"")
  d
}

#let check-list(v, what, lo, hi) = {
  ensure(type(v) == array, what + " must be an array")
  ensure(v.len() >= lo and v.len() <= hi, what + " must have between " + str(lo) + " and " + str(hi) + " items, got " + str(v.len()))
}
#let check-label(v, what) = {
  ensure(type(v) == str and v.len() > 0, what + " must be a non-empty string")
  ensure(v.codepoints().len() <= limits.max-label, what + " is longer than " + str(limits.max-label) + " characters")
  // a label is one line of text: no line break, no other control character
  ensure(not v.contains(regex("[\\x00-\\x1f\\x7f]")), what + " must not contain a line break or another control character")
}
// A number is 0 or at least `min-abs-value` in absolute value (the displayed precision),
// at most `max-abs-value`: no value can underflow the axis computation.
#let check-num(v, what) = {
  ensure(is-num(v), what + " must be a number")
  ensure(calc.abs(v) <= limits.max-abs-value, what + " is beyond +-" + str(limits.max-abs-value))
  ensure(v == 0 or calc.abs(v) >= limits.min-abs-value, what + " must be 0 or at least " + str(limits.min-abs-value) + " in absolute value")
}
// An integer is an int, or a float without fractional part (JSON Schema reads 1.0 as an
// integer; Typst decodes it as a float). Returns the value as an int.
#let check-int(v, what, lo, hi) = {
  ensure(is-num(v) and (type(v) == int or v == calc.trunc(v)), what + " must be an integer")
  ensure(v >= lo and v <= hi, what + " must be between " + str(lo) + " and " + str(hi) + ", got " + str(v))
  int(v)
}
#let get(d, key, who) = {
  ensure(key in d, who + ": `" + key + "` is missing")
  d.at(key)
}
#let check-series(s, n, who) = {
  ensure(type(s) == dictionary, who + ": a series must be an object")
  check-label(get(s, "name", who + " series"), who + ": series name")
  let vals = get(s, "values", who + " series")
  ensure(type(vals) == array and vals.len() == n, who + ": series `" + s.name + "` must have exactly " + str(n) + " values (one per category)")
  for v in vals { check-num(v, who + ": a value of series `" + s.name + "`") }
}

// A word wider than `avail` is cut by zero-width spaces between its characters, so that it can wrap.
// Needs a `context`.
#let cut-words(txt, size, weight, avail) = txt.split(" ").map(word => {
  if measure(text(size: size, weight: weight, word)).width > avail { word.clusters().join(sym.zws) } else { word }
}).join(" ")

// ---------------------------------------------------------------- nombres (fr)

// 1234567.5 -> « 1 234 567,5 » (espace fine insecable U+202F, virgule decimale,
// vrai signe moins U+2212). Arrondi a `digits` decimales, zeros finaux retires.
#let thin = "\u{202F}"
#let fmt-num(v, digits: 2) = {
  let r = calc.round(v, digits: digits)
  if r == 0 { "0" } else {
    let parts = str(calc.abs(r)).split(".")
    let ip = parts.at(0)
    let n = ip.len()
    let grouped = ip.clusters().enumerate().map(((i, ch)) => if i > 0 and calc.rem(n - i, 3) == 0 { thin + ch } else { ch }).join()
    (if r < 0 { "\u{2212}" } else { "" }) + grouped + (if parts.len() > 1 { "," + parts.at(1) } else { "" })
  }
}
#let fmt-pct(v) = fmt-num(v, digits: 1) + thin + "%"

// ---------------------------------------------------------------- socle

#let at(x, y, body) = place(top + left, dx: x, dy: y, body)
#let seg(x1, y1, x2, y2, stroke) = at(x1, y1, line(start: (0pt, 0pt), end: (x2 - x1, y2 - y1), stroke: stroke))
#let tbox(x, y, w, h, body, size: 7pt, fill: c.text, al: center + horizon, weight: "regular") = at(x, y, box(width: w, height: h, align(al, text(size: size, fill: fill, weight: weight, body))))

// Pas « joli » (1, 2, 5 x 10^k) pour l'axe.
#let nice-step(v) = {
  let p = calc.pow(10.0, calc.floor(calc.log(v, base: 10)))
  let m = v / p
  let s = if m <= 1 { 1 } else if m <= 2 { 2 } else if m <= 5 { 5 } else { 10 }
  s * p
}

// Axe vertical qui contient toujours zero : (lo, hi, step, decimals).
#let axis-scale(vmin, vmax) = {
  let lo = calc.min(vmin, 0)
  let hi = calc.max(vmax, 0)
  if hi == lo { hi = 1 }
  // finite floor: the span is never below the displayed precision, so log(0) cannot happen
  let step = calc.max(nice-step(calc.max(hi - lo, limits.min-abs-value) / 5), 0.001)
  let decimals = calc.min(6, calc.max(0, -calc.floor(calc.log(step, base: 10))))
  (lo: calc.floor(lo / step) * step, hi: calc.ceil(hi / step) * step, step: step, decimals: decimals)
}

#let y-axis(ml, mt, ph, w, sc) = {
  let n = calc.round((sc.hi - sc.lo) / sc.step)
  for i in range(int(n) + 1) {
    let v = sc.lo + i * sc.step
    let y = mt + ph * ((sc.hi - v) / (sc.hi - sc.lo))
    seg(ml, y, w, y, 0.3pt + c.muted)
    tbox(0pt, y - 2mm, ml - 2mm, 4mm, fmt-num(v, digits: sc.decimals), al: right + horizon, fill: c.muted, size: 7pt)
  }
}

#let legend(items, x0, y0, w, step: 56mm) = {
  let st = calc.min(step, (w - x0) / items.len())
  let x = x0
  for (i, it) in items.enumerate() {
    at(x, y0 + 0.8mm, rect(width: 3mm, height: 3mm, fill: series-colors.at(calc.rem(i, series-colors.len())), stroke: none))
    tbox(x + 4mm, y0, st - 5mm, 4.6mm, it, al: left + horizon, size: 8pt)
    x += st
  }
}

// ---------------------------------------------------------------- barres

// data : (type: "bar", categories: (..), series: ((name:, values: (..)), ..))
// Series groupees cote a cote, valeurs negatives sous l'axe zero.
#let bar-chart(data, w: 170mm, h: 85mm) = {
  let who = "bar-chart"
  let d = load(data, "bar", who)
  let cats = get(d, "categories", who)
  check-list(cats, who + ": categories", 1, limits.max-categories)
  for cat in cats { check-label(cat, who + ": a category") }
  let ser = get(d, "series", who)
  check-list(ser, who + ": series", 1, limits.max-series)
  for s in ser { check-series(s, cats.len(), who) }
  let ml = 12mm; let mt = 9mm; let mb = 7mm
  let pw = w - ml; let ph = h - mt - mb
  let all = ser.map(s => s.values).flatten()
  let sc = axis-scale(calc.min(..all), calc.max(..all))
  let span = sc.hi - sc.lo
  let ypos(v) = mt + ph * ((sc.hi - v) / span)
  let slot = pw / cats.len()
  let group = slot * 0.72
  let bw = group / ser.len()
  let labels = bw >= 9mm
  box(width: w, height: h, {
    legend(ser.map(s => s.name), ml, 0mm, w)
    y-axis(ml, mt, ph, w, sc)
    for (si, s) in ser.enumerate() {
      for (i, v) in s.values.enumerate() {
        let x = ml + slot * i + (slot - group) / 2 + bw * si
        let y0 = ypos(0)
        let y1 = ypos(v)
        let top = calc.min(y0, y1)
        at(x, top, rect(width: bw, height: calc.abs(y1 - y0), fill: series-colors.at(si), stroke: none))
        if labels {
          let ly = if v >= 0 { top - 3.8mm } else { top + calc.abs(y1 - y0) + 0.2mm }
          tbox(x - 2mm, ly, bw + 4mm, 3.6mm, fmt-num(v), size: 6.5pt)
        }
      }
    }
    for (i, cat) in cats.enumerate() {
      tbox(ml + slot * i, mt + ph + 1mm, slot, 4mm, cat, fill: c.text, size: if slot < 12mm { 6pt } else { 7pt })
    }
    seg(ml, ypos(0), w, ypos(0), 0.6pt + c.text)
  })
}

// ---------------------------------------------------------------- lignes

// data : (type: "line", categories: (..), series: ((name:, values: (..)), ..))
#let line-chart(data, w: 170mm, h: 85mm) = {
  let who = "line-chart"
  let d = load(data, "line", who)
  let cats = get(d, "categories", who)
  check-list(cats, who + ": categories", 1, limits.max-categories)
  for cat in cats { check-label(cat, who + ": a category") }
  let ser = get(d, "series", who)
  check-list(ser, who + ": series", 1, limits.max-series)
  for s in ser { check-series(s, cats.len(), who) }
  let ml = 12mm; let mt = 9mm; let mb = 7mm
  let pw = w - ml; let ph = h - mt - mb
  let all = ser.map(s => s.values).flatten()
  let sc = axis-scale(calc.min(..all), calc.max(..all))
  let span = sc.hi - sc.lo
  let ypos(v) = mt + ph * ((sc.hi - v) / span)
  let slot = pw / cats.len()
  box(width: w, height: h, {
    legend(ser.map(s => s.name), ml, 0mm, w)
    y-axis(ml, mt, ph, w, sc)
    for (i, cat) in cats.enumerate() {
      tbox(ml + slot * i, mt + ph + 1mm, slot, 4mm, cat, fill: c.text, size: if slot < 12mm { 6pt } else { 7pt })
    }
    seg(ml, ypos(0), w, ypos(0), 0.6pt + c.text)
    for (k, s) in ser.enumerate() {
      let col = series-colors.at(k)
      let pts = s.values.enumerate().map(((i, v)) => (ml + slot * (i + 0.5), ypos(v)))
      for i in range(pts.len() - 1) {
        seg(pts.at(i).at(0), pts.at(i).at(1), pts.at(i + 1).at(0), pts.at(i + 1).at(1), 1.6pt + col)
      }
      for p in pts {
        at(p.at(0) - 1.2mm, p.at(1) - 1.2mm, circle(radius: 1.2mm, fill: col, stroke: 0.5pt + white))
      }
    }
  })
}

// ---------------------------------------------------------------- camembert

// data : (type: "pie", parts: ((name:, value:), ..)). Les parts sont calculees
// depuis les valeurs (value / somme) ; le pourcentage affiche est donc derive.
#let pie-chart(data, w: 170mm, h: 90mm) = {
  let who = "pie-chart"
  let d = load(data, "pie", who)
  let parts = get(d, "parts", who)
  check-list(parts, who + ": parts", 1, limits.max-parts)
  for p in parts {
    ensure(type(p) == dictionary, who + ": a part must be an object")
    check-label(get(p, "name", who + " part"), who + ": part name")
    let v = get(p, "value", who + " part")
    check-num(v, who + ": value of part `" + p.name + "`")
    ensure(v >= 0, who + ": value of part `" + p.name + "` must not be negative")
  }
  let total = parts.map(p => p.value).sum()
  ensure(total > 0, who + ": the sum of the values must be greater than zero")
  // Une couleur par part ; au-dela de cinq parts la palette se repete, et la derniere
  // part ne prend jamais la couleur de ses deux voisines.
  let n = parts.len()
  let ncol = series-colors.len()
  let color-index(k) = {
    if n > 1 and k == n - 1 and n > ncol {
      let taken = (calc.rem(0, ncol), calc.rem(n - 2, ncol))
      range(ncol).find(j => j not in taken)
    } else { calc.rem(k, ncol) }
  }
  let r = 36mm; let cx = 42mm; let cy = h / 2
  box(width: w, height: h, {
    let a0 = -90deg
    for (k, it) in parts.enumerate() {
      let share = it.value / total
      let da = 360deg * share
      if it.value > 0 {
        let steps = calc.max(2, calc.ceil(da / 2deg))
        let pts = ((cx, cy),)
        for j in range(steps + 1) {
          let a = a0 + da * (j / steps)
          pts.push((cx + r * calc.cos(a), cy + r * calc.sin(a)))
        }
        at(0pt, 0pt, polygon(fill: series-colors.at(color-index(k)), stroke: 1pt + white, ..pts))
        let am = a0 + da / 2
        tbox(cx + r * 0.68 * calc.cos(am) - 7mm, cy + r * 0.68 * calc.sin(am) - 2.5mm, 14mm, 5mm,
          fmt-pct(share * 100), size: if share < 0.04 { 6pt } else { 8pt }, fill: white, weight: "bold")
      }
      a0 += da
    }
    for (k, it) in parts.enumerate() {
      let y = 22mm + k * 6.4mm
      at(95mm, y + 0.6mm, rect(width: 4mm, height: 4mm, fill: series-colors.at(color-index(k)), stroke: none))
      tbox(102mm, y, w - 102mm, 5.2mm, it.name + " (" + fmt-pct(it.value / total * 100) + ")", al: left + horizon, size: 9pt)
    }
  })
}

// ---------------------------------------------------------------- Gantt

// data : (type: "gantt", weeks: 26, phases: (..), tasks: ((name:, start:, duration:, phase:), ..))
// `start` et `duration` en semaines depuis la semaine 0 ; `phase` = indice dans `phases`.
#let gantt-chart(data, w: 170mm) = {
  let who = "gantt-chart"
  let d = load(data, "gantt", who)
  let weeks = check-int(get(d, "weeks", who), who + ": weeks", 1, limits.max-weeks)
  let phases = get(d, "phases", who)
  check-list(phases, who + ": phases", 1, limits.max-phases)
  for p in phases { check-label(p, who + ": a phase") }
  let raw-tasks = get(d, "tasks", who)
  check-list(raw-tasks, who + ": tasks", 1, limits.max-tasks)
  let tasks = ()
  for t in raw-tasks {
    ensure(type(t) == dictionary, who + ": a task must be an object")
    check-label(get(t, "name", who + " task"), who + ": task name")
    let start = get(t, "start", who + " task")
    let dur = get(t, "duration", who + " task")
    check-num(start, who + ": start of task `" + t.name + "`")
    check-num(dur, who + ": duration of task `" + t.name + "`")
    ensure(start >= 0, who + ": start of task `" + t.name + "` must not be negative")
    ensure(dur >= limits.min-abs-value, who + ": duration of task `" + t.name + "` must be at least " + str(limits.min-abs-value))
    ensure(start + dur <= weeks, who + ": task `" + t.name + "` ends after week " + str(weeks))
    let phase = check-int(get(t, "phase", who + " task"), who + ": phase of task `" + t.name + "`", 0, phases.len() - 1)
    tasks.push((name: t.name, start: start, duration: dur, phase: phase))
  }
  let nw = 50mm; let rh = 7mm; let hh = 7mm
  let gw = w - nw; let ws = gw / weeks
  let hgt = hh + rh * tasks.len() + 9mm
  let every = calc.max(1, calc.ceil(9mm / ws))
  box(width: w, height: hgt, {
    legend(phases, nw, 0mm, w, step: 30mm)
    let y0 = 8mm
    at(nw, y0, rect(width: gw, height: hh + rh * tasks.len(), fill: c.background, stroke: none))
    for k in range(weeks + 1) {
      seg(nw + ws * k, y0 + hh, nw + ws * k, y0 + hh + rh * tasks.len(), 0.25pt + c.muted)
    }
    for k in range(weeks) {
      if calc.rem(k, every) == 0 { tbox(nw + ws * k - 1mm, y0 + 1mm, ws * every, 5mm, "S" + str(k + 1), size: 6.5pt, fill: c.muted) }
    }
    for (i, t) in tasks.enumerate() {
      let y = y0 + hh + rh * i
      seg(0pt, y, w, y, 0.25pt + c.muted)
      tbox(0pt, y, nw - 2mm, rh, t.name, al: left + horizon, size: 7.5pt)
      // the bar spans the real start and end; a bar shorter than 0.2 mm becomes a 0.2 mm
      // marker that starts at the real start and only moves back to stay inside the grid
      let bl = ws * t.start
      let br = ws * (t.start + t.duration)
      let bwid = calc.min(calc.max(0.2mm, br - bl), gw)
      let bx = calc.max(0pt, calc.min(bl, gw - bwid))
      at(nw + bx, y + 1.2mm, rect(width: bwid, height: rh - 2.4mm, radius: calc.min(0.8mm, bwid / 2), fill: series-colors.at(t.phase), stroke: none))
    }
    seg(0pt, y0 + hh + rh * tasks.len(), w, y0 + hh + rh * tasks.len(), 0.6pt + c.text)
  })
}

// ---------------------------------------------------------------- organigramme

// Disposition d'arbre : un emplacement par feuille, parent centre sur ses enfants.
#let tree-layout(nodes, id, depth, x0) = {
  let kids = nodes.filter(n => n.parent == id)
  if kids.len() == 0 {
    (width: 1, cx: x0 + 0.5, items: ((id: id, cx: x0 + 0.5, depth: depth),))
  } else {
    let cur = x0
    let items = ()
    let cxs = ()
    for k in kids {
      let r = tree-layout(nodes, k.id, depth + 1, cur)
      cur += r.width
      items += r.items
      cxs.push(r.cx)
    }
    let cx = (cxs.first() + cxs.last()) / 2
    (width: cur - x0, cx: cx, items: items + ((id: id, cx: cx, depth: depth),))
  }
}

// data : (type: "org", nodes: ((id:, name:, title:, parent:), ..)) ; `parent` = "" pour la racine,
// `title` facultatif.
#let org-chart(data, w: 170mm) = {
  let who = "org-chart"
  let d = load(data, "org", who)
  let raw = get(d, "nodes", who)
  check-list(raw, who + ": nodes", 1, limits.max-nodes)
  let ids = ()
  for n in raw {
    ensure(type(n) == dictionary, who + ": a node must be an object")
    let id = get(n, "id", who + " node")
    ensure(type(id) == str and id.len() > 0 and id.codepoints().len() <= limits.max-id, who + ": a node id must be a string of 1 to " + str(limits.max-id) + " characters")
    ensure(id not in ids, who + ": duplicate node id `" + id + "`")
    ids.push(id)
    check-label(get(n, "name", who + " node"), who + ": name of node `" + id + "`")
    // optional, but a title that is present is a label like any other (not empty)
    if "title" in n { check-label(n.title, who + ": title of node `" + id + "`") }
    ensure(type(get(n, "parent", who + " node")) == str, who + ": parent of node `" + id + "` must be a string (empty for the root)")
  }
  let nodes = raw.map(n => (id: n.id, name: n.name, title: n.at("title", default: ""), parent: n.parent))
  let roots = nodes.filter(n => n.parent == "")
  ensure(roots.len() == 1, who + ": exactly one node must have an empty parent, got " + str(roots.len()))
  for n in nodes {
    ensure(n.parent == "" or n.parent in ids, who + ": parent `" + n.parent + "` of node `" + n.id + "` does not exist")
  }
  let lay = tree-layout(nodes, roots.first().id, 0, 0)
  ensure(lay.items.len() == nodes.len(), who + ": every node must descend from the root (cycle or detached node)")
  let depth = calc.max(..lay.items.map(i => i.depth))
  ensure(depth <= limits.max-org-depth, who + ": depth " + str(depth) + " is above " + str(limits.max-org-depth))
  ensure(lay.width <= limits.max-org-leaves, who + ": " + str(lay.width) + " leaves is above " + str(limits.max-org-leaves))
  let pos = lay.items.map(i => (i.id, i)).to-dict()
  let slot = w / lay.width
  let bw = calc.min(slot - 3mm, 36mm); let pad = 1.5mm; let gap = 14mm
  let avail = bw - 2 * pad
  // The node height follows the measured content, one common height per level; a word wider
  // than the box is cut (zero-width spaces between its characters), so no text leaves its node.
  context {
    let cut(txt, size, weight) = txt.split(" ").map(word => {
      if measure(text(size: size, weight: weight, word)).width > avail { word.clusters().join(sym.zws) } else { word }
    }).join(" ")
    let cell(n, fg) = {
      set par(leading: 0.45em, justify: false)
      align(center + horizon, {
        text(size: 7.5pt, weight: "bold", fill: fg, cut(n.name, 7.5pt, "bold"))
        if n.title != "" {
          linebreak()
          text(size: 6pt, fill: fg, cut(n.title, 6pt, "regular"))
        }
      })
    }
    let fg-of(depth) = if depth <= 1 { white } else { c.text }
    let level-h = range(depth + 1).map(dep => {
      let hs = nodes.filter(n => pos.at(n.id).depth == dep).map(n => measure(width: avail, block(width: avail, cell(n, fg-of(dep)))).height)
      calc.max(13mm, calc.max(..hs) + 2 * pad)
    })
    let level-y(dep) = level-h.slice(0, dep).sum(default: 0mm) + dep * gap
    let hgt = level-h.sum() + depth * gap
    ensure(hgt <= limits.max-org-height * 1mm, who + ": the names and titles need " + str(calc.round(hgt / 1mm)) + " mm of height, above " + str(limits.max-org-height) + " mm: shorten them")
    box(width: w, height: hgt, {
      for n in nodes.filter(n => n.parent != "") {
        let p = pos.at(n.parent); let q = pos.at(n.id)
        let px = p.cx * slot; let qx = q.cx * slot
        let py = level-y(p.depth) + level-h.at(p.depth); let qy = level-y(q.depth)
        let my = (py + qy) / 2
        seg(px, py, px, my, 0.8pt + c.secondary)
        seg(px, my, qx, my, 0.8pt + c.secondary)
        seg(qx, my, qx, qy, 0.8pt + c.secondary)
      }
      for n in nodes {
        let p = pos.at(n.id)
        let x = p.cx * slot - bw / 2; let y = level-y(p.depth)
        let fillc = if p.depth == 0 { c.primary } else if p.depth == 1 { c.secondary } else { c.background }
        at(x, y, rect(width: bw, height: level-h.at(p.depth), radius: 1.2mm, fill: fillc, stroke: 0.6pt + c.secondary))
        at(x + pad, y + pad, box(width: avail, height: level-h.at(p.depth) - 2 * pad, cell(n, fg-of(p.depth))))
      }
    })
  }
}

// ---------------------------------------------------------------- processus

// data : (type: "process", steps: ((label:, kind:), ..)) ; kind = start | step | decision | end.
// Retour a la ligne (ce n'est pas un serpentin) : chaque ligne se lit de gauche a droite et un
// connecteur coude revient au debut de la ligne suivante. Le nombre d'etapes par ligne (3 a 5)
// est choisi pour equilibrer les lignes. La hauteur des etapes suit le texte mesure (commune a
// une ligne) ; un mot plus large que sa forme est coupe ; le texte d'une decision tient dans le
// rectangle inscrit dans le losange, qui grandit si besoin.
#let process-flow(data, w: 170mm) = {
  let who = "process-flow"
  let d = load(data, "process", who)
  let steps = get(d, "steps", who)
  check-list(steps, who + ": steps", 1, limits.max-steps)
  for s in steps {
    ensure(type(s) == dictionary, who + ": a step must be an object")
    check-label(get(s, "label", who + " step"), who + ": step label")
    ensure(get(s, "kind", who + " step") in ("start", "step", "decision", "end"), who + ": step kind must be start, step, decision or end")
  }
  let rows = calc.ceil(steps.len() / 5)
  let per-row = calc.ceil(steps.len() / rows)
  let slot = w / per-row; let bw = slot - 9mm; let min-h = 16mm; let gap = 22mm
  let pad = 1.5mm
  let radius(kind) = if kind == "step" { 1.2mm } else { 6mm }
  context {
    // text area of a step: the box inside the shape (corners of a rounded shape cut 0.3 r)
    let area-w(kind) = if kind == "decision" { 0.46 * bw } else { bw - 2 * pad - 0.6 * radius(kind) }
    let size-of(kind) = if kind == "decision" { 6.5pt } else { 7pt }
    let cut-label(s) = cut-words(s.label, size-of(s.kind), "bold", area-w(s.kind))
    let body(s) = block(width: area-w(s.kind), align(center + horizon, {
      set par(leading: 0.4em, justify: false)
      text(size: size-of(s.kind), weight: "bold", fill: white, cut-label(s))
    }))
    let need(s) = {
      let th = measure(width: area-w(s.kind), body(s)).height
      if s.kind == "decision" { calc.max(min-h, 2 * th) } else { calc.max(min-h, th + 2 * pad + 0.6 * radius(s.kind)) }
    }
    let row-h = range(rows).map(r => calc.max(..steps.enumerate().filter(((i, s)) => calc.floor(i / per-row) == r).map(((i, s)) => need(s))))
    let row-y(r) = row-h.slice(0, r).sum(default: 0mm) + r * gap
    let hgt = row-h.sum() + (rows - 1) * gap
    ensure(hgt <= limits.max-process-height * 1mm, who + ": the labels need " + str(calc.round(hgt / 1mm)) + " mm of height, above " + str(limits.max-process-height) + " mm: shorten them")
    box(width: w, height: hgt, {
      for (i, s) in steps.enumerate() {
        let r = calc.floor(i / per-row); let k = calc.rem(i, per-row)
        let bh = row-h.at(r)
        let x = slot * k + (slot - bw) / 2; let y = row-y(r)
        let aw = area-w(s.kind)
        if s.kind == "decision" {
          at(x, y, polygon(fill: c.accent, stroke: none, (bw / 2, 0pt), (bw, bh / 2), (bw / 2, bh), (0pt, bh / 2)))
        } else {
          let fc = if s.kind == "step" { c.secondary } else { c.primary }
          at(x, y, rect(width: bw, height: bh, radius: radius(s.kind), fill: fc, stroke: none))
        }
        at(x + (bw - aw) / 2, y, box(width: aw, height: bh, align(horizon, body(s))))
        tbox(x - 1mm, y - 4.5mm, 6mm, 4mm, str(i + 1), size: 7pt, fill: c.muted, al: left + horizon)
        if k < per-row - 1 and i < steps.len() - 1 {
          seg(x + bw + 0.5mm, y + bh / 2, x + slot - 0.5mm, y + bh / 2, 1.2pt + c.text)
          at(x + slot - 2.6mm, y + bh / 2 - 1.2mm, polygon(fill: c.text, stroke: none, (0pt, 0pt), (2.4mm, 1.2mm), (0pt, 2.4mm)))
        }
        if k == per-row - 1 and i < steps.len() - 1 {
          let cx = x + bw / 2; let y1 = y + bh; let y2 = y + bh + gap / 2
          let x2 = slot / 2
          seg(cx, y1, cx, y2, 1.2pt + c.text)
          seg(cx, y2, x2, y2, 1.2pt + c.text)
          seg(x2, y2, x2, y + bh + gap - 2.4mm, 1.2pt + c.text)
          at(x2 - 1.2mm, y + bh + gap - 2.6mm, polygon(fill: c.text, stroke: none, (0pt, 0pt), (2.4mm, 0pt), (1.2mm, 2.4mm)))
        }
      }
    })
  }
}
