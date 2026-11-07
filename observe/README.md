# AgentMaurice Observe MCP

Deterministic diagnostic Go MCP server for OTLP JSON exports and already
redacted run summaries. It normalizes a timeline, computes the critical path
and highlights errors, retries and regressions between two runs.

Tools:

- `observe_health_v1`
- `observe_capabilities_v1`
- `observe_run_v1`
- `observe_explain_v1`
- `observe_compare_v1`

v0.1 does not connect directly to Tempo, Loki, Prometheus or ClickHouse. The
inline JSON input is capped at 2 MiB and must contain no raw prompt, no
credential and no sensitive business payload.

Pseudonymes et limites : toute valeur libre de métadonnée (identifiant de workflow,
modèle, nom, action, adresse ou autre valeur arbitraire) est pseudonymisée par un
HMAC à clé aléatoire process-local ; seuls les enums structurés, les versions
validées et les références canoniques de corrélation restent lisibles. La clé
n'est pas persistée ou partagée entre redémarrages : la corrélation HMAC est donc
garantie uniquement pendant la vie du processus et ne constitue pas une promesse
de corrélation interprocessus pour des valeurs non canoniques. La requête de cohorte applique un
budget coopératif de 2 secondes côté MCP et des limites de désérialisation
(8 MiB de chaînes, 100 000 nœuds, profondeur 32) ; la sérialisation JSON
préalable reste non interruptible.

## Statut

Propriétaire AgentMaurice.
