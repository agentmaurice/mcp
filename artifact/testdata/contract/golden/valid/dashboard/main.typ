// Gabarit de reference `dashboard` : les six graphiques chart/v1, une page chacun.
// Les donnees passent en JSON par reference (`| json` entre crochets), jamais dans le code.
#import "/brand.typ": brand, locale
#import "/fragments/charts.typ": bar-chart, line-chart, pie-chart, gantt-chart, org-chart, process-flow

#set page(paper: brand.page.paper, margin: brand.page.margins)
#set text(font: (brand.fonts.body, "Libertinus Serif"), size: 10pt, fill: brand.colors.text, lang: locale)
#show heading.where(level: 1): set text(fill: brand.colors.primary, size: 17pt)

#heading(level: 1)[{{ .data.title }}]
#v(4mm)
*{{ .data.titles.bar }}*
#v(2mm)
#bar-chart([{{ .data.bar | json }}])

#pagebreak()
*{{ .data.titles.line }}*
#v(2mm)
#line-chart([{{ .data.line | json }}])

#pagebreak()
*{{ .data.titles.pie }}*
#v(2mm)
#pie-chart([{{ .data.pie | json }}])

#pagebreak()
*{{ .data.titles.gantt }}*
#v(2mm)
#gantt-chart([{{ .data.gantt | json }}])

#pagebreak()
*{{ .data.titles.org }}*
#v(2mm)
#org-chart([{{ .data.org | json }}])

#pagebreak()
*{{ .data.titles.process }}*
#v(2mm)
#process-flow([{{ .data.process | json }}])
