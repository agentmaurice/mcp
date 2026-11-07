// Fragment partage : en-tete de document. Fonction pure, aucune balise Go.
// Les valeurs arrivent en contenu ([...]) : le moteur les a deja echappees.
#import "/brand.typ": brand

#let doc-header(company-name, doc-title, doc-number, doc-date) = {
  grid(
    columns: (1fr, auto),
    gutter: 8mm,
    align: (left + horizon, right + horizon),
    {
      if brand.logo != none {
        image(brand.logo, height: 12mm, alt: brand.logo-alt)
      } else {
        text(font: brand.fonts.heading, size: 14pt, weight: "bold", fill: brand.colors.primary, company-name)
      }
    },
    {
      text(size: 9pt, fill: brand.colors.muted)[#doc-number \ #doc-date]
    },
  )
  v(2mm)
  line(length: 100%, stroke: 0.8pt + brand.colors.primary)
  v(4mm)
  text(font: brand.fonts.heading, size: 20pt, weight: "bold", fill: brand.colors.primary, doc-title)
  v(4mm)
}
