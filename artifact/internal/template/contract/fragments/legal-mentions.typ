// Fragment partage : mentions legales. Le texte vient de la fiche entreprise
// (`company.legal_mentions`), jamais ecrit en dur dans un gabarit.
#import "/brand.typ": brand

#let legal-mentions(title, body) = {
  v(6mm)
  block(breakable: false, width: 100%)[
    #set text(size: 8pt, fill: brand.colors.muted)
    #text(weight: "bold")[#title] \
    #body
  ]
}
