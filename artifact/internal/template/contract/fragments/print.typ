// Fragment partage : mise en page d'impression professionnelle (option `print`, AGENT-1921).
// Usage : `#show: print-layout.with(paper: "a5", margin: (top: 15mm, bottom: 15mm, inside: 20mm, outside: 12mm))`
// Sans option `print` (print-options absente), la mise en page est celle d'un `set page` ordinaire.
#import "/brand.typ" as brand-module
// La plateforme n'ecrit `print-options` dans /brand.typ que si l'option `print` est demandee.
#let print-options = dictionary(brand-module).at("print-options", default: none)

#let paper-sizes = (
  a3: (297mm, 420mm), a4: (210mm, 297mm), a5: (148mm, 210mm), a6: (105mm, 148mm),
  "us-letter": (215.9mm, 279.4mm),
)

// margin : une longueur (les quatre cotes) ou un dictionnaire top, bottom et inside, outside (ou left, right).
#let print-margin(margin) = {
  if type(margin) != dictionary { return (top: margin, bottom: margin, inside: margin, outside: margin) }
  (
    top: margin.at("top", default: 0mm),
    bottom: margin.at("bottom", default: 0mm),
    inside: margin.at("inside", default: margin.at("left", default: 0mm)),
    outside: margin.at("outside", default: margin.at("right", default: 0mm)),
  )
}

// Traits de coupe : deux traits par angle du format fini, dans la zone de marque (hors du fond perdu).
#let print-crop-marks(w, h, pad, bleed) = {
  let len = 5mm
  let off = bleed + 1mm
  let stroke = 0.25pt + black
  for (sx, sy) in ((0, 0), (1, 0), (0, 1), (1, 1)) {
    let cx = pad + sx * w
    let cy = pad + sy * h
    let x0 = if sx == 0 { cx - off - len } else { cx + off }
    let y0 = if sy == 0 { cy - off - len } else { cy + off }
    place(top + left, dx: x0, dy: cy, line(length: len, stroke: stroke))
    place(top + left, dx: cx, dy: y0, line(angle: 90deg, length: len, stroke: stroke))
  }
}

// background : fonction `bleed => contenu`, dessinee sur la zone fini + fond perdu (le contenu
// d'un aplat qui touche le bord doit deborder de `bleed`). Sans impression, bleed vaut 0mm.
#let print-layout(paper: "a4", margin: 15mm, background: none, body) = {
  let m = print-margin(margin)
  if print-options == none {
    set page(
      paper: paper,
      margin: (top: m.top, bottom: m.bottom, left: m.inside, right: m.outside),
      background: if background != none { background(0mm) },
    )
    body
  } else {
    let (w, h) = paper-sizes.at(paper)
    let bleed = print-options.bleed
    let pad = bleed + print-options.slug
    let margins = if print-options.mirror {
      (top: m.top + pad, bottom: m.bottom + pad, inside: m.inside + pad, outside: m.outside + pad)
    } else {
      (top: m.top + pad, bottom: m.bottom + pad, left: m.inside + pad, right: m.outside + pad)
    }
    set page(
      width: w + 2 * pad,
      height: h + 2 * pad,
      margin: margins,
      binding: left,
      background: if background != none {
        place(top + left, dx: print-options.slug, dy: print-options.slug,
          block(width: w + 2 * bleed, height: h + 2 * bleed, background(bleed)))
      },
      foreground: if print-options.crop { print-crop-marks(w, h, pad, bleed) },
    )
    body
  }
}
