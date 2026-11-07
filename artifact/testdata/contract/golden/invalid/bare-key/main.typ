// Gabarit de reference `quote` (devis). Contrat : contracts/template/v1/.
// Le gabarit affiche ; il ne calcule aucun montant (totaux fournis dans data).
#import "/brand.typ": brand, locale
#import "/fragments/header.typ": doc-header
#import "/fragments/footer.typ": doc-footer
#import "/fragments/legal-mentions.typ": legal-mentions
#import "/fragments/lines-table.typ": lines-table

#set page(
  paper: brand.page.paper,
  margin: brand.page.margins,
  footer: doc-footer([{{ .company.legal_name }} - {{ .company.siret }}]),
)
#set text(font: (brand.fonts.body, "Libertinus Serif"), size: 10pt, fill: brand.colors.text, lang: locale)
#set par(justify: false)

#doc-header([{{ .company.trade_name }}], [{{ .i18n.strings.quote_title }} {{ .doc.number }}], [{{ .i18n.strings.quote_reference }} {{ reference }}], [{{ .doc.date }}])

#grid(
  columns: (1fr, 1fr),
  gutter: 10mm,
  [
    *{{ .company.legal_name }}* \
    {{ .company.address.line1 }} \
    {{ .company.address.postal_code }} {{ .company.address.city }} \
    {{ .company.email }}
  ],
  [
    *{{ .i18n.strings.quote_customer }}* \
    {{ .data.customer.name }} \
{{- range .data.customer.address_lines }}
    {{ . }} \
{{- end }}
    {{ .data.customer.contact }}
  ],
)

#v(6mm)

#lines-table(
  (
    [{{ .i18n.strings.quote_col_description }}],
    [{{ .i18n.strings.quote_col_quantity }}],
    [{{ .i18n.strings.quote_col_unit_price }}],
    [{{ .i18n.strings.quote_col_vat }}],
    [{{ .i18n.strings.quote_col_total }}],
  ),
  (
{{- range .data.lines }}
    ([{{ .description }}], [{{ .quantity }}], [{{ .unit_price_ht }}], [{{ .vat_rate }}], [{{ .total_ht }}]),
{{- end }}
  ),
)

#v(4mm)
#align(right, block(width: 60%, table(
  columns: (1fr, auto),
  align: (left, right),
  stroke: none,
  inset: (x: 2mm, y: 1.5mm),
  [{{ .i18n.strings.quote_total_ht }}], [{{ .data.totals.total_ht }}],
{{- range .data.totals.vat }}
  [{{ $.i18n.strings.quote_total_vat }} {{ .rate }}], [{{ .amount }}],
{{- end }}
  [*{{ .i18n.strings.quote_total_ttc }}*], [*{{ .data.totals.total_ttc }}*],
)))

#v(4mm)
*{{ .i18n.strings.quote_valid_until }}* {{ .data.valid_until }}

#v(2mm)
*{{ .i18n.strings.quote_terms }}*
{{ range .data.terms }}
- {{ . }}
{{- end }}

*{{ .i18n.strings.quote_payment }}* {{ .data.payment_terms }}

#legal-mentions([{{ .i18n.strings.common_legal_notice }}], [{{ .company.legal_mentions }}])
