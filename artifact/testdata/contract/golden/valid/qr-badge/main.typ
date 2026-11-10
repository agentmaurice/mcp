// Gabarit de reference `qr-badge` : un QR code par niveau de correction (contrat qr/v1).
// `qrModules` rend la matrice en texte ; elle passe entre crochets, par reference, jamais dans le code.
#import "/brand.typ": brand, locale
#import "/fragments/qr.typ": qr-code

#set page(paper: "a6", margin: 12mm)
#set text(font: (brand.fonts.body, "Libertinus Serif"), size: 10pt, fill: brand.colors.text, lang: locale)
#show heading.where(level: 1): set text(fill: brand.colors.primary, size: 15pt)

#heading(level: 1)[{{ .data.title }}]
{{ .data.reference }}
{{ if .data.caption }}
_{{ .data.caption }}_
{{ end }}
#v(3mm)
#grid(
  columns: (1fr, 1fr),
  row-gutter: 4mm,
  align(center)[#qr-code([{{ qrModules .data.payment_url "L" }}], size: 32mm) \ L],
  align(center)[#qr-code([{{ qrModules .data.payment_url }}], size: 32mm) \ M],
  align(center)[#qr-code([{{ qrModules .data.payment_url "Q" }}], size: 32mm) \ Q],
  align(center)[#qr-code([{{ qrModules .data.payment_url "H" }}], size: 32mm) \ H],
)
