// Gabarit de reference `meeting-minutes` (compte rendu de reunion).
// Contrat : contracts/template/v1/.
#import "/brand.typ": brand, locale
#import "/fragments/header.typ": doc-header
#import "/fragments/footer.typ": doc-footer
#import "/fragments/legal-mentions.typ": legal-mentions
#import "/fragments/lines-table.typ": lines-table

#set page(
  paper: brand.page.paper,
  margin: brand.page.margins,
  footer: doc-footer([{{ .company.legal_name }} - {{ .i18n.strings.minutes_title }}]),
)
#set text(font: (brand.fonts.body, "Libertinus Serif"), size: 10pt, fill: brand.colors.text, lang: locale)

#doc-header([{{ .company.trade_name }}], [{{ .i18n.strings.minutes_title }}], [{{ .doc.number }}], [{{ .doc.date }}])

*{{ .i18n.strings.minutes_meeting }}* : {{ .data.meeting.title }} \
*{{ .i18n.strings.minutes_date }}* : {{ .data.meeting.date }} {{ .data.meeting.time | default "" }} \
*{{ .i18n.strings.minutes_place }}* : {{ .data.meeting.place | default "-" }}

#v(4mm)
== {{ .i18n.strings.minutes_participants }}
{{ range .data.participants }}
- *{{ .name }}*, {{ .role }}{{ if not .present }} ({{ $.i18n.strings.minutes_absent }}){{ end }}
{{- end }}

== {{ .i18n.strings.minutes_agenda }}
{{ range .data.agenda }}
+ {{ . }}
{{- end }}

== {{ .i18n.strings.minutes_decisions }}
{{ range .data.decisions }}
- {{ . }}
{{- end }}

== {{ .i18n.strings.minutes_actions }}
#lines-table(
  (
    [{{ .i18n.strings.minutes_col_action }}],
    [{{ .i18n.strings.minutes_col_owner }}],
    [{{ .i18n.strings.minutes_col_due }}],
  ),
  (
{{- range .data.actions }}
    ([{{ .description }}], [{{ .owner }}], [{{ .due_date }}]),
{{- end }}
  ),
  right-from: 3,
)

#legal-mentions([{{ .i18n.strings.common_legal_notice }}], [{{ .company.legal_mentions }}])
