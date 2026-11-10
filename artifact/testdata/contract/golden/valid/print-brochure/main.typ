// Gabarit de reference `print-brochure` : plaquette recto verso pour l'impression.
// Contrat : contracts/template/v1/ (option de rendu `print`, fragment print.typ).
#import "/brand.typ": brand, locale
#import "/fragments/print.typ": print-layout

#show: print-layout.with(
  paper: brand.page.paper,
  margin: (top: brand.page.margins.top, bottom: brand.page.margins.bottom, inside: brand.page.margins.left, outside: brand.page.margins.right),
  background: bleed => place(top, rect(width: 100%, height: 16mm + bleed, fill: brand.colors.primary, stroke: none)),
)
#set text(font: (brand.fonts.body, "Libertinus Serif"), size: 11pt, fill: brand.colors.text, lang: locale)
#set par(justify: true)
#show heading.where(level: 1): set text(fill: brand.colors.primary, size: 24pt)
#show heading.where(level: 2): set text(fill: brand.colors.secondary, size: 15pt)

#v(30mm)
= {{ .data.title }}
{{ if .data.subtitle }}
#text(size: 13pt, fill: brand.colors.muted)[{{ .data.subtitle }}]
{{ end }}
{{ range .data.sections }}
#pagebreak()
#v(8mm)
== {{ .heading }}
{{ range .paragraphs }}
{{ . }}
{{ end }}
{{ end }}
