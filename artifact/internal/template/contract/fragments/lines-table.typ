// Fragment partage : tableau de lignes (devis, facture lisible, commande).
// `headers` : tableau de contenus. `rows` : tableau de tableaux de contenus.
// Le fragment affiche ; il ne calcule rien (les montants sont fournis en amont).
#import "/brand.typ": brand

#let lines-table(headers, rows, widths: auto, right-from: 1) = {
  let ncols = headers.len()
  let aligns = range(ncols).map(i => if i >= right-from { right } else { left })
  table(
    columns: if widths == auto { (1fr,) + (auto,) * (ncols - 1) } else { widths },
    align: aligns,
    inset: (x: 3mm, y: 2.2mm),
    stroke: (x: none, y: 0.4pt + brand.colors.muted),
    fill: (_, y) => if y == 0 { brand.colors.primary } else { none },
    ..headers.map(h => text(fill: white, weight: "bold", h)),
    ..rows.flatten(),
  )
}
