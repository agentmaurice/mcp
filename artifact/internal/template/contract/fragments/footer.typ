// Fragment partage : pied de page avec pagination. A passer a `set page(footer: ...)`.
#import "/brand.typ": brand

#let doc-footer(company-line) = {
  set text(size: 8pt, fill: brand.colors.muted)
  line(length: 100%, stroke: 0.4pt + brand.colors.muted)
  v(1mm)
  grid(
    columns: (1fr, auto),
    align: (left, right),
    company-line,
    context [#counter(page).display() / #counter(page).final().first()],
  )
}
